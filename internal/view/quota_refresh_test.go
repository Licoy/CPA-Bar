package view

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestQuotaRefreshDue(t *testing.T) {
	now := time.Now()
	for _, minutes := range []int{1, 5, 17, store.MaxQuotaRefreshMinutes} {
		interval := time.Duration(minutes) * time.Minute
		e := quotaEntry{at: now}
		if interval <= 0 || e.due(now.Add(interval-time.Nanosecond), interval) || !e.due(now.Add(interval), interval) {
			t.Fatalf("incorrect %d minute threshold", minutes)
		}
	}
	for _, interval := range []time.Duration{time.Minute, 17 * time.Minute} {
		e := quotaEntry{}
		if !e.due(now, interval) {
			t.Fatal("initial read not due")
		}
		e.fetching = true
		if e.due(now, interval) {
			t.Fatal("duplicate in-flight read")
		}
		e.fetching = false
		e.acting = true
		if e.due(now, interval) {
			t.Fatal("read during action")
		}
		e.acting = false
		e.failures = 2
		e.failedAt = now
		if e.due(now.Add(4*time.Minute-time.Nanosecond), interval) || !e.due(now.Add(4*time.Minute), interval) {
			t.Fatal("quota interval changed failure backoff")
		}
	}
}

func TestQuotaRefreshSettingsApplyToExistingCache(t *testing.T) {
	a, id, srv, q := quotaApp(t)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, q, "initial quota", func() bool { return tt.HasText("剩余 95%") })
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	e, cache, _ := a.quotaEntryFor(snapshot, cr, time.Now())
	at := e.at
	before := srv.count("GET")
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.QuotaRefreshMinutes = 17 }); err != nil {
		t.Fatal(err)
	}
	same, _, _ := a.quotaEntryFor(snapshot, cr, at.Add(6*time.Minute))
	if same != e || same.res == nil || same.fetching || srv.count("GET") != before {
		t.Fatal("extending interval discarded or refreshed cached quota")
	}
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.QuotaRefreshMinutes = 1 }); err != nil {
		t.Fatal(err)
	}
	a.quotaEntryFor(snapshot, cr, at.Add(time.Minute))
	if !e.fetching {
		t.Fatal("shortening interval did not immediately make cached quota due")
	}
	waitFor(t, tt, q, "shortened refresh", func() bool { return !e.fetching })
	before = srv.count("GET")
	a.quotaFetch(cache, e, cr, true)
	waitFor(t, tt, q, "manual refresh within interval", func() bool { return !e.fetching && srv.count("GET") > before })
}

func TestQuotaRefreshDoesNotChangePluginCacheTTL(t *testing.T) {
	a, id, _, _, queue := passiveApp(t)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false; s.QuotaRefreshMinutes = 1 }); err != nil {
		t.Fatal(err)
	}
	cl, err := a.Svc.Client(id)
	if err != nil {
		t.Fatal(err)
	}
	cache := a.quotaCache(id, cl)
	cache.pluginsAt = time.Now().Add(-4 * time.Minute)
	if !a.quotaPlugins(cache, false) || cache.pluginsBusy {
		t.Fatal("one-minute account interval expired four-minute plugin cache")
	}
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.QuotaRefreshMinutes = 17 }); err != nil {
		t.Fatal(err)
	}
	cache.pluginsAt = time.Now().Add(-6 * time.Minute)
	a.quotaPlugins(cache, false)
	if !cache.pluginsBusy {
		t.Fatal("long account interval extended five-minute plugin cache")
	}
	deadline := time.Now().Add(5 * time.Second)
	for cache.pluginsBusy && time.Now().Before(deadline) {
		queue.drain()
		time.Sleep(time.Millisecond)
	}
	if cache.pluginsBusy || cache.pluginsErr != nil {
		t.Fatalf("plugin refresh failed: %v", cache.pluginsErr)
	}
}

func TestQuotaRefreshWakeScheduling(t *testing.T) {
	a, _ := newTestApp(t, false)
	t.Cleanup(a.cancelQuotaWake)
	a.syncQuotaWake()
	now := time.Now()
	e := &quotaEntry{at: now}
	a.scheduleQuotaWake(e, now)
	first, id := a.quotaWake, a.quotaWakeID
	if first == nil || !a.quotaWakeAt.Equal(now.Add(5*time.Minute)) {
		t.Fatal("missing five-minute wake")
	}
	a.scheduleQuotaWake(e, now)
	a.scheduleQuotaWake(&quotaEntry{at: now.Add(time.Minute)}, now)
	if a.quotaWake != first || a.quotaWakeID != id {
		t.Fatal("frame rescheduled an unchanged/later deadline")
	}
	e.at = now.Add(-time.Minute)
	a.scheduleQuotaWake(e, now)
	if a.quotaWake == first || !a.quotaWakeAt.Equal(now.Add(4*time.Minute)) {
		t.Fatal("earlier deadline was not selected")
	}
	current := a.quotaWake
	a.expireQuotaWake(id)
	if a.quotaWake != current {
		t.Fatal("cancelled callback invalidated the new timer")
	}
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = true }); err != nil {
		t.Fatal(err)
	}
	a.syncQuotaWake()
	a.scheduleQuotaWake(e, now)
	if a.quotaWake != nil {
		t.Fatal("passive mode scheduled an active wake")
	}
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = false; s.QuotaRefreshMinutes = 1 }); err != nil {
		t.Fatal(err)
	}
	a.syncQuotaWake()
	a.scheduleQuotaWake(&quotaEntry{fetching: true, at: now}, now)
	a.scheduleQuotaWake(&quotaEntry{}, now)
	if a.quotaWake != nil {
		t.Fatal("busy or already overdue entry scheduled a busy loop")
	}
	a.scheduleQuotaWake(&quotaEntry{at: now}, now)
	id = a.quotaWakeID
	a.quitting = true
	a.cancelQuotaWake()
	a.expireQuotaWake(id)
	if a.quotaWake != nil {
		t.Fatal("quit left an active wake")
	}
}

func TestQuotaRefreshWakeIndependentOfCredentialPolling(t *testing.T) {
	a, id, srv, upstream, q := passiveApp(t)
	t.Cleanup(a.cancelQuotaWake)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) {
		s.PassiveQuota = false
		s.QuotaRefreshMinutes = 1
		s.BackgroundRefresh = 600
		s.ActiveRefresh = 360
	}); err != nil {
		t.Fatal(err)
	}
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, q, "initial quota", func() bool { return tt.HasText("剩余 95%") })
	snapshot, _ := a.Svc.Snapshot(id)
	e, _, _ := a.quotaEntryFor(snapshot, snapshot.Credentials[0], time.Now())
	a.cancelQuotaWake()
	now := time.Now()
	e.at = now.Add(-time.Minute + 30*time.Millisecond)
	before := upstream.count("GET")
	srv.mu.Lock()
	requests := len(srv.requests)
	srv.mu.Unlock()
	a.scheduleQuotaWake(e, now)
	deadline := time.Now().Add(3 * time.Second)
	// Do not build any frames or notify the service until the real timer posts
	// its callback, proving the independent wake exists without minute-long sleeps.
	for len(q.ch) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(q.ch) == 0 {
		t.Fatal("quota deadline did not wake the UI")
	}
	q.drain()
	if a.quotaWake != nil {
		t.Fatal("expired wake was not cleared")
	}
	tt.Frame()
	waitFor(t, tt, q, "quota refreshed by wake", func() bool { return !e.fetching && upstream.count("GET") > before })
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, request := range srv.requests[requests:] {
		if request == "GET /v8/management/credentials" {
			t.Fatal("quota wake depended on a credential poll")
		}
	}
}
