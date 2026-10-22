package view

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestDisabledQuotaPausesAutomaticReads(t *testing.T) {
	defer SetLanguage(store.LanguageZH)
	_ = SetLanguage(store.LanguageZH)
	for _, disabledFlag := range []bool{true, false} {
		name := "status"
		if disabledFlag {
			name = "flag"
		}
		t.Run(name, func(t *testing.T) {
			a, id, _, upstream, q := passiveApp(t)
			t.Cleanup(a.cancelQuotaWake)
			if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false }); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := a.Svc.Snapshot(id)
			cr := snapshot.Credentials[0]
			cr.Disabled = disabledFlag
			if !disabledFlag {
				cr.Status = cpa.StatusDisabled
			}
			tt := ui.NewTester(func(c *ui.Context) { a.quotaView(c, snapshot, cr, time.Now(), 3) }, managerWidth, 300)
			if !tt.HasText("账号已停用，额度自动刷新已暂停") || tt.HasText("正在读取额度") {
				t.Fatalf("disabled state is misleading: %q", tt.Texts())
			}
			_ = SetLanguage(store.LanguageEN)
			tt.Frame()
			if !tt.HasText("Account disabled; automatic quota refresh paused") {
				t.Fatalf("English pause state missing: %q", tt.Texts())
			}
			_ = SetLanguage(store.LanguageZH)
			tt.Frame()
			e, cache, _ := a.quotaEntryFor(snapshot, cr, time.Now().Add(time.Hour))
			a.quotaFetch(cache, e, cr, false)
			if e.fetching || cache.running != 0 || a.quotaWake != nil || upstream.count("GET") != 0 {
				t.Fatal("disabled initial display queried quota or scheduled a wake")
			}
			// An explicit refresh remains available while disabled.
			if err := tt.Click("刷新额度"); err != nil {
				t.Fatal(err)
			}
			if !tt.HasText("正在读取额度") || tt.HasText("自动刷新已暂停") {
				t.Fatalf("manual refresh was not shown as reading: %q", tt.Texts())
			}
			waitFor(t, tt, q, "manual disabled quota", func() bool { return !e.fetching && tt.HasText("剩余 95%") })
			if upstream.count("GET") != 3 {
				t.Fatalf("manual read made %d requests, want 3", upstream.count("GET"))
			}
			if a.quotaWake != nil {
				t.Fatal("manual disabled result scheduled an automatic wake")
			}
			at := time.Now().Add(-6 * time.Minute)
			e.at = at
			cached := e.res
			before := upstream.count("GET")
			tt.Frame()
			if e.fetching || e.res != cached || !e.at.Equal(at) || a.quotaWake != nil || upstream.count("GET") != before {
				t.Fatal("expired disabled cache was refreshed, discarded or scheduled")
			}
			cr.Disabled, cr.Status = false, cpa.StatusActive
			tt.Frame()
			waitFor(t, tt, q, "quota after re-enabling", func() bool { return !e.fetching && upstream.count("GET") == before+3 })
		})
	}
}

func TestDisabledQuotaKeepsPassiveObservations(t *testing.T) {
	a, id, _, upstream, _ := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	cr.Disabled = true
	tt := ui.NewTester(func(c *ui.Context) { a.quotaView(c, snapshot, cr, time.Now(), 3) }, managerWidth, 300)
	if !tt.HasText("剩余 47%") || !tt.HasText("CPA 观测") || tt.HasText("自动刷新已暂停") {
		t.Fatalf("disabled passive observation was hidden: %q", tt.Texts())
	}
	if upstream.count("GET") != 0 || a.quotaWake != nil {
		t.Fatal("disabled passive display queried upstream or scheduled wake")
	}
}

func TestDisabledQuotaDoesNotDiscoverPluginsAutomatically(t *testing.T) {
	a, id, srv, _, q := passiveApp(t)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false }); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	cr.Provider, cr.QuotaProvider, cr.SupportsQuota = "custom", "custom", true
	cr.Status = cpa.StatusDisabled
	e, cache, _ := a.quotaEntryFor(snapshot, cr, time.Now())
	a.quotaFetch(cache, e, cr, false)
	if cache.pluginsBusy || e.fetching || e.manualPending != nil {
		t.Fatal("disabled account started plugin discovery")
	}
	srv.mu.Lock()
	for _, request := range srv.requests {
		if strings.Contains(request, "/plugins") {
			t.Errorf("automatic plugin request %s", request)
		}
	}
	srv.mu.Unlock()
	a.quotaFetch(cache, e, cr, true)
	deadline := time.Now().Add(5 * time.Second)
	for e.res == nil && time.Now().Before(deadline) {
		q.drain()
		time.Sleep(time.Millisecond)
	}
	if e.res == nil || e.err != nil {
		t.Fatalf("explicit disabled plugin refresh failed: %v", e.err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	plugins, reads := 0, 0
	for _, request := range srv.requests {
		if request == "GET /v8/management/plugins" {
			plugins++
		}
		if request == "GET /v8/management/plugins/custom/quota" {
			reads++
		}
	}
	if plugins != 1 || reads != 1 {
		t.Fatalf("manual plugin requests: discovery %d, quota %d", plugins, reads)
	}
}
