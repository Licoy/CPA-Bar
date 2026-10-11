package usage

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

const testKey = "mgmt-secret"

// sample is a usage record as CPA publishes it, with every field that
// identifies the client.
func sample(id, model string, failed bool) string {
	return fmt.Sprintf(`{"timestamp":%q,"request_id":%q,"provider":"claude","executor_type":"ClaudeExecutor",
		"model":%q,"alias":"sonnet","endpoint":"POST /v1/messages","auth_type":"oauth","auth_index":"1",
		"source":"pro@example.com","api_key":"sk-live-0123456789abcdef","client_ip":"203.0.113.9",
		"x_forwarded_for":"203.0.113.9","user_agent":"curl/8","response_headers":{"Set-Cookie":["s=1"]},
		"failed":%t,"fail":{"status_code":%d,"body":"upstream said no"},"latency_ms":1200,"ttft_ms":300,
		"tokens":{"input_tokens":100,"output_tokens":50,"reasoning_tokens":10,"cache_read_tokens":40,"total_tokens":150}}`,
		time.Now().UTC().Format(time.RFC3339Nano), id, model, failed, map[bool]int{true: 429, false: 200}[failed])
}

func TestParseRedacts(t *testing.T) {
	r, ok := Parse([]byte(sample("r1", "claude-sonnet-4-5", true)), time.Now())
	if !ok {
		t.Fatal("not parsed")
	}
	if r.KeyMask != "••••cdef" || r.KeyID == "" || strings.Contains(r.KeyID, "sk-") {
		t.Errorf("key kept as %q / %q", r.KeyID, r.KeyMask)
	}
	if r.DisplayModel() != "sonnet" || r.Model != "claude-sonnet-4-5" {
		t.Errorf("model %q alias %q", r.Model, r.Alias)
	}
	if !r.Failed || r.Status != 429 || r.Error != "upstream said no" || r.Canceled {
		t.Errorf("failure %+v", r)
	}
	if r.Tokens != (Tokens{Input: 100, Output: 50, Reasoning: 10, CacheRead: 40, Total: 150}) {
		t.Errorf("tokens %+v", r.Tokens)
	}
	// API-key credentials have the upstream key as their source.
	ak, _ := Parse([]byte(`{"request_id":"r2","auth_type":"apikey","source":"sk-upstream-secret-key-9876"}`), time.Now())
	if ak.Source != "••••9876" {
		t.Errorf("api-key source kept as %q", ak.Source)
	}
	if r.Source != "pro@example.com" {
		t.Errorf("account source %q", r.Source)
	}
	for _, msg := range []string{`{"support_refresh":true}`, `{"refresh":true}`, `null`, `not json`} {
		if _, ok := Parse([]byte(msg), time.Now()); ok {
			t.Errorf("%s parsed as a record", msg)
		}
	}
}

func TestStoreKeepsAndExpires(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := newStore(dir, func() time.Time { return now })
	old := Record{Time: now.Add(-Retention - time.Hour), RequestID: "old"}
	if err := s.add("i1", old, Record{Time: now.Add(-time.Minute), RequestID: "new", KeyMask: "••••cdef"}); err != nil {
		t.Fatal(err)
	}
	s.flush()
	s.close()

	data, err := os.ReadFile(filepath.Join(dir, "i1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "i1.jsonl")); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if strings.Contains(string(data), "sk-live") {
		t.Error("the key reached the disk")
	}
	s2 := newStore(dir, func() time.Time { return now })
	var ids []string
	s2.read("i1", func(rs []Record) {
		for _, r := range rs {
			ids = append(ids, r.RequestID)
		}
	})
	if strings.Join(ids, ",") != "new" {
		t.Errorf("reloaded %v, want only the unexpired record", ids)
	}
	if err := s2.clear("i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "i1.jsonl")); !os.IsNotExist(err) {
		t.Error("clear left the file")
	}
}

func TestSummarize(t *testing.T) {
	now := time.Now()
	var recs []Record
	for i, s := range []string{sample("a", "m1", false), sample("b", "m1", true), sample("c", "m2", false)} {
		r, _ := Parse([]byte(s), now)
		r.Time = now.Add(time.Duration(i-3) * time.Minute)
		recs = append(recs, r)
	}
	recs[2].Alias = ""
	s := Summarize(recs, Filter{From: now.Add(-time.Hour)}, 10*time.Minute, now)
	if s.Requests != 3 || s.Success != 2 || s.Failed != 1 || s.Tokens.Total != 450 {
		t.Fatalf("summary %+v", s)
	}
	if rate, ok := s.SuccessRate(); !ok || rate < 0.66 || rate > 0.67 {
		t.Errorf("success rate %v", rate)
	}
	if len(s.Models) != 2 || s.Models[0].Key != "sonnet" || s.Models[0].Requests != 2 {
		t.Errorf("models %+v", s.Models)
	}
	if len(s.Keys) != 1 || s.Keys[0].Label != "••••cdef" {
		t.Errorf("keys %+v", s.Keys)
	}
	var sum int64
	for _, b := range s.Buckets {
		sum += b.Tokens
	}
	if sum != 450 || len(s.Buckets) < 6 {
		t.Errorf("%d buckets with %d tokens", len(s.Buckets), sum)
	}
	f := Filter{From: now.Add(-time.Hour), Result: ResultFailed}
	if s := Summarize(recs, f, 0, now); s.Requests != 1 || len(s.Options.Models) != 2 {
		t.Errorf("failed filter %+v", s)
	}
	page, total := Recent(recs, Filter{}, 1, 1)
	if total != 3 || len(page) != 1 || page[0].RequestID != "b" {
		t.Errorf("recent %v of %d", page, total)
	}
}

// fakeRESP answers AUTH and SUBSCRIBE as CPA does and publishes what is
// sent on pub.
type fakeRESP struct {
	ln    net.Listener
	pub   chan string
	auths atomic.Int32
	conns atomic.Int32
	reply string // to AUTH; "" for +OK
	mu    sync.Mutex
	open  []net.Conn
}

func newFakeRESP(t *testing.T, reply string) *fakeRESP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRESP{ln: ln, pub: make(chan string, 16), reply: reply}
	t.Cleanup(func() {
		_ = ln.Close()
		f.dropAll()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			f.mu.Lock()
			f.open = append(f.open, c)
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeRESP) dropAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.open {
		_ = c.Close()
	}
	f.open = nil
}

func (f *fakeRESP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	bulk := func(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }
	for {
		args, err := readCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		switch strings.ToUpper(args[0]) {
		case "AUTH":
			f.auths.Add(1)
			if f.reply != "" || args[1] != testKey {
				_, _ = io.WriteString(c, "-"+f.reply+"\r\n")
				continue
			}
			_, _ = io.WriteString(c, "+OK\r\n")
		case "SUBSCRIBE":
			_, _ = io.WriteString(c, "*3\r\n"+bulk("subscribe")+bulk("usage")+":1\r\n")
			_, _ = io.WriteString(c, "*3\r\n"+bulk("message")+bulk("usage")+bulk(`{"support_refresh":true}`))
			go func() {
				for msg := range f.pub {
					if _, err := io.WriteString(c, "*3\r\n"+bulk("message")+bulk("usage")+bulk(msg)); err != nil {
						f.pub <- msg
						return
					}
				}
			}()
		}
	}
}

// readCommand reads "*N\r\n" and N bulk strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	line := func() (string, error) {
		s, err := r.ReadString('\n')
		return strings.TrimRight(s, "\r\n"), err
	}
	head, err := line()
	if err != nil || !strings.HasPrefix(head, "*") {
		return nil, io.EOF
	}
	n, _ := strconv.Atoi(head[1:])
	var out []string
	for range n {
		h, err := line()
		if err != nil {
			return nil, err
		}
		size, _ := strconv.Atoi(strings.TrimPrefix(h, "$"))
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:size]))
	}
	return out, nil
}

func clientFor(t *testing.T, addr, key string) *cpa.Client {
	t.Helper()
	c, err := cpa.New(cpa.Options{InstanceID: "i1", BaseURL: "http://" + addr, ManagementKey: key, ReadTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerCollects(t *testing.T) {
	srv := newFakeRESP(t, "")
	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	var notes atomic.Int32
	m.Subscribe(func() { notes.Add(1) })
	c := clientFor(t, srv.ln.Addr().String(), testKey)
	m.Sync([]Target{{ID: "i1", Client: c}})
	waitUntil(t, "live", func() bool { return m.Status("i1").State == StateLive })

	srv.pub <- sample("r1", "m1", false)
	srv.pub <- sample("r2", "m1", true)
	waitUntil(t, "two records", func() bool { return m.Status("i1").Received == 2 })
	var n int
	m.Read("i1", func(rs []Record) { n = len(rs) })
	if n != 2 {
		t.Errorf("%d records kept", n)
	}

	// A dropped connection is opened again.
	srv.dropAll()
	waitUntil(t, "the reconnect", func() bool { return srv.conns.Load() >= 2 && m.Status("i1").State == StateLive })

	// Turning collection off closes the stream.
	m.SetEnabled(false)
	m.Sync([]Target{{ID: "i1", Client: c}})
	if s := m.Status("i1"); s.State != StateOff {
		t.Errorf("state %v after turning off", s.State)
	}
	if notes.Load() == 0 {
		t.Error("no change notifications")
	}
}

// A refused key is sent once: every refusal counts towards a ban.
func TestManagerStopsOnRejectedKey(t *testing.T) {
	srv := newFakeRESP(t, "ERR invalid management key")
	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	c := clientFor(t, srv.ln.Addr().String(), "wrong")
	m.Sync([]Target{{ID: "i1", Client: c}})
	waitUntil(t, "the refusal", func() bool { return m.Status("i1").State == StateRejected })
	time.Sleep(retryMin + 500*time.Millisecond)
	if n := srv.auths.Load(); n != 1 {
		t.Errorf("%d AUTH attempts, want 1", n)
	}
	// A new client (a new key) tries again.
	m.Sync([]Target{{ID: "i1", Client: clientFor(t, srv.ln.Addr().String(), testKey)}})
	waitUntil(t, "the second attempt", func() bool { return srv.auths.Load() == 2 })
}

func TestManagerHomeMode(t *testing.T) {
	srv := newFakeRESP(t, "ERR redis usage output disabled in home mode")
	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	m.Sync([]Target{{ID: "i1", Client: clientFor(t, srv.ln.Addr().String(), testKey)}})
	waitUntil(t, "home mode", func() bool { return m.Status("i1").State == StateHomeMode })
}
