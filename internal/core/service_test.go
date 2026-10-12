package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/cpamp/cpamptest"
	"github.com/licoy/cpa-bar/internal/store"
)

var bg = context.Background()

// ---- fakes ----

type fakeSettings struct {
	mu      sync.Mutex
	s       store.Settings
	saveErr error
	saves   int
}

func (f *fakeSettings) Load() (store.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s.Clone(), nil
}

func (f *fakeSettings) Save(s store.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.s = s.Clone()
	f.saves++
	return nil
}

func (f *fakeSettings) setErr(err error) {
	f.mu.Lock()
	f.saveErr = err
	f.mu.Unlock()
}

func (f *fakeSettings) snapshot() (store.Settings, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.s.Clone(), f.saves
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// gauge counts requests in flight across several fake servers.
type gauge struct {
	mu        sync.Mutex
	cur, peak int
	total     int
}

func (g *gauge) enter() {
	g.mu.Lock()
	g.cur++
	g.total++
	g.peak = max(g.peak, g.cur)
	g.mu.Unlock()
}

func (g *gauge) leave() {
	g.mu.Lock()
	g.cur--
	g.mu.Unlock()
}

func (g *gauge) get() (cur, peak, total int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cur, g.peak, g.total
}

// fakeCPA is one instance's management server.
type fakeCPA struct {
	srv *httptest.Server
	key string

	mu       sync.Mutex
	files    []map[string]any
	status   int           // non-zero: answer /credentials with this status and an empty body
	body     string        // non-empty: answer /credentials 200 with this body
	block    chan struct{} // non-nil: /credentials waits for it to close
	requests int           // GET /credentials received
	canceled int           // requests whose client went away
	log      []string      // every request, "METHOD path"
	shared   *gauge
}

func newFakeCPA(t *testing.T, key string, files ...map[string]any) *fakeCPA {
	t.Helper()
	f := &fakeCPA{key: key, files: files}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCPA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.log = append(f.log, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v8/management"))
	key := f.key
	if r.URL.Path == "/v8/management/credentials" && r.Method == http.MethodGet {
		f.requests++ // counted even when the key is rejected
	}
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid management key"}`)
		return
	}
	switch {
	case r.URL.Path == "/v8/management/config":
		_, _ = io.WriteString(w, `{}`)
	case r.URL.Path == "/v8/management/credentials" && r.Method == http.MethodGet:
		f.credentials(w, r)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (f *fakeCPA) credentials(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	block, status, body, files, g := f.block, f.status, f.body, f.files, f.shared
	f.mu.Unlock()
	if g != nil {
		g.enter()
		defer g.leave()
	}
	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
			f.mu.Lock()
			f.canceled++
			f.mu.Unlock()
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if body != "" {
		_, _ = io.WriteString(w, body)
		return
	}
	w.Header().Set("X-CPA-VERSION", "8.0.23")
	out := fmt.Sprintf(`{"observed_at":"2026-01-01T00:00:00Z","files":%s}`, mustJSON(files))
	_, _ = io.WriteString(w, out)
}

func (f *fakeCPA) set(fn func(f *fakeCPA)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeCPA) setFiles(files ...map[string]any) { f.set(func(f *fakeCPA) { f.files = files }) }
func (f *fakeCPA) setStatus(code int)               { f.set(func(f *fakeCPA) { f.status = code }) }
func (f *fakeCPA) setBlock(ch chan struct{})        { f.set(func(f *fakeCPA) { f.block = ch }) }

func (f *fakeCPA) count() (requests, canceled int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.canceled
}

func (f *fakeCPA) requestLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// credential builders
func cred(name, status string) map[string]any {
	return map[string]any{"id": name, "auth_index": "idx-" + name, "name": name, "status": status}
}

func coolingCred(name string) map[string]any {
	c := cred(name, "active")
	c["unavailable"] = true
	c["next_retry_after"] = "2026-01-01T01:00:00Z"
	return c
}

func disabledCred(name string) map[string]any {
	c := cred(name, "disabled")
	c["disabled"] = true
	return c
}

// ---- environment ----

type env struct {
	t       *testing.T
	set     *fakeSettings
	sec     *store.MemorySecrets
	clk     *fakeClock
	svc     *Service
	servers map[string]*fakeCPA

	evMu   sync.Mutex
	events []Event
}

// newEnv builds a Service with one preloaded instance per server, named
// "i0", "i1"... (IDs) / "N0", "N1"... (names), with the right keys stored.
// The service is not started: tests drive it with Refresh, Tick and the clock.
func newEnv(t *testing.T, servers ...*fakeCPA) *env {
	return newEnvWith(t, nil, servers...)
}

func newEnvWith(t *testing.T, mut func(*Options), servers ...*fakeCPA) *env {
	t.Helper()
	e := &env{
		t:       t,
		set:     &fakeSettings{s: store.Defaults()},
		sec:     &store.MemorySecrets{},
		clk:     &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
		servers: map[string]*fakeCPA{},
	}
	for i, f := range servers {
		id := fmt.Sprintf("i%d", i)
		base, err := cpa.NormalizeManagementURL(f.srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		e.set.s.Instances = append(e.set.s.Instances, store.InstanceProfile{
			ID: id, Name: fmt.Sprintf("N%d", i), BaseURL: base, Order: i, CreatedAt: e.clk.Now(),
		})
		_ = e.sec.Set(id, f.key)
		e.servers[id] = f
	}
	opts := Options{
		Settings: e.set,
		Secrets:  e.sec,
		Now:      e.clk.Now,
		OnEvent: func(ev Event) {
			e.evMu.Lock()
			e.events = append(e.events, ev)
			e.evMu.Unlock()
		},
		NewClient: func(p store.InstanceProfile, key string) (*cpa.Client, error) {
			return cpa.New(cpa.Options{
				InstanceID: p.ID, InstanceName: p.Name, BaseURL: p.BaseURL, ManagementKey: key,
				ReadTimeout: 5 * time.Second,
			})
		},
	}
	if mut != nil {
		mut(&opts)
	}
	svc, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	t.Cleanup(svc.Close)
	return e
}

// settle waits for every refresh goroutine to finish, events included. The
// service must not have been Started, and no request may be left blocked.
func (e *env) settle() {
	e.t.Helper()
	done := make(chan struct{})
	go func() { e.svc.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		e.t.Fatal("refreshes did not finish")
	}
}

func (e *env) refresh(id string) InstanceSnapshot {
	e.t.Helper()
	e.svc.Refresh(id)
	e.settle()
	return e.snap(id)
}

// rekey stores key for the instance and rebuilds its client, as a restart would.
func (e *env) rekey(id, key string) {
	e.t.Helper()
	if err := e.sec.Set(id, key); err != nil {
		e.t.Fatal(err)
	}
	e.svc.mu.Lock()
	e.svc.connect(e.svc.inst[id])
	e.svc.mu.Unlock()
}

func (e *env) snap(id string) InstanceSnapshot {
	e.t.Helper()
	s, ok := e.svc.Snapshot(id)
	if !ok {
		e.t.Fatalf("no snapshot for %s", id)
	}
	return s
}

func (e *env) kinds() []EventKind {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	out := make([]EventKind, len(e.events))
	for i, ev := range e.events {
		out[i] = ev.Kind
	}
	return out
}

func (e *env) lastEvent() Event {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	return e.events[len(e.events)-1]
}

func (e *env) eventCount() int {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	return len(e.events)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func wantReqs(t *testing.T, f *fakeCPA, want int) {
	t.Helper()
	if got, _ := f.count(); got != want {
		t.Errorf("server received %d credential requests, want %d", got, want)
	}
}

func names(s InstanceSnapshot) string {
	var n []string
	for _, c := range s.Credentials {
		n = append(n, c.Name)
	}
	return strings.Join(n, ",")
}

func apiKind(t *testing.T, err error) cpa.Kind {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return cpa.KindOf(err)
}

var errBoom = errors.New("disk full")

// spySecrets is an in-memory secret store whose contents tests can inspect.
type spySecrets struct {
	m   map[string]string
	err error
}

func (s *spySecrets) Get(id string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	v, ok := s.m[id]
	if !ok {
		return "", store.ErrSecretNotFound
	}
	return v, nil
}

func (s *spySecrets) Set(id, k string) error {
	if s.err != nil {
		return s.err
	}
	s.m[id] = k
	return nil
}

func (s *spySecrets) Delete(id string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.m, id)
	return nil
}

type faultSecrets struct {
	m          map[string]string
	getHook    func(string) (string, error)
	setHook    func(string, string) error
	deleteHook func(string) error
}

func (s *faultSecrets) Get(id string) (string, error) {
	if s.getHook != nil {
		return s.getHook(id)
	}
	key, ok := s.m[id]
	if !ok {
		return "", store.ErrSecretNotFound
	}
	return key, nil
}

func (s *faultSecrets) Set(id, key string) error {
	if s.setHook != nil {
		return s.setHook(id, key)
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[id] = key
	return nil
}

func (s *faultSecrets) Delete(id string) error {
	if s.deleteHook != nil {
		return s.deleteHook(id)
	}
	delete(s.m, id)
	return nil
}

// ---- AddInstance ----

func TestAddInstanceProbesThenSaves(t *testing.T) {
	f := newFakeCPA(t, "good", cred("a.json", "active"))
	e := newEnv(t)
	id, err := e.svc.AddInstance(bg, InstanceInput{Name: " Home ", Address: f.srv.URL, Key: "good"})
	if err != nil {
		t.Fatal(err)
	}
	e.settle()
	st, saves := e.set.snapshot()
	if saves != 1 || len(st.Instances) != 1 || st.Instances[0].ID != id || st.Instances[0].Name != "Home" ||
		!strings.HasSuffix(st.Instances[0].BaseURL, "/v8/management") || !st.Instances[0].CreatedAt.Equal(e.clk.Now()) {
		t.Errorf("saved settings = %+v (saves %d)", st, saves)
	}
	if k, err := e.sec.Get(id); err != nil || k != "good" {
		t.Errorf("stored key = %q, %v", k, err)
	}
	if s := e.snap(id); s.Conn != ConnOnline || s.Accounts.Total != 1 {
		t.Errorf("snapshot = %+v", s)
	}
	log := strings.Join(f.requestLog(), " ")
	if !strings.HasPrefix(log, "GET /config GET /credentials") {
		t.Errorf("probe requests = %s", log)
	}
}

func TestAddInstanceNameDefaultsToAddress(t *testing.T) {
	f := newFakeCPA(t, "k")
	e := newEnv(t)
	id, err := e.svc.AddInstance(bg, InstanceInput{Address: f.srv.URL, Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	e.settle()
	if got := e.snap(id).Profile.Name; got != strings.TrimPrefix(f.srv.URL, "http://") {
		t.Errorf("default name = %q", got)
	}
}

func TestAddInstanceSavesNothingOnFailure(t *testing.T) {
	good := newFakeCPA(t, "good", cred("a", "active"))
	down := newFakeCPA(t, "good")
	downURL := down.srv.URL
	down.srv.Close()

	tests := []struct {
		name  string
		input InstanceInput
		setup func(e *env)
		check func(t *testing.T, err error)
	}{
		{name: "wrong key", input: InstanceInput{Address: good.srv.URL, Key: "bad"},
			check: func(t *testing.T, err error) {
				if !cpa.IsAuthError(err) {
					t.Errorf("err = %v, want auth error", err)
				}
			}},
		{name: "server unreachable", input: InstanceInput{Address: downURL, Key: "good"},
			check: func(t *testing.T, err error) {
				if k := apiKind(t, err); k != cpa.KindNetwork {
					t.Errorf("kind = %v", k)
				}
			}},
		{name: "empty key", input: InstanceInput{Address: good.srv.URL, Key: "  "}},
		{name: "invalid address", input: InstanceInput{Address: "ftp://x", Key: "good"}},
		{name: "v0 address", input: InstanceInput{Address: good.srv.URL + "/v0/management", Key: "good"}},
		{name: "settings save fails", input: InstanceInput{Address: good.srv.URL, Key: "good"},
			setup: func(e *env) { e.set.setErr(errBoom) },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errBoom) {
					t.Errorf("err = %v, want the save error", err)
				}
			}},
		{name: "secret store fails", input: InstanceInput{Address: good.srv.URL, Key: "good"},
			setup: func(e *env) { e.svc.opts.Secrets.(*spySecrets).err = errBoom },
			check: func(t *testing.T, err error) {
				if !errors.Is(err, errBoom) {
					t.Errorf("err = %v", err)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spySecrets{m: map[string]string{}}
			e := newEnvWith(t, func(o *Options) { o.Secrets = spy })
			if tc.setup != nil {
				tc.setup(e)
			}
			id, err := e.svc.AddInstance(bg, tc.input)
			if err == nil {
				t.Fatalf("AddInstance succeeded with id %s", id)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
			if len(spy.m) != 0 {
				t.Errorf("a key was left in the secret store: %v", spy.m)
			}
			st, saves := e.set.snapshot()
			if saves != 0 || len(st.Instances) != 0 {
				t.Errorf("settings were saved: %+v (saves %d)", st, saves)
			}
			if len(e.svc.Snapshots()) != 0 || len(e.svc.Settings().Instances) != 0 {
				t.Error("service state changed")
			}
		})
	}
}

func TestAddInstanceReportsOriginalAndCleanupErrors(t *testing.T) {
	f := newFakeCPA(t, "good")
	mp := cpamptest.New(t, "admin")
	primary := errors.New("manager key write failed")
	cleanup := errors.New("credential cleanup failed")
	secrets := &faultSecrets{m: map[string]string{}}
	var setCalls, deleteCalls int
	var mainID string
	secrets.setHook = func(id, key string) error {
		setCalls++
		secrets.m[id] = key
		if setCalls == 1 {
			mainID = id
			return nil
		}
		return primary
	}
	secrets.deleteHook = func(id string) error {
		deleteCalls++
		delete(secrets.m, id)
		if deleteCalls == 2 {
			return cleanup
		}
		return nil
	}
	e := newEnvWith(t, func(o *Options) { o.Secrets = secrets })
	_, err := e.svc.AddInstance(bg, InstanceInput{
		Address: f.srv.URL, Key: "good", Flavor: store.FlavorManagerPlus,
		ManagerURL: mp.URL, ManagerKey: "admin",
	})
	if !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), mainID) || strings.Contains(err.Error(), "good") || strings.Contains(err.Error(), "admin") {
		t.Errorf("error must identify the credential without exposing keys: %v", err)
	}
	if len(secrets.m) != 0 {
		t.Errorf("credentials left after cleanup: %v", secrets.m)
	}
}

// ---- isolation and failure handling ----

func TestSameNamedCredentialsStayIsolated(t *testing.T) {
	a := newFakeCPA(t, "ka", cred("shared.json", "error"), cred("only-a.json", "active"))
	b := newFakeCPA(t, "kb", cred("shared.json", "active"))
	e := newEnv(t, a, b)
	e.svc.RefreshAll()
	e.settle()

	sa, sb := e.snap("i0"), e.snap("i1")
	if names(sa) != "shared.json,only-a.json" || names(sb) != "shared.json" {
		t.Errorf("credentials a=%s b=%s", names(sa), names(sb))
	}
	if sa.Accounts.Error != 1 || sa.Accounts.Available != 1 || sb.Accounts.Error != 0 || sb.Accounts.Available != 1 {
		t.Errorf("accounts a=%+v b=%+v", sa.Accounts, sb.Accounts)
	}
	// Changing one server leaves the other's data alone.
	a.setFiles(cred("shared.json", "active"))
	e.refresh("i0")
	if e.snap("i0").Accounts.Error != 0 || e.snap("i1").Accounts.Available != 1 {
		t.Error("refreshing one instance changed another")
	}
	wantReqs(t, b, 1)
}

func TestOneInstanceFailingDoesNotAffectAnother(t *testing.T) {
	a := newFakeCPA(t, "ka", cred("a.json", "active"))
	b := newFakeCPA(t, "kb", cred("b.json", "active"))
	e := newEnv(t, a, b)
	e.svc.RefreshAll()
	e.settle()

	b.setStatus(500)
	e.svc.RefreshAll()
	e.settle()
	sa, sb := e.snap("i0"), e.snap("i1")
	if sa.Conn != ConnOnline || sa.Stale || sa.Err != nil || sa.Accounts.Total != 1 {
		t.Errorf("healthy instance disturbed: %+v", sa)
	}
	if sb.Conn != ConnError || !sb.Stale {
		t.Errorf("failing instance: %+v", sb)
	}
	// Errors name the failing instance.
	var ae *cpa.APIError
	if !errors.As(sb.Err, &ae) || ae.InstanceID != "i1" || ae.InstanceName != "N1" {
		t.Errorf("error does not name its instance: %v", sb.Err)
	}
}

func TestFailureAfterSuccessKeepsDataAndMarksStale(t *testing.T) {
	const slow = "slow"
	tests := []struct {
		name string
		fail func(f *fakeCPA)
		want ConnState
		kind cpa.Kind
	}{
		{"500", func(f *fakeCPA) { f.setStatus(500) }, ConnError, cpa.KindServer},
		{"429", func(f *fakeCPA) { f.setStatus(429) }, ConnError, cpa.KindRateLimited},
		{"401", func(f *fakeCPA) { f.setStatus(401) }, ConnAuth, cpa.KindUnauthorized},
		{"403", func(f *fakeCPA) { f.setStatus(403) }, ConnAuth, cpa.KindForbidden},
		{"404 empty body", func(f *fakeCPA) { f.setStatus(404) }, ConnUnavailable, cpa.KindManagementUnavailable},
		{"404 plain text", func(f *fakeCPA) { f.set(func(f *fakeCPA) { f.body = "" }); f.setStatus(404) }, ConnUnavailable, cpa.KindManagementUnavailable},
		{"invalid json", func(f *fakeCPA) { f.set(func(f *fakeCPA) { f.body = "{nope" }) }, ConnError, cpa.KindDecode},
		{"unreachable", func(f *fakeCPA) { f.srv.CloseClientConnections(); f.srv.Close() }, ConnOffline, cpa.KindNetwork},
		{slow, func(f *fakeCPA) { f.setBlock(make(chan struct{})) }, ConnOffline, cpa.KindTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCPA(t, "k", cred("a.json", "active"), cred("b.json", "error"), disabledCred("c.json"))
			e := newEnvWith(t, func(o *Options) {
				if tc.name == slow {
					o.NewClient = func(p store.InstanceProfile, key string) (*cpa.Client, error) {
						return cpa.New(cpa.Options{InstanceID: p.ID, InstanceName: p.Name, BaseURL: p.BaseURL,
							ManagementKey: key, ReadTimeout: 150 * time.Millisecond})
					}
				}
			}, f)
			ok := e.refresh("i0")
			if ok.Conn != ConnOnline || ok.Stale || !ok.HasData || ok.Accounts.Total != 3 {
				t.Fatalf("first refresh: %+v", ok)
			}
			if ok.Server.Version != "8.0.23" {
				t.Errorf("Server = %+v", ok.Server)
			}

			tc.fail(f)
			e.clk.Advance(time.Minute)
			got := e.refresh("i0")
			if got.Conn != tc.want || cpa.KindOf(got.Err) != tc.kind {
				t.Errorf("Conn=%v kind=%v err=%v, want %v/%v", got.Conn, cpa.KindOf(got.Err), got.Err, tc.want, tc.kind)
			}
			if !got.Stale || !got.HasData {
				t.Errorf("Stale=%v HasData=%v, want true/true", got.Stale, got.HasData)
			}
			if names(got) != "a.json,b.json,c.json" || got.Accounts != ok.Accounts {
				t.Errorf("last data lost: %s %+v", names(got), got.Accounts)
			}
			if !got.LastSuccess.Equal(ok.LastSuccess) || !got.LastAttempt.After(ok.LastAttempt) {
				t.Errorf("LastSuccess=%v LastAttempt=%v", got.LastSuccess, got.LastAttempt)
			}
			if got.Refreshing {
				t.Error("still marked Refreshing")
			}
			if ov := e.svc.Overview(); !ov.Stale || ov.Problem != 1 || ov.Accounts.Total != 3 {
				t.Errorf("Overview = %+v", ov)
			}
		})
	}
}

func TestRecoveryClearsStale(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"))
	e := newEnv(t, f)
	e.refresh("i0")
	f.setStatus(503)
	if s := e.refresh("i0"); !s.Stale {
		t.Fatal("not stale after failure")
	}
	f.setStatus(0)
	f.setFiles(cred("a.json", "active"), cred("n.json", "active"))
	s := e.refresh("i0")
	if s.Conn != ConnOnline || s.Stale || s.Err != nil || s.Accounts.Total != 2 {
		t.Errorf("after recovery: %+v", s)
	}
}

func TestFailureWithoutPriorDataIsNotStale(t *testing.T) {
	f := newFakeCPA(t, "k")
	f.setStatus(500)
	e := newEnv(t, f)
	s := e.refresh("i0")
	if s.Conn != ConnError || s.HasData || s.Stale || len(s.Credentials) != 0 {
		t.Errorf("snapshot = %+v", s)
	}
	if ov := e.svc.Overview(); ov.Stale || ov.Accounts.Total != 0 || ov.Problem != 1 {
		t.Errorf("Overview = %+v", ov)
	}
}

// ---- authentication ----

func TestAuthErrorStopsAutomaticRefreshButManualStillTries(t *testing.T) {
	f := newFakeCPA(t, "good", cred("a.json", "active"))
	e := newEnv(t, f)
	e.rekey("i0", "bad")

	e.svc.RefreshAll()
	e.settle()
	if s := e.snap("i0"); s.Conn != ConnAuth || !cpa.IsAuthError(s.Err) {
		t.Fatalf("snapshot = %+v", s)
	}
	wantReqs(t, f, 1)

	// Automatic refreshes stop, however much time passes.
	for i := 0; i < 3; i++ {
		e.clk.Advance(time.Hour)
		e.svc.Tick()
		e.svc.RefreshAll()
	}
	e.settle()
	wantReqs(t, f, 1)

	// A manual refresh tries exactly once more.
	e.svc.Refresh("i0")
	e.settle()
	wantReqs(t, f, 2)
	if s := e.snap("i0"); s.Conn != ConnAuth {
		t.Errorf("Conn = %v", s.Conn)
	}
	e.clk.Advance(time.Hour)
	e.svc.Tick()
	e.settle()
	wantReqs(t, f, 2)
	if n := e.eventCount(); n != 0 {
		t.Errorf("a never-online instance sent %d events", n)
	}
}

func TestUpdateInstanceWithNewKeyClearsAuthBlock(t *testing.T) {
	f := newFakeCPA(t, "good", cred("a.json", "active"))
	e := newEnv(t, f)
	e.rekey("i0", "bad")
	e.refresh("i0")
	if e.snap("i0").Conn != ConnAuth {
		t.Fatal("not blocked")
	}

	err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL, Key: "good"})
	if err != nil {
		t.Fatal(err)
	}
	e.settle()
	s := e.snap("i0")
	if s.Conn != ConnOnline || s.Err != nil || s.Accounts.Total != 1 {
		t.Fatalf("after new key: %+v", s)
	}
	if k, _ := e.sec.Get("i0"); k != "good" {
		t.Errorf("stored key = %q", k)
	}
	// Automatic refreshing works again.
	before, _ := f.count()
	e.clk.Advance(time.Hour)
	e.svc.Tick()
	e.settle()
	if after, _ := f.count(); after != before+1 {
		t.Errorf("Tick after unblocking sent %d requests", after-before)
	}
}

func TestMissingKeyIsAnAuthStateWithoutRequests(t *testing.T) {
	f := newFakeCPA(t, "k")
	e := newEnv(t, f)
	if err := e.sec.Delete("i0"); err != nil {
		t.Fatal(err)
	}
	e.svc.mu.Lock()
	e.svc.connect(e.svc.inst["i0"])
	e.svc.mu.Unlock()

	s := e.refresh("i0")
	if s.Conn != ConnAuth || !errors.Is(s.Err, store.ErrSecretNotFound) {
		t.Errorf("snapshot = %+v", s)
	}
	if _, err := e.svc.Client("i0"); !errors.Is(err, store.ErrSecretNotFound) {
		t.Errorf("Client() error = %v", err)
	}
	wantReqs(t, f, 0)
	if _, err := e.svc.Client("nope"); !errors.Is(err, ErrNoInstance) {
		t.Errorf("Client(unknown) = %v", err)
	}
	// Supplying a key repairs it.
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if s := e.snap("i0"); s.Conn != ConnOnline {
		t.Errorf("after supplying a key: %+v", s)
	}
}

// ---- connection changes and late responses ----

// lateTransport answers only when gate closes, whatever the request's
// context says: a response that arrives after its connection was replaced.
type lateTransport struct {
	gate    chan struct{}
	started chan struct{}
	body    string
}

func (l *lateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	l.started <- struct{}{}
	<-l.gate
	return &http.Response{
		StatusCode: 200, Status: "200 OK", Header: http.Header{"X-Cpa-Version": {"late"}},
		Body: io.NopCloser(strings.NewReader(l.body)), Request: req,
	}, nil
}

const lateBase = "http://late.invalid/v8/management"

// newLateEnv has instance i0 behind a lateTransport and f as another server.
func newLateEnv(t *testing.T, lt *lateTransport, f *fakeCPA) *env {
	return newEnvWith(t, func(o *Options) {
		o.Settings.(*fakeSettings).s.Instances[0].BaseURL = lateBase
		o.NewClient = func(p store.InstanceProfile, key string) (*cpa.Client, error) {
			opts := cpa.Options{InstanceID: p.ID, InstanceName: p.Name, BaseURL: p.BaseURL, ManagementKey: key}
			if p.BaseURL == lateBase {
				opts.Transport = lt
			}
			return cpa.New(opts)
		}
	}, f)
}

func newLate() *lateTransport {
	return &lateTransport{gate: make(chan struct{}), started: make(chan struct{}, 8),
		body: `{"files":[{"name":"stale.json","auth_index":"x","status":"error"}]}`}
}

func TestLateResponseAfterAddressChangeIsDropped(t *testing.T) {
	lt := newLate()
	f := newFakeCPA(t, "k", cred("fresh.json", "active"))
	e := newLateEnv(t, lt, f)

	e.svc.Refresh("i0")
	<-lt.started
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new connection's data", func() bool {
		s := e.snap("i0")
		return s.Conn == ConnOnline && !s.Refreshing && names(s) == "fresh.json"
	})

	close(lt.gate) // the old connection's response arrives now
	e.settle()
	s := e.snap("i0")
	if s.Conn != ConnOnline || names(s) != "fresh.json" || s.Accounts.Error != 0 || s.Server.Version == "late" {
		t.Errorf("late response was applied: %+v", s)
	}
	if n := e.eventCount(); n != 0 {
		t.Errorf("late response produced events: %v", e.kinds())
	}
}

func TestAddressChangeResetsDataOfTheOldServer(t *testing.T) {
	a := newFakeCPA(t, "k", cred("a.json", "error"))
	b := newFakeCPA(t, "k", cred("b.json", "active"))
	b.setStatus(500)
	e := newEnv(t, a)
	e.refresh("i0")
	if e.snap("i0").Accounts.Error != 1 {
		t.Fatal("setup")
	}
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: b.srv.URL}); err != nil {
		t.Fatal(err)
	}
	e.settle()
	s := e.snap("i0")
	// The other server's data must not be shown as this one's, not even stale.
	if s.HasData || s.Stale || len(s.Credentials) != 0 || s.Conn != ConnError {
		t.Errorf("snapshot after moving to a failing server: %+v", s)
	}
	if e.eventCount() != 1 { // the abnormal credential of the old server, nothing more
		t.Errorf("events = %v", e.kinds())
	}
}

func TestRenameKeepsConnectionAndData(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"))
	e := newEnv(t, f)
	e.refresh("i0")
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "Renamed", Address: f.srv.URL}); err != nil {
		t.Fatal(err)
	}
	e.settle()
	s := e.snap("i0")
	if s.Profile.Name != "Renamed" || !s.HasData || s.Conn != ConnOnline {
		t.Errorf("snapshot = %+v", s)
	}
	wantReqs(t, f, 1) // a rename is not a reconnect
	st, _ := e.set.snapshot()
	if st.Instances[0].Name != "Renamed" || st.Instances[0].Order != 0 {
		t.Errorf("saved = %+v", st.Instances[0])
	}
	// Errors from now on carry the new name.
	f.setStatus(500)
	if err := e.refresh("i0").Err; !strings.Contains(err.Error(), "Renamed") {
		t.Errorf("error = %v", err)
	}
}

func TestUpdateInstanceFailures(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"))
	e := newEnv(t, f)
	if err := e.svc.UpdateInstance("nope", InstanceInput{Address: f.srv.URL}); !errors.Is(err, ErrNoInstance) {
		t.Errorf("unknown instance: %v", err)
	}
	if err := e.svc.UpdateInstance("i0", InstanceInput{Address: "ftp://x"}); err == nil {
		t.Error("invalid address accepted")
	}
	e.set.setErr(errBoom)
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "X", Address: f.srv.URL}); !errors.Is(err, errBoom) {
		t.Errorf("save failure: %v", err)
	}
	if got := e.svc.Settings().Instances[0].Name; got != "N0" {
		t.Errorf("settings changed despite the failed save: %q", got)
	}
	if got := e.snap("i0").Profile.Name; got != "N0" {
		t.Errorf("snapshot changed despite the failed save: %q", got)
	}
}

func TestUpdateInstanceRestoresCredentialState(t *testing.T) {
	f := newFakeCPA(t, "old")
	t.Run("failed write and failed rollback are both reported", func(t *testing.T) {
		secrets := &faultSecrets{m: map[string]string{"i0": "old"}}
		e := newEnvWith(t, func(o *Options) { o.Secrets = secrets }, f)
		primary := errors.New("key write failed")
		rollback := errors.New("key rollback failed")
		calls := 0
		secrets.setHook = func(id, key string) error {
			calls++
			secrets.m[id] = key
			if calls == 1 {
				return primary
			}
			return rollback
		}
		err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL, Key: "new"})
		if !errors.Is(err, primary) || !errors.Is(err, rollback) {
			t.Fatalf("err = %v", err)
		}
		if got := secrets.m["i0"]; got != "old" {
			t.Errorf("credential after rollback = %q", got)
		}
		if got := e.svc.Settings().Instances[0].Name; got != "N0" {
			t.Errorf("settings changed to %q", got)
		}
	})

	t.Run("previously absent credential is deleted after save failure", func(t *testing.T) {
		secrets := &faultSecrets{m: map[string]string{}}
		e := newEnvWith(t, func(o *Options) { o.Secrets = secrets }, f)
		e.set.setErr(errBoom)
		if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL, Key: "new"}); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := secrets.m["i0"]; ok {
			t.Error("new credential was not deleted")
		}
	})

	t.Run("credential read failure stops before mutation", func(t *testing.T) {
		secrets := &faultSecrets{m: map[string]string{"i0": "old"}}
		e := newEnvWith(t, func(o *Options) { o.Secrets = secrets }, f)
		readErr := errors.New("credential read failed")
		secrets.getHook = func(string) (string, error) { return "", readErr }
		var writes int
		secrets.setHook = func(string, string) error { writes++; return nil }
		err := e.svc.UpdateInstance("i0", InstanceInput{Name: "N0", Address: f.srv.URL, Key: "new"})
		if !errors.Is(err, readErr) || writes != 0 {
			t.Fatalf("err = %v, writes = %d", err, writes)
		}
	})
}

// A rename rebuilds the client; if the key cannot be read then, a request
// that was already in flight must still finish without crashing.
func TestRenameWhileRefreshingWithUnreadableKey(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"))
	gate := make(chan struct{})
	f.setBlock(gate)
	e := newEnv(t, f)
	e.svc.Refresh("i0")
	waitFor(t, "request in flight", func() bool { r, _ := f.count(); return r == 1 })

	e.sec.Err = errBoom
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "Renamed", Address: f.srv.URL}); err != nil {
		t.Fatal(err)
	}
	e.sec.Err = nil
	close(gate)
	e.settle()
	if s := e.snap("i0"); s.Profile.Name != "Renamed" || s.Refreshing {
		t.Errorf("snapshot = %+v", s)
	}
}

// ---- RemoveInstance ----

func TestRemoveInstanceCancelsInFlightAndOnlyDeletesLocalState(t *testing.T) {
	a := newFakeCPA(t, "ka", cred("a.json", "active"))
	b := newFakeCPA(t, "kb", cred("b.json", "active"))
	gate := make(chan struct{})
	a.setBlock(gate)
	e := newEnv(t, a, b)
	_ = e.svc.SetDefaultInstance("i0")
	e.svc.SetActive("i0") // refreshes both... a blocks
	waitFor(t, "request in flight", func() bool { r, _ := a.count(); return r == 1 })

	if err := e.svc.RemoveInstance("i0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the request to be canceled", func() bool { _, c := a.count(); return c == 1 })
	e.settle()

	if _, ok := e.svc.Snapshot("i0"); ok {
		t.Error("snapshot still there")
	}
	if len(e.svc.Snapshots()) != 1 || e.svc.Snapshots()[0].Profile.ID != "i1" {
		t.Errorf("snapshots = %+v", e.svc.Snapshots())
	}
	st, _ := e.set.snapshot()
	if len(st.Instances) != 1 || st.Instances[0].ID != "i1" || st.DefaultInstanceID != "" {
		t.Errorf("saved settings = %+v", st)
	}
	if _, err := e.sec.Get("i0"); !errors.Is(err, store.ErrSecretNotFound) {
		t.Errorf("key of the removed instance: %v", err)
	}
	if k, err := e.sec.Get("i1"); err != nil || k != "kb" {
		t.Errorf("key of the other instance: %q %v", k, err)
	}
	for _, line := range a.requestLog() {
		if line != "GET /credentials" {
			t.Errorf("removal sent %q to the server", line)
		}
	}
	if e.svc.active != "" {
		t.Errorf("active = %q", e.svc.active)
	}
	if err := e.svc.RemoveInstance("i0"); !errors.Is(err, ErrNoInstance) {
		t.Errorf("second removal: %v", err)
	}
	if _, err := e.svc.Client("i0"); !errors.Is(err, ErrNoInstance) {
		t.Errorf("Client after removal: %v", err)
	}
}

func TestResponseAfterRemoveInstanceIsDropped(t *testing.T) {
	lt := newLate()
	f := newFakeCPA(t, "k")
	e := newLateEnv(t, lt, f)
	var notified int
	e.svc.Subscribe(func() { notified++ })

	e.svc.Refresh("i0")
	<-lt.started
	if err := e.svc.RemoveInstance("i0"); err != nil {
		t.Fatal(err)
	}
	notifiedBefore := notified
	close(lt.gate)
	e.settle()
	if _, ok := e.svc.Snapshot("i0"); ok {
		t.Error("the removed instance reappeared")
	}
	if notified != notifiedBefore {
		t.Error("the dropped response notified listeners")
	}
	if e.eventCount() != 0 {
		t.Errorf("events = %v", e.kinds())
	}
}

func TestRemoveInstanceFailures(t *testing.T) {
	t.Run("save fails: nothing changes", func(t *testing.T) {
		f := newFakeCPA(t, "k", cred("a.json", "active"))
		e := newEnv(t, f)
		e.refresh("i0")
		e.set.setErr(errBoom)
		if err := e.svc.RemoveInstance("i0"); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := e.svc.Snapshot("i0"); !ok {
			t.Error("instance gone")
		}
		if k, err := e.sec.Get("i0"); err != nil || k != "k" {
			t.Errorf("key was deleted: %q %v", k, err)
		}
	})
	t.Run("key deletion fails: instance remains for retry", func(t *testing.T) {
		f := newFakeCPA(t, "k")
		e := newEnv(t, f)
		e.sec.Err = errBoom
		err := e.svc.RemoveInstance("i0")
		if !errors.Is(err, errBoom) {
			t.Errorf("err = %v", err)
		}
		if _, ok := e.svc.Snapshot("i0"); !ok {
			t.Error("instance was removed")
		}
		e.sec.Err = nil
		if err := e.svc.RemoveInstance("i0"); err != nil {
			t.Fatalf("retry: %v", err)
		}
	})
	t.Run("delete may mutate before failing and is restored", func(t *testing.T) {
		f := newFakeCPA(t, "k")
		secrets := &faultSecrets{m: map[string]string{"i0": "k"}}
		e := newEnvWith(t, func(o *Options) { o.Secrets = secrets }, f)
		deleteErr := errors.New("delete failed")
		calls := 0
		secrets.deleteHook = func(id string) error {
			calls++
			delete(secrets.m, id)
			if calls == 1 {
				return deleteErr
			}
			return nil
		}
		if err := e.svc.RemoveInstance("i0"); !errors.Is(err, deleteErr) {
			t.Fatalf("err = %v", err)
		}
		if _, ok := e.svc.Snapshot("i0"); !ok || secrets.m["i0"] != "k" {
			t.Fatalf("failed removal changed instance or credential: %v", secrets.m)
		}
		if err := e.svc.RemoveInstance("i0"); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if _, ok := e.svc.Snapshot("i0"); ok {
			t.Error("instance remains after successful retry")
		}
	})
}

// ---- scheduling ----

func TestTickRespectsIntervals(t *testing.T) {
	a := newFakeCPA(t, "k", cred("a", "active"))
	b := newFakeCPA(t, "k", cred("b", "active"))
	e := newEnv(t, a, b)
	st := e.svc.Settings()
	if st.ActiveRefresh != 10 || st.BackgroundRefresh != 30 {
		t.Fatalf("default intervals %d/%d", st.ActiveRefresh, st.BackgroundRefresh)
	}
	step := func(d time.Duration, wantA, wantB int) {
		t.Helper()
		e.clk.Advance(d)
		e.svc.Tick()
		e.settle()
		ra, _ := a.count()
		rb, _ := b.count()
		if ra != wantA || rb != wantB {
			t.Fatalf("at %v: a=%d b=%d requests, want %d/%d", e.clk.Now().Sub(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)), ra, rb, wantA, wantB)
		}
	}
	step(0, 1, 1) // everything is due at the start
	step(29*time.Second, 1, 1)
	step(time.Second, 2, 2) // 30s: both on the background interval

	e.svc.SetActive("i0") // switching refreshes the newly active instance at once
	e.settle()
	if ra, _ := a.count(); ra != 3 {
		t.Fatalf("SetActive did not refresh: %d", ra)
	}
	step(9*time.Second, 3, 2)
	step(time.Second, 4, 2) // +10s: a (active interval)
	step(10*time.Second, 5, 2)
	step(10*time.Second, 6, 3) // +30s since b's last: b (background)

	e.svc.SetActive("i1")
	e.settle()
	if rb, _ := b.count(); rb != 4 {
		t.Fatalf("switching to b did not refresh it: %d", rb)
	}
	e.svc.SetActive("i1") // unchanged: nothing
	e.settle()
	if rb, _ := b.count(); rb != 4 {
		t.Fatalf("SetActive to the same instance refreshed: %d", rb)
	}
}

func TestTickIgnoresPausedInstances(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	e := newEnvWith(t, func(o *Options) { o.Settings.(*fakeSettings).s.Instances[0].Paused = true }, f)
	if s := e.snap("i0"); s.Conn != ConnPaused {
		t.Fatalf("Conn = %v", s.Conn)
	}
	e.svc.Tick()
	e.svc.RefreshAll()
	e.settle()
	wantReqs(t, f, 0)
}

func TestSameInstanceNeverRefreshesConcurrently(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	g := &gauge{}
	f.set(func(f *fakeCPA) { f.shared = g })
	gate := make(chan struct{})
	f.setBlock(gate)
	e := newEnv(t, f)

	e.svc.Refresh("i0")
	waitFor(t, "request in flight", func() bool { c, _, _ := g.get(); return c == 1 })
	if !e.snap("i0").Refreshing {
		t.Error("not marked Refreshing")
	}
	for i := 0; i < 3; i++ {
		e.clk.Advance(time.Hour)
		e.svc.Refresh("i0")
		e.svc.RefreshAll()
		e.svc.Tick()
		e.svc.SetActive("i0")
	}
	close(gate)
	e.settle()
	if _, peak, total := g.get(); peak != 1 || total != 1 {
		t.Errorf("peak=%d total=%d requests, want 1/1", peak, total)
	}
}

func TestAtMostMaxConcurrentRefreshInFlight(t *testing.T) {
	g := &gauge{}
	gate := make(chan struct{})
	var servers []*fakeCPA
	for i := 0; i < 6; i++ {
		f := newFakeCPA(t, "k", cred(fmt.Sprintf("c%d", i), "active"))
		f.set(func(f *fakeCPA) { f.shared, f.block = g, gate })
		servers = append(servers, f)
	}
	e := newEnv(t, servers...)
	e.svc.RefreshAll()
	waitFor(t, "four requests in flight", func() bool { c, _, _ := g.get(); return c == MaxConcurrentRefresh })
	if _, _, total := g.get(); total != MaxConcurrentRefresh {
		t.Errorf("%d requests reached servers while %d slots exist", total, MaxConcurrentRefresh)
	}
	refreshing := 0
	for _, s := range e.svc.Snapshots() {
		if s.Refreshing {
			refreshing++
		}
	}
	if refreshing != 6 {
		t.Errorf("%d instances marked Refreshing, want all 6", refreshing)
	}
	close(gate)
	e.settle()
	if cur, peak, total := g.get(); cur != 0 || peak != MaxConcurrentRefresh || total != 6 {
		t.Errorf("cur=%d peak=%d total=%d", cur, peak, total)
	}
	for _, s := range e.svc.Snapshots() {
		if s.Conn != ConnOnline {
			t.Errorf("%s: %v", s.Profile.ID, s.Conn)
		}
	}
}

func TestSuspendCancelsInFlightBlocksTickAndResumeRefreshesOnce(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	e := newEnv(t, f)
	first := e.refresh("i0")
	wantReqs(t, f, 1)

	gate := make(chan struct{})
	f.setBlock(gate)
	e.clk.Advance(time.Minute)
	e.svc.Tick()
	waitFor(t, "request in flight", func() bool { r, _ := f.count(); return r == 2 })

	e.svc.Suspend()
	waitFor(t, "cancellation", func() bool { _, c := f.count(); return c == 1 })
	e.settle()
	s := e.snap("i0")
	if s.Refreshing || s.Conn != ConnOnline || s.Stale || s.Err != nil || names(s) != "a" || !s.LastSuccess.Equal(first.LastSuccess) {
		t.Errorf("a canceled refresh must leave the previous state: %+v", s)
	}
	if e.eventCount() != 0 {
		t.Errorf("cancellation produced events: %v", e.kinds())
	}

	e.clk.Advance(time.Hour)
	e.svc.Tick()
	e.svc.Refresh("i0")
	e.svc.RefreshAll()
	e.settle()
	wantReqs(t, f, 2)

	f.setBlock(nil)
	e.svc.Resume()
	e.settle()
	wantReqs(t, f, 3)
	if s := e.snap("i0"); !s.LastSuccess.After(first.LastSuccess) {
		t.Errorf("Resume did not refresh: %+v", s)
	}
}

func TestSetPaused(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	e := newEnv(t, f)
	e.refresh("i0")

	if err := e.svc.SetPaused("i0", true); err != nil {
		t.Fatal(err)
	}
	if s := e.snap("i0"); s.Conn != ConnPaused || !s.Profile.Paused || !s.HasData {
		t.Errorf("paused snapshot = %+v", s)
	}
	if st, _ := e.set.snapshot(); !st.Instances[0].Paused {
		t.Error("pause not persisted")
	}
	e.clk.Advance(time.Hour)
	e.svc.Tick()
	e.svc.RefreshAll()
	e.settle()
	wantReqs(t, f, 1)
	if ov := e.svc.Overview(); ov.Instances != 0 || ov.Accounts.Total != 0 || ov.Problem != 0 {
		t.Errorf("paused instance counted: %+v", ov)
	}

	if err := e.svc.SetPaused("i0", false); err != nil {
		t.Fatal(err)
	}
	e.settle()
	wantReqs(t, f, 2)
	if s := e.snap("i0"); s.Conn != ConnOnline || s.Profile.Paused {
		t.Errorf("resumed snapshot = %+v", s)
	}
	if err := e.svc.SetPaused("nope", true); !errors.Is(err, ErrNoInstance) {
		t.Errorf("unknown instance: %v", err)
	}
	e.set.setErr(errBoom)
	if err := e.svc.SetPaused("i0", true); !errors.Is(err, errBoom) || e.snap("i0").Conn != ConnOnline {
		t.Errorf("failed save must not pause: %v / %v", err, e.snap("i0").Conn)
	}
}

func TestPausingCancelsInFlightAndDropsItsResponse(t *testing.T) {
	f := newFakeCPA(t, "k", cred("late", "error"))
	gate := make(chan struct{})
	f.setBlock(gate)
	e := newEnv(t, f)
	e.svc.Refresh("i0")
	waitFor(t, "request in flight", func() bool { r, _ := f.count(); return r == 1 })
	if err := e.svc.SetPaused("i0", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancellation", func() bool { _, c := f.count(); return c == 1 })
	e.settle()
	s := e.snap("i0")
	if s.Conn != ConnPaused || s.HasData || s.Refreshing || e.eventCount() != 0 {
		t.Errorf("snapshot = %+v, events %v", s, e.kinds())
	}
}

func TestStartRefreshesAndCloseStops(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	e := newEnv(t, f)
	e.svc.Start()
	e.svc.Start() // idempotent
	waitFor(t, "initial refresh", func() bool { return e.snap("i0").Conn == ConnOnline })
	e.svc.Close()
	r, _ := f.count()
	e.clk.Advance(time.Hour)
	e.svc.Tick() // after Close the context is canceled: no request may leave
	e.svc.wg.Wait()
	if r2, _ := f.count(); r2 != r {
		t.Errorf("requests kept coming after Close: %d -> %d", r, r2)
	}
}

// ---- notifications ----

func TestInstanceDownAndRecoveredAreAnnouncedOncePerOutage(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "active"))
	f.setStatus(500)
	e := newEnv(t, f)

	// Starting out of reach is not "went down": nothing was known before.
	e.refresh("i0")
	e.refresh("i0")
	if e.eventCount() != 0 {
		t.Fatalf("initial failure announced: %v", e.kinds())
	}

	f.setStatus(0)
	e.refresh("i0")
	if e.eventCount() != 0 {
		t.Fatalf("first success announced a recovery: %v", e.kinds())
	}

	f.setStatus(503)
	e.refresh("i0")
	if got := e.kinds(); len(got) != 1 || got[0] != EventInstanceDown {
		t.Fatalf("events = %v, want one InstanceDown", got)
	}
	if ev := e.lastEvent(); ev.InstanceID != "i0" || ev.InstanceName != "N0" || ev.Conn != ConnError || cpa.KindOf(ev.Err) != cpa.KindServer {
		t.Errorf("event = %+v", ev)
	}
	// Still down, even with a different failure: no repeat.
	e.refresh("i0")
	f.setStatus(401)
	e.refresh("i0")
	f.setStatus(404)
	e.refresh("i0")
	if e.eventCount() != 1 {
		t.Fatalf("outage announced again: %v", e.kinds())
	}

	f.setStatus(0)
	e.refresh("i0")
	e.refresh("i0")
	if got := e.kinds(); len(got) != 2 || got[1] != EventRecovered {
		t.Fatalf("events = %v, want InstanceDown, Recovered", got)
	}

	// A second outage is a new announcement.
	f.setStatus(500)
	e.refresh("i0")
	f.setStatus(0)
	e.refresh("i0")
	want := []EventKind{EventInstanceDown, EventRecovered, EventInstanceDown, EventRecovered}
	if got := e.kinds(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestAccountsAbnormalAnnouncedOnlyWhenNewlyAbnormal(t *testing.T) {
	f := newFakeCPA(t, "k", cred("ok.json", "active"), cred("b.json", "error"), coolingCred("c.json"), disabledCred("off.json"))
	e := newEnv(t, f)
	abnormal := func() (n int, last []string) {
		e.evMu.Lock()
		defer e.evMu.Unlock()
		for _, ev := range e.events {
			if ev.Kind == EventAccountsAbnormal {
				n++
				last = ev.Accounts
			}
		}
		return
	}

	e.refresh("i0")
	if n, last := abnormal(); n != 1 || fmt.Sprint(last) != "[b.json c.json]" {
		t.Fatalf("first refresh: %d events, accounts %v (disabled must not count)", n, last)
	}
	if ev := e.lastEvent(); ev.InstanceID != "i0" || ev.InstanceName != "N0" {
		t.Errorf("event = %+v", ev)
	}
	e.refresh("i0")
	e.refresh("i0")
	if n, _ := abnormal(); n != 1 {
		t.Fatalf("unchanged abnormal credentials announced again: %d", n)
	}

	// One more fails: only it is announced.
	f.setFiles(cred("ok.json", "active"), cred("b.json", "error"), coolingCred("c.json"), cred("d.json", "error"))
	e.refresh("i0")
	if n, last := abnormal(); n != 2 || fmt.Sprint(last) != "[d.json]" {
		t.Fatalf("new failure: %d events, accounts %v", n, last)
	}

	// An outage in between does not make the known ones new again.
	f.setStatus(500)
	e.refresh("i0")
	f.setStatus(0)
	e.refresh("i0")
	if n, _ := abnormal(); n != 2 {
		t.Fatalf("outage and recovery re-announced credentials: %d", n)
	}

	// b recovers silently, then relapses and is announced again.
	f.setFiles(cred("ok.json", "active"), cred("b.json", "active"), coolingCred("c.json"), cred("d.json", "error"))
	e.refresh("i0")
	if n, _ := abnormal(); n != 2 {
		t.Fatalf("recovery announced as abnormal: %d", n)
	}
	f.setFiles(cred("ok.json", "active"), cred("b.json", "error"), coolingCred("c.json"), cred("d.json", "error"))
	e.refresh("i0")
	if n, last := abnormal(); n != 3 || fmt.Sprint(last) != "[b.json]" {
		t.Fatalf("relapse: %d events, accounts %v", n, last)
	}

	// Credentials that left the list are forgotten too.
	f.setFiles(cred("ok.json", "active"))
	e.refresh("i0")
	f.setFiles(cred("ok.json", "active"), cred("b.json", "error"))
	e.refresh("i0")
	if n, last := abnormal(); n != 4 || fmt.Sprint(last) != "[b.json]" {
		t.Fatalf("re-added credential: %d events, accounts %v", n, last)
	}
}

func TestSameNamedCredentialsOfDifferentInstancesAnnounceSeparately(t *testing.T) {
	a := newFakeCPA(t, "k", map[string]any{"name": "x.json", "status": "error"})
	b := newFakeCPA(t, "k", map[string]any{"name": "x.json", "status": "error"})
	e := newEnv(t, a, b)
	e.svc.RefreshAll()
	e.settle()
	if got := e.kinds(); len(got) != 2 || got[0] != EventAccountsAbnormal || got[1] != EventAccountsAbnormal {
		t.Errorf("events = %v", got)
	}
}

func TestEventsWithoutAHandlerAreFine(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a", "error"))
	e := newEnvWith(t, func(o *Options) { o.OnEvent = nil }, f)
	e.refresh("i0")
}

// ---- ordering, overview, settings ----

func TestMoveInstance(t *testing.T) {
	var servers []*fakeCPA
	for i := 0; i < 3; i++ {
		servers = append(servers, newFakeCPA(t, "k"))
	}
	e := newEnv(t, servers...)
	order := func() string {
		var ids []string
		for _, s := range e.svc.Snapshots() {
			ids = append(ids, s.Profile.ID)
		}
		return strings.Join(ids, ",")
	}
	persisted := func() string {
		st, _ := e.set.snapshot()
		var ids []string
		for i, p := range st.Instances {
			if p.Order != i {
				t.Errorf("persisted %s has Order %d at index %d", p.ID, p.Order, i)
			}
			ids = append(ids, p.ID)
		}
		return strings.Join(ids, ",")
	}

	if err := e.svc.MoveInstance("i2", -2); err != nil {
		t.Fatal(err)
	}
	if order() != "i2,i0,i1" || persisted() != "i2,i0,i1" {
		t.Fatalf("after move: %s / %s", order(), persisted())
	}
	if err := e.svc.MoveInstance("i2", 1); err != nil || order() != "i0,i2,i1" {
		t.Fatalf("move down: %s %v", order(), err)
	}
	if err := e.svc.MoveInstance("i0", 10); err != nil || order() != "i2,i1,i0" || persisted() != "i2,i1,i0" {
		t.Fatalf("move beyond the end clamps: %s %v", order(), err)
	}
	if err := e.svc.MoveInstance("i2", -5); err != nil || order() != "i2,i1,i0" {
		t.Fatalf("move beyond the start clamps: %s %v", order(), err)
	}
	_, saves := e.set.snapshot()
	if err := e.svc.MoveInstance("i1", 0); err != nil {
		t.Fatal(err)
	}
	if _, after := e.set.snapshot(); after != saves {
		t.Error("a no-op move saved the settings")
	}
	if err := e.svc.MoveInstance("nope", 1); !errors.Is(err, ErrNoInstance) {
		t.Errorf("unknown instance: %v", err)
	}
	for i, s := range e.svc.Snapshots() {
		if s.Profile.Order != i {
			t.Errorf("snapshot profile order %d at %d", s.Profile.Order, i)
		}
	}

	e.set.setErr(errBoom)
	if err := e.svc.MoveInstance("i0", -1); !errors.Is(err, errBoom) || order() != "i2,i1,i0" {
		t.Errorf("failed save must not reorder: %v %s", err, order())
	}
	e.set.setErr(nil)

	// A restart sees the persisted order.
	svc2, err := New(Options{Settings: e.set, Secrets: e.sec, NewClient: e.svc.opts.NewClient})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	var ids []string
	for _, s := range svc2.Snapshots() {
		ids = append(ids, s.Profile.ID)
	}
	if strings.Join(ids, ",") != "i2,i1,i0" {
		t.Errorf("after restart: %v", ids)
	}
}

func TestOverviewCounts(t *testing.T) {
	ok := newFakeCPA(t, "k", cred("a", "active"), cred("b", "error"))
	stale := newFakeCPA(t, "k", cred("c", "active"), cred("d", "active"), disabledCred("e"))
	paused := newFakeCPA(t, "k", cred("p", "error"))
	authFail := newFakeCPA(t, "other")
	unknown := newFakeCPA(t, "k")
	e := newEnvWith(t, func(o *Options) { o.Settings.(*fakeSettings).s.Instances[2].Paused = true },
		ok, stale, paused, authFail, unknown)
	e.rekey("i3", "wrong")

	for _, id := range []string{"i0", "i1", "i3"} {
		e.refresh(id)
	}
	stale.setStatus(500)
	e.refresh("i1")

	ov := e.svc.Overview()
	want := Overview{
		Instances: 4, Online: 1, Problem: 2, // paused excluded; unknown is neither online nor a problem
		Accounts: AccountSummary{Total: 5, Available: 3, Error: 1, Disabled: 1},
		Stale:    true,
	}
	if ov != want {
		t.Errorf("Overview = %+v\nwant       %+v", ov, want)
	}
	if ov.Healthy() {
		t.Error("Healthy with problems")
	}
	if got := ov.Accounts.Abnormal(); got != 1 {
		t.Errorf("Abnormal = %d", got)
	}
	wantReqs(t, paused, 0)
	wantReqs(t, unknown, 0)
}

func TestOverviewOfSnapshots(t *testing.T) {
	tests := []struct {
		name  string
		snaps []InstanceSnapshot
		want  Overview
		heal  bool
	}{
		{"none", nil, Overview{}, true},
		{"all online and clean", []InstanceSnapshot{
			{Conn: ConnOnline, HasData: true, Accounts: AccountSummary{Total: 2, Available: 2}},
			{Conn: ConnOnline, HasData: true, Accounts: AccountSummary{Total: 1, Available: 1, Disabled: 0}},
		}, Overview{Instances: 2, Online: 2, Accounts: AccountSummary{Total: 3, Available: 3}}, true},
		{"cooldown is abnormal", []InstanceSnapshot{
			{Conn: ConnOnline, HasData: true, Accounts: AccountSummary{Total: 2, Available: 1, Cooling: 1}},
		}, Overview{Instances: 1, Online: 1, Accounts: AccountSummary{Total: 2, Available: 1, Cooling: 1}}, false},
		{"disabled alone is healthy", []InstanceSnapshot{
			{Conn: ConnOnline, HasData: true, Accounts: AccountSummary{Total: 1, Disabled: 1}},
		}, Overview{Instances: 1, Online: 1, Accounts: AccountSummary{Total: 1, Disabled: 1}}, true},
		{"paused is excluded entirely", []InstanceSnapshot{
			{Conn: ConnPaused, HasData: true, Stale: true, Accounts: AccountSummary{Total: 9, Error: 9}},
		}, Overview{}, true},
		{"every non-online state is a problem except unknown", []InstanceSnapshot{
			{Conn: ConnUnknown}, {Conn: ConnOffline}, {Conn: ConnAuth}, {Conn: ConnUnavailable}, {Conn: ConnError},
		}, Overview{Instances: 5, Problem: 4}, false},
		{"stale data counts and flags", []InstanceSnapshot{
			{Conn: ConnOffline, HasData: true, Stale: true, Accounts: AccountSummary{Total: 4, Available: 4, Pending: 0}},
		}, Overview{Instances: 1, Problem: 1, Stale: true, Accounts: AccountSummary{Total: 4, Available: 4}}, false},
		{"counts without data are ignored", []InstanceSnapshot{
			{Conn: ConnError, Stale: true, Accounts: AccountSummary{Total: 7}},
		}, Overview{Instances: 1, Problem: 1}, false},
	}
	for _, tc := range tests {
		got := overviewOf(tc.snaps)
		if got != tc.want || got.Healthy() != tc.heal {
			t.Errorf("%s: %+v (healthy %v), want %+v (healthy %v)", tc.name, got, got.Healthy(), tc.want, tc.heal)
		}
	}
}

func TestSummarize(t *testing.T) {
	retry := time.Now()
	creds := []cpa.Credential{
		{Status: "active"}, {Status: "active"},
		{Status: "error"}, {Unavailable: true},
		{Unavailable: true, NextRetryAfter: &retry},
		{Disabled: true}, {Status: "disabled"},
		{Status: "pending"}, {Status: "refreshing"},
	}
	got := Summarize(creds)
	want := AccountSummary{Total: 9, Available: 2, Error: 2, Cooling: 1, Disabled: 2, Pending: 2}
	if got != want {
		t.Errorf("Summarize = %+v, want %+v", got, want)
	}
	if got.Abnormal() != 3 {
		t.Errorf("Abnormal = %d", got.Abnormal())
	}
	if Summarize(nil) != (AccountSummary{}) {
		t.Error("empty list")
	}
}

func TestSubscribeAndUpdateSettings(t *testing.T) {
	f := newFakeCPA(t, "k")
	e := newEnv(t, f)
	var mu sync.Mutex
	n := 0
	cancel := e.svc.Subscribe(func() { mu.Lock(); n++; mu.Unlock() })
	count := func() int { mu.Lock(); defer mu.Unlock(); return n }

	err := e.svc.UpdateSettings(func(s *store.Settings) {
		s.BackgroundRefresh = 1 // invalid: normalized
		s.Language = store.LanguageEN
		s.DefaultInstanceID = "ghost"
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.svc.Settings()
	if got.BackgroundRefresh != 30 || got.Language != store.LanguageEN || got.DefaultInstanceID != "" {
		t.Errorf("settings = %+v", got)
	}
	if st, _ := e.set.snapshot(); st.Language != store.LanguageEN {
		t.Error("not persisted")
	}
	if count() == 0 {
		t.Error("listener not called")
	}
	if err := e.svc.SetDefaultInstance("i0"); err != nil || e.svc.Settings().DefaultInstanceID != "i0" {
		t.Errorf("SetDefaultInstance: %v", err)
	}

	// Settings() is a copy.
	cp := e.svc.Settings()
	cp.Instances[0].Name = "mutated"
	if e.svc.Settings().Instances[0].Name == "mutated" {
		t.Error("Settings() shares its instance list")
	}

	cancel()
	before := count()
	_ = e.svc.UpdateSettings(func(s *store.Settings) { s.Notifications = false })
	if count() != before {
		t.Error("listener called after unsubscribe")
	}
	e.set.setErr(errBoom)
	if err := e.svc.UpdateSettings(func(s *store.Settings) { s.Language = store.LanguageZH }); !errors.Is(err, errBoom) || e.svc.Settings().Language != store.LanguageEN {
		t.Errorf("failed save must not apply: %v", err)
	}
}

// ---- connection test and operations ----

func TestTestConnection(t *testing.T) {
	f := newFakeCPA(t, "good", cred("a", "active"))
	e := newEnv(t, f)
	_, saves := e.set.snapshot()

	if err := e.svc.TestConnection(bg, InstanceInput{Address: f.srv.URL, Key: "good"}, ""); err != nil {
		t.Errorf("good key: %v", err)
	}
	if err := e.svc.TestConnection(bg, InstanceInput{Address: f.srv.URL, Key: "bad"}, ""); !cpa.IsAuthError(err) {
		t.Errorf("bad key: %v", err)
	}
	if err := e.svc.TestConnection(bg, InstanceInput{Address: f.srv.URL}, ""); err == nil {
		t.Error("empty key accepted")
	}
	if err := e.svc.TestConnection(bg, InstanceInput{Address: "ftp://x", Key: "k"}, ""); err == nil {
		t.Error("invalid address accepted")
	}
	// Editing an existing instance may leave the key empty: the stored one is used.
	if err := e.svc.TestConnection(bg, InstanceInput{Address: f.srv.URL}, "i0"); err != nil {
		t.Errorf("stored key: %v", err)
	}
	if err := e.svc.TestConnection(bg, InstanceInput{Address: f.srv.URL}, "unknown"); !errors.Is(err, store.ErrSecretNotFound) {
		t.Errorf("unknown instance: %v", err)
	}
	if _, after := e.set.snapshot(); after != saves || len(e.svc.Snapshots()) != 1 {
		t.Error("TestConnection changed state")
	}
}

func TestSetCredentialDisabledWritesThenRefreshes(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"))
	e := newEnv(t, f)
	e.refresh("i0")

	err := e.svc.SetCredentialDisabled(bg, "i0", cpa.Credential{Name: "a.json", AuthIndex: "idx-a.json"}, true)
	if err != nil {
		t.Fatal(err)
	}
	e.settle()
	log := f.requestLog()
	if len(log) != 3 || log[1] != "PATCH /credentials/status" || log[2] != "GET /credentials" {
		t.Errorf("requests = %v", log)
	}
	// A credential without a name falls back to its id.
	if err := e.svc.SetCredentialDisabled(bg, "i0", cpa.Credential{ID: "only-id"}, false); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if err := e.svc.SetCredentialDisabled(bg, "nope", cpa.Credential{Name: "x"}, true); !errors.Is(err, ErrNoInstance) {
		t.Errorf("unknown instance: %v", err)
	}
}

func TestDefaultClientFactoryBuildsRealClients(t *testing.T) {
	c, err := DefaultClientFactory(store.InstanceProfile{ID: "x", Name: "X", BaseURL: "http://h:1/v8/management"}, "k")
	if err != nil || c.InstanceID() != "x" || c.BaseURL() != "http://h:1/v8/management" {
		t.Errorf("client = %+v, err = %v", c, err)
	}
	if _, err := DefaultClientFactory(store.InstanceProfile{BaseURL: "http://h", CACertPEM: "garbage"}, "k"); err == nil {
		t.Error("invalid CA accepted")
	}
	// A bad CA leaves the instance without a client, reported as an auth-class state.
	f := newFakeCPA(t, "k")
	e := newEnvWith(t, func(o *Options) {
		o.Settings.(*fakeSettings).s.Instances[0].CACertPEM = "garbage"
		o.NewClient = DefaultClientFactory
	}, f)
	if s := e.refresh("i0"); s.Conn != ConnAuth || s.Err == nil {
		t.Errorf("snapshot = %+v", s)
	}
	wantReqs(t, f, 0)
}

func TestChangingRedirectAuthorizationSettingReconnects(t *testing.T) {
	f := newFakeCPA(t, "k")
	var built []bool
	e := newEnvWith(t, func(o *Options) {
		o.NewClient = func(p store.InstanceProfile, key string) (*cpa.Client, error) {
			built = append(built, p.ForwardAuthOnRedirect)
			return cpa.New(cpa.Options{
				InstanceID: p.ID, InstanceName: p.Name, BaseURL: p.BaseURL, ManagementKey: key,
				ForwardAuthOnRedirect: p.ForwardAuthOnRedirect,
			})
		}
	}, f)
	if err := e.svc.UpdateInstance("i0", InstanceInput{
		Name: "N0", Address: f.srv.URL, ForwardAuthOnRedirect: true,
	}); err != nil {
		t.Fatal(err)
	}
	e.settle()
	if !slices.Equal(built, []bool{false, true}) {
		t.Fatalf("client builds = %v", built)
	}
	if p := e.svc.Settings().Instances[0]; !p.ForwardAuthOnRedirect {
		t.Errorf("setting was not persisted: %+v", p)
	}
}

func TestNewInstanceIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewInstanceID()
		if len(id) != 16 || seen[id] {
			t.Fatalf("id %q (len %d) repeated or malformed", id, len(id))
		}
		seen[id] = true
	}
}

func TestSetCredentialPinned(t *testing.T) {
	f := newFakeCPA(t, "k", cred("a.json", "active"), cred("b.json", "active"))
	e := newEnv(t, f)
	e.refresh("i0")

	for _, name := range []string{"b.json", "a.json", "b.json"} {
		if err := e.svc.SetCredentialPinned("i0", name, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.snap("i0").Profile.PinnedAccounts; !slices.Equal(got, []string{"b.json", "a.json"}) {
		t.Errorf("pinned = %v", got)
	}
	if err := e.svc.SetCredentialPinned("i0", "b.json", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.set.snapshot(); !slices.Equal(st.Instances[0].PinnedAccounts, []string{"a.json"}) {
		t.Errorf("saved = %v", st.Instances[0].PinnedAccounts)
	}

	// Editing the instance keeps its pins.
	if err := e.svc.UpdateInstance("i0", InstanceInput{Name: "Renamed", Address: f.srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := e.snap("i0").Profile.PinnedAccounts; !slices.Equal(got, []string{"a.json"}) {
		t.Errorf("pins after edit = %v", got)
	}

	if err := e.svc.SetCredentialPinned("nope", "a.json", true); !errors.Is(err, ErrNoInstance) {
		t.Errorf("unknown instance: %v", err)
	}
	e.set.setErr(errBoom)
	if err := e.svc.SetCredentialPinned("i0", "b.json", true); !errors.Is(err, errBoom) || e.snap("i0").Profile.Pinned("b.json") {
		t.Errorf("failed save must not pin: %v", err)
	}
}
