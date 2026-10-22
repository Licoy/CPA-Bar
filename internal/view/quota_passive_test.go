package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

type passiveCPA struct {
	mu          sync.Mutex
	credentials string
	requests    []string
	hold        chan struct{}
}

func (s *passiveCPA) setQuota(q string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = `{"files":[{"id":"a.json","auth_index":"1","name":"a.json","provider":"claude","email":"pro@example.com","status":"active","quota":` + q + `}]}`
}

func passiveObservation(at time.Time, used int) string {
	return fmt.Sprintf(`{"observed_at":%q,"signals":{"Anthropic-Ratelimit-Unified-5h-Utilization":%q}}`, at.Format(time.RFC3339Nano), fmt.Sprintf("%.2f", float64(used)/100))
}

func passiveApp(t *testing.T) (*App, string, *passiveCPA, *quotaCPA, *uiQueue) {
	t.Helper()
	queue := installTestRun(t)
	upstream := newQuotaCPA(t)
	state := &passiveCPA{}
	state.setQuota(passiveObservation(time.Now().UTC().Add(-time.Hour), 53))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.requests = append(state.requests, r.Method+" "+r.URL.Path)
		creds, hold := state.credentials, state.hold
		state.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/credentials") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(creds))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/plugins") {
			_, _ = w.Write([]byte(`{"plugins":[{"id":"custom","registered":true,"supports_quota":true,"quota_provider":"custom"}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/plugins/custom/quota") {
			_, _ = w.Write([]byte(`{"groups":[{"buckets":[{"window":"5h","remaining_fraction":0.62}]}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/api-call") && hold != nil {
			<-hold
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	svc, err := core.New(core.Options{Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)}, Secrets: &store.MemorySecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	if err := svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = true }); err != nil {
		t.Fatal(err)
	}
	id, err := svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: srv.URL, Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, _ := svc.Snapshot(id)
		if snapshot.HasData {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("credentials did not load")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a := NewApp(svc)
	a.selected, a.panelInstance = id, id
	return a, id, state, upstream, queue
}

func TestPassiveQuotaFollowsCredentialsWithoutQueries(t *testing.T) {
	a, id, srv, upstream, q := passiveApp(t)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, q, "passive quota", func() bool { return tt.HasText("剩余 47%") })
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	e, _, _ := a.quotaEntryFor(snapshot, cr, time.Now().Add(24*time.Hour))
	if e.at.IsZero() || !e.fromCPA || time.Since(e.at) < 30*time.Minute {
		t.Fatalf("CPA observation time lost: %+v", e)
	}
	a.trayRows(2, time.Now().Add(24*time.Hour))
	panel := ui.NewTester(a.panelView, panelWidth, panelHeight)
	panel.Frame()
	q.drain()
	if !panel.HasText("剩余 47%") || !panel.HasText(e.sourceText()) {
		t.Fatalf("panel lacks passive quota/source: %q", panel.Texts())
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "panel-passive-quota.png"), panel)
	}
	srv.mu.Lock()
	for _, req := range srv.requests {
		if strings.Contains(req, "api-call") || strings.Contains(req, "plugins") {
			t.Errorf("passive display requested %s", req)
		}
	}
	srv.mu.Unlock()
	srv.setQuota(passiveObservation(e.at.Add(time.Minute), 72))
	a.Svc.Refresh(id)
	waitFor(t, tt, q, "new CPA observation", func() bool { return tt.HasText("剩余 28%") })
	if n := upstream.count("GET"); n != 0 {
		t.Fatalf("passive mode made %d upstream requests", n)
	}
	// A manual read stays visible through repeated renders of that same server observation.
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "manual quota", func() bool { return tt.HasText("剩余 95%") })
	for range 3 {
		tt.Frame()
		q.drain()
	}
	if !tt.HasText("剩余 95%") {
		t.Fatal("old CPA data replaced a manual result")
	}
	// Server clock is deliberately behind local time, so freshness cannot use e.at.
	snapshot, _ = a.Svc.Snapshot(id)
	e, _, _ = a.quotaEntryFor(snapshot, snapshot.Credentials[0], time.Now())
	srv.setQuota(passiveObservation(e.observedAt.Add(time.Minute), 81))
	a.Svc.Refresh(id)
	waitFor(t, tt, q, "CPA following manual read", func() bool { return tt.HasText("剩余 19%") })
}

func TestPassiveQuotaMissingAndMalformedAreVisible(t *testing.T) {
	for _, tc := range []struct{ name, quota, want string }{
		{"missing", "null", "等待 CPA 额度数据"},
		{"malformed", `{"observed_at":12,"signals":{}}`, "读取额度失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, id, srv, upstream, q := passiveApp(t)
			srv.setQuota(tc.quota)
			a.Svc.Refresh(id)
			a.page = "accounts"
			tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
			waitFor(t, tt, q, tc.name, func() bool {
				for _, s := range tt.Texts() {
					if strings.Contains(s, tc.want) {
						return true
					}
				}
				return false
			})
			if tt.HasText("正在读取额度…") || tt.HasText("剩余 100%") {
				t.Fatalf("missing data became success/loading: %q", tt.Texts())
			}
			if n := upstream.count("GET"); n != 0 {
				t.Fatalf("invalid data caused %d upstream requests", n)
			}
		})
	}
}

func TestPassiveQuotaIgnoresOldInflightAndActiveResumes(t *testing.T) {
	a, id, srv, upstream, q := passiveApp(t)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false }); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.hold = make(chan struct{})
	hold := srv.hold
	srv.mu.Unlock()
	snapshot, _ := a.Svc.Snapshot(id)
	old, _, _ := a.quotaEntryFor(snapshot, snapshot.Credentials[0], time.Now())
	if !old.fetching {
		t.Fatal("active mode did not start an upstream read")
	}
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = true }); err != nil {
		t.Fatal(err)
	}
	e, _, _ := a.quotaEntryFor(snapshot, snapshot.Credentials[0], time.Now())
	if e == old || !e.fromCPA {
		t.Fatal("mode switch reused active cache")
	}
	close(hold)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, q, "old response completion", func() bool { return !old.fetching })
	if !tt.HasText("剩余 47%") {
		t.Fatalf("old active response replaced passive quota: %q", tt.Texts())
	}
	before := upstream.count("GET")
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "active mode restored", func() bool { return tt.HasText("剩余 95%") })
	if upstream.count("GET") <= before {
		t.Fatal("active mode did not resume reads")
	}
}

func TestPassiveQuotaUnsupportedProviderDoesNotLoadPlugins(t *testing.T) {
	a, id, srv, upstream, q := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	cr.Provider = "custom"
	cr.SupportsQuota = true
	cr.QuotaProvider = "custom"
	cr.Raw = json.RawMessage(`{"provider":"custom","supports_quota":true,"quota_provider":"custom"}`)
	e, cache, ok := a.quotaEntryFor(snapshot, cr, time.Now())
	if !ok || e.res != nil || e.fetching || cache.pluginsBusy {
		t.Fatal("unsupported passive quota started a query or lost its waiting entry")
	}
	a.quotaEntryFor(snapshot, cr, time.Now().Add(24*time.Hour))
	srv.mu.Lock()
	for _, request := range srv.requests {
		if strings.Contains(request, "plugins") || strings.Contains(request, "api-call") {
			t.Errorf("unexpected automatic request %s", request)
		}
	}
	srv.mu.Unlock()
	// One explicit refresh must continue past plugin discovery without an automatic render retry.
	a.quotaFetch(cache, e, cr, true)
	deadline := time.Now().Add(5 * time.Second)
	for e.res == nil && time.Now().Before(deadline) {
		q.drain()
		time.Sleep(10 * time.Millisecond)
	}
	if e.res == nil {
		t.Fatalf("manual plugin refresh did not complete: %v", e.err)
	}
	short, _ := e.res.Summary()
	if short == nil || short.Remaining != 0.62 || e.fromCPA {
		t.Fatalf("manual plugin result = %+v", e)
	}
	if upstream.count("GET") != 0 {
		t.Fatal("plugin provider queried built-in upstream")
	}
}

func TestPassiveQuotaNewObservationWinsOverInflightManual(t *testing.T) {
	a, id, srv, _, q := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	e, cache, _ := a.quotaEntryFor(snapshot, cr, time.Now())
	srv.mu.Lock()
	srv.hold = make(chan struct{})
	hold := srv.hold
	srv.mu.Unlock()
	a.quotaFetch(cache, e, cr, true)
	var updated = cr
	updated.Raw = json.RawMessage(`{"quota":` + passiveObservation(e.observedAt.Add(time.Minute), 81) + `}`)
	a.quotaEntryFor(snapshot, updated, time.Now())
	close(hold)
	deadline := time.Now().Add(5 * time.Second)
	for e.fetching && time.Now().Before(deadline) {
		q.drain()
		time.Sleep(10 * time.Millisecond)
	}
	short, _ := e.res.Summary()
	if e.fetching || !e.fromCPA || short == nil || percent(short.Remaining) != 19 {
		t.Fatalf("manual response replaced a newer CPA observation: %+v", e)
	}
}

func TestPassiveQuotaObservationVersions(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	cr := cpa.Credential{Provider: "claude", AuthIndex: "1", Raw: json.RawMessage(`{"quota":` + passiveObservation(at, 53) + `}`)}
	e := &quotaEntry{}
	e.observe(cr)
	manual := &quota.Result{Plan: "manual"}
	e.res, e.fromCPA = manual, false
	// JSON property order is not a new server observation.
	cr.Raw = json.RawMessage(fmt.Sprintf(`{"quota":{"signals":{"Anthropic-Ratelimit-Unified-5h-Utilization":"0.53"},"observed_at":%q}}`, at.Format(time.RFC3339Nano)))
	e.observe(cr)
	if e.res != manual {
		t.Fatal("equivalent observation replaced manual quota")
	}
	// Invalid old signals must still respect the server timestamp watermark.
	cr.Raw = json.RawMessage(fmt.Sprintf(`{"quota":{"observed_at":%q,"signals":{"Anthropic-Ratelimit-Unified-5h-Utilization":"NaN"}}}`, at.Add(-time.Minute).Format(time.RFC3339Nano)))
	e.observe(cr)
	if e.res != manual || !e.observedAt.Equal(at) {
		t.Fatal("old malformed observation replaced newer quota")
	}
	cr.Raw = json.RawMessage(`{"quota":null}`)
	e.observe(cr)
	if !e.observedAt.Equal(at) {
		t.Fatal("missing observation reset server watermark")
	}
	cr.Raw = json.RawMessage(`{"quota":` + passiveObservation(at.Add(-time.Minute), 80) + `}`)
	e.observe(cr)
	if e.res != nil {
		t.Fatal("old observation passed watermark after missing data")
	}
	cr.Raw = json.RawMessage(`{"quota":` + passiveObservation(at.Add(time.Minute), 80) + `}`)
	e.observe(cr)
	if e.res == nil || !e.fromCPA {
		t.Fatal("new server observation did not restore quota")
	}
}
