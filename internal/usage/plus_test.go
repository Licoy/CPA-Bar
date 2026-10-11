package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpamp"
	"github.com/licoy/cpa-bar/internal/cpamp/cpamptest"
)

func plusEvent(id string, at time.Time) map[string]any {
	sum := sha256.Sum256([]byte("sk-client-0123456789abcdef"))
	return map[string]any{
		"request_id": id, "event_hash": "h-" + id, "timestamp_ms": int(at.UnixMilli()),
		"model": "claude-sonnet-4-5", "requested_model": "sonnet", "endpoint": "POST /v1/messages",
		"auth_index": "1", "source": "sk-upstream-0123456789abcdefghijklmnop", "api_key_hash": hex.EncodeToString(sum[:]),
		"auth_provider_snapshot": "claude", "input_tokens": 100, "output_tokens": 20, "cache_read_tokens": 40,
		"total_tokens": 120, "latency_ms": 900, "ttft_ms": 300, "stream": true,
		"client_ip": "203.0.113.9", "user_agent": "curl/8", "x_forwarded_for": "203.0.113.9",
	}
}

// An event keeps what a stream record keeps: the client's key only as the
// fingerprint, an upstream key masked, nothing of the client's address.
func TestFromManagerPlusRedacts(t *testing.T) {
	raw, _ := json.Marshal(plusEvent("r1", time.UnixMilli(1_700_000_000_000)))
	var e cpamp.Event
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	r, ok := FromManagerPlus(e)
	if !ok {
		t.Fatal("not a record")
	}
	if want, _ := keyFingerprint("sk-client-0123456789abcdef"); r.KeyID != want {
		t.Errorf("key id %q, want the stream's %q", r.KeyID, want)
	}
	if r.Model != "claude-sonnet-4-5" || r.DisplayModel() != "sonnet" || r.Provider != "claude" || !r.Stream {
		t.Errorf("record %+v", r)
	}
	if r.Tokens.CacheRead != 40 || r.Tokens.Total != 120 || r.LatencyMs != 900 || r.TTFTMs != 300 {
		t.Errorf("tokens %+v", r.Tokens)
	}
	out, _ := json.Marshal(r)
	for _, leak := range []string{"203.0.113.9", "curl", "sk-upstream", "sk-client"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("record keeps %q: %s", leak, out)
		}
	}

	failed := e
	failed.Failed, failed.FailSummary = true, strings.Repeat("x", 1000)
	if r, _ := FromManagerPlus(failed); len([]rune(r.Error)) > maxErrorLen+1 {
		t.Errorf("fail summary kept at %d runes", len([]rune(r.Error)))
	}
	if _, ok := FromManagerPlus(cpamp.Event{}); ok {
		t.Error("an empty event is a record")
	}
}

// Manager-Plus is read instead of the stream, again on schedule, without
// keeping a record twice.
func TestManagerReadsManagerPlus(t *testing.T) {
	old := plusEvery
	plusEvery = 30 * time.Millisecond
	defer func() { plusEvery = old }()

	resp := newFakeRESP(t, "")
	mp := cpamptest.New(t, "cpamp_k")
	now := time.Now()
	mp.Add(plusEvent("r3", now.Add(-time.Minute)), plusEvent("r2", now.Add(-time.Hour)), plusEvent("r1", now.Add(-40*24*time.Hour)))
	pc, _ := cpamp.New(cpamp.Options{BaseURL: mp.URL, AdminKey: "cpamp_k"})

	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	m.Sync([]Target{{ID: "i1", Client: clientFor(t, resp.ln.Addr().String(), testKey), Plus: pc}})
	waitUntil(t, "the first read", func() bool { return m.Status("i1").Received == 2 })
	if s := m.Status("i1"); !s.ManagerPlus || s.State != StateLive {
		t.Errorf("status %+v", s)
	}

	mp.Add(plusEvent("r4", time.Now()))
	waitUntil(t, "the next read", func() bool { return m.Status("i1").Received == 3 })
	time.Sleep(100 * time.Millisecond) // more reads, of what is kept
	var ids []string
	m.Read("i1", func(rs []Record) {
		for _, r := range rs {
			ids = append(ids, r.RequestID)
		}
	})
	if strings.Join(ids, ",") != "r2,r3,r4" {
		t.Errorf("kept %q: outside the retention, or twice", ids)
	}
	// The client keys are asked of CPA over HTTP; the stream is never
	// opened.
	if n := resp.auths.Load(); n != 0 {
		t.Errorf("%d logins to CPA's stream", n)
	}
}

// A refused admin key is not sent again until it changes.
func TestManagerPlusRejected(t *testing.T) {
	old := plusEvery
	plusEvery = 30 * time.Millisecond
	defer func() { plusEvery = old }()

	mp := cpamptest.New(t, "cpamp_right")
	pc, _ := cpamp.New(cpamp.Options{BaseURL: mp.URL, AdminKey: "cpamp_wrong"})
	m := New(Options{Dir: t.TempDir()})
	defer m.Close()
	m.Sync([]Target{{ID: "i1", Plus: pc}})
	waitUntil(t, "the refusal", func() bool { return m.Status("i1").State == StateRejected })
	time.Sleep(retryMin + 200*time.Millisecond)
	if n := len(mp.Paths()); n != 1 {
		t.Errorf("%d requests after a refusal: %q", n, mp.Paths())
	}
}
