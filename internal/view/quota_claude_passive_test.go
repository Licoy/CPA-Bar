package view

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
)

func claudeRecorded(at time.Time, used int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"quota":%s,"claude_subscription":{"usage_observed_at":%q,"observed_at":%q,"usage_age_seconds":10,"evaluation":{"reason":"fresh"},"usage":{"five_hour":{"utilization":%d,"resets_at":"2026-11-15T09:00:00Z"},"seven_day":{"utilization":16,"resets_at":"2026-11-19T09:00:00Z"},"seven_day_sonnet":{"utilization":10,"resets_at":"2026-11-19T09:00:00Z"},"iguana_necktie":{"utilization":5,"limit_dollars":100,"used_dollars":5,"remaining_dollars":95,"resets_at":"2026-12-01T00:00:00Z"},"additional_credit_balance":{"limit_dollars":20,"used_dollars":0,"remaining_dollars":20,"resets_at":null},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5"}},"percent":31,"is_active":true,"resets_at":"2026-11-19T09:00:00Z"},{"kind":"weekly_scoped","scope":{"model":{"display_name":"Future Model"}},"percent":23,"is_active":true,"resets_at":"2026-11-19T09:00:00Z"}],"extra_usage":{"is_enabled":true,"used_credits":1250,"monthly_limit":5000},"cedar_ember":{"eligible":true,"grants":[{"id":"reset_a","resets_total":5,"resets_left":3}]}}}}`, passiveObservation(at.Add(time.Hour), 53), at.Format(time.RFC3339Nano), at.Add(time.Hour).Format(time.RFC3339Nano), used))
}

func setClaudeRecorded(srv *passiveCPA, raw json.RawMessage) {
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		panic(err)
	}
	record["id"], record["auth_index"], record["name"] = "a.json", "1", "a.json"
	record["provider"], record["status"], record["email"] = "claude", "active", "pro@example.com"
	body, err := json.Marshal(map[string]any{"files": []any{record}})
	if err != nil {
		panic(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.credentials = string(body)
}

func TestClaudePassiveExtendedCredentialsDisplay(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	_, legacyAt, err := quota.PassiveObservationMeta(cr)
	if err != nil || legacyAt.IsZero() {
		t.Fatalf("initial legacy observation: %v, %v", legacyAt, err)
	}
	e, _, _ := a.quotaEntryFor(snapshot, cr, time.Now())
	if e == nil || e.err != nil || e.res == nil || !e.fromCPA || !e.observedAt.Equal(legacyAt) {
		t.Fatalf("initial legacy cache: %+v", e)
	}
	at := legacyAt.Add(time.Minute)
	setClaudeRecorded(srv, claudeRecorded(at, 42))
	a.Svc.Refresh(id)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, tt, queue, "extended Claude windows", func() bool { return tt.HasText("7 天 Fable 5") })
	panel := ui.NewTester(a.panelView, panelWidth, 1800)
	panel.Frame()
	queue.drain()
	for name, screen := range map[string]*ui.Tester{"accounts": tt, "panel": panel} {
		for _, want := range []string{"5 小时限额", "7 天限额", "7 天 Sonnet", "7 天 Fable 5", "Future Model", "$12.50 / $50.00", "$95.00", "$20.00", "剩余重置次数 3", "CPA 观测 · " + at.Local().Format("01/02 15:04:05"), time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC).Local().Format("01/02 15:04")} {
			if !strings.Contains(strings.Join(screen.Texts(), "\n"), want) {
				t.Errorf("%s lacks %q: %q", name, want, screen.Texts())
			}
		}
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "claude-passive-extended-accounts.png"), tt)
		saveSnapshot(t, filepath.Join(dir, "claude-passive-extended-panel.png"), panel)
		normal := ui.NewTester(a.panelView, panelWidth, panelHeight)
		normal.Frame()
		saveSnapshot(t, filepath.Join(dir, "claude-passive-panel-normal.png"), normal)
	}
	if upstream.count("GET") != 0 {
		t.Fatal("extended display queried upstream")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, request := range srv.requests {
		if strings.Contains(request, "api-call") || strings.Contains(request, "plugins") {
			t.Errorf("passive request: %s", request)
		}
	}
}

func TestClaudePassiveUsageObservationVersions(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	cr := cpa.Credential{Provider: "claude", AuthIndex: "1", Raw: claudeRecorded(at, 42)}
	e := &quotaEntry{}
	e.observe(cr)
	if e.err != nil || e.res == nil || !e.at.Equal(at) {
		t.Fatalf("initial extended observation: %+v", e)
	}
	manual := &quota.Result{Plan: "manual"}
	e.res, e.fromCPA = manual, false
	changed := strings.ReplaceAll(string(cr.Raw), `"usage_age_seconds":10`, `"usage_age_seconds":90`)
	changed = strings.ReplaceAll(changed, `"reason":"fresh"`, `"reason":"stale"`)
	cr.Raw = json.RawMessage(changed)
	e.observe(cr)
	if e.res != manual {
		t.Fatal("cache metadata replaced manual result")
	}
	cr.Raw = claudeRecorded(at.Add(-time.Minute), 80)
	e.observe(cr)
	if e.res != manual || !e.observedAt.Equal(at) {
		t.Fatal("old usage with newer quota header replaced manual result")
	}
	cr.Raw = json.RawMessage(strings.Replace(string(cr.Raw), `"utilization":80`, `"utilization":"invalid"`, 1))
	e.observe(cr)
	if e.res != manual {
		t.Fatal("old malformed usage replaced manual result")
	}
	cr.Raw = claudeRecorded(at.Add(time.Minute), 81)
	e.observe(cr)
	short, _ := e.res.Summary()
	if short == nil || percent(short.Remaining) != 19 || !e.fromCPA || !e.at.Equal(at.Add(time.Minute)) {
		t.Fatalf("new usage ignored: %+v", e)
	}
	// A changed usage payload is visible even when the quota/header fields stay identical.
	var record map[string]any
	if err := json.Unmarshal(cr.Raw, &record); err != nil {
		t.Fatal(err)
	}
	record["claude_subscription"].(map[string]any)["usage"].(map[string]any)["five_hour"].(map[string]any)["utilization"] = 67
	cr.Raw, _ = json.Marshal(record)
	e.observe(cr)
	short, _ = e.res.Summary()
	if short == nil || percent(short.Remaining) != 33 {
		t.Fatal("changed usage was ignored because quota did not change")
	}
}

func TestClaudePassiveExtendedNewObservationWinsManual(t *testing.T) {
	a, id, srv, _, queue := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	cr := snapshot.Credentials[0]
	at := time.Now().UTC().Add(-time.Hour)
	cr.Raw = claudeRecorded(at, 42)
	e, cache, _ := a.quotaEntryFor(snapshot, cr, time.Now())
	srv.mu.Lock()
	srv.hold = make(chan struct{})
	hold := srv.hold
	srv.mu.Unlock()
	a.quotaFetch(cache, e, cr, true)
	cr.Raw = claudeRecorded(at.Add(time.Minute), 81)
	a.quotaEntryFor(snapshot, cr, time.Now())
	close(hold)
	deadline := time.Now().Add(5 * time.Second)
	for e.fetching && time.Now().Before(deadline) {
		queue.drain()
		time.Sleep(10 * time.Millisecond)
	}
	short, _ := e.res.Summary()
	if e.fetching || !e.fromCPA || short == nil || percent(short.Remaining) != 19 {
		t.Fatalf("manual response replaced extended observation: %+v", e)
	}
}

func TestClaudePassiveMalformedExtensionIsVisible(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	at := time.Now().UTC()
	raw := strings.Replace(string(claudeRecorded(at, 42)), `"utilization":42`, `"utilization":"invalid"`, 1)
	setClaudeRecorded(srv, json.RawMessage(raw))
	a.Svc.Refresh(id)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, queue, "malformed extended Claude quota", func() bool { return strings.Contains(strings.Join(tt.Texts(), "\n"), "读取额度失败") })
	if tt.HasText("剩余 47%") || tt.HasText("剩余 100%") {
		t.Fatalf("malformed extension silently fell back: %q", tt.Texts())
	}
	if upstream.count("GET") != 0 {
		t.Fatal("malformed extension queried upstream")
	}
}

func TestClaudePassiveMetadataPollKeepsManualResult(t *testing.T) {
	a, id, srv, _, queue := passiveApp(t)
	at := time.Now().UTC().Add(-time.Hour)
	raw := claudeRecorded(at, 42)
	setClaudeRecorded(srv, raw)
	a.Svc.Refresh(id)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, tt, queue, "extended usage", func() bool { return tt.HasText("剩余 58%") })
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, queue, "manual usage", func() bool { return tt.HasText("剩余 95%") })
	raw = json.RawMessage(strings.ReplaceAll(string(raw), `"usage_age_seconds":10`, `"usage_age_seconds":90`))
	setClaudeRecorded(srv, raw)
	a.Svc.Refresh(id)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, _ := a.Svc.Snapshot(id)
		if strings.Contains(string(snapshot.Credentials[0].Raw), `"usage_age_seconds":90`) {
			break
		}
		queue.drain()
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := a.Svc.Snapshot(id)
	if !strings.Contains(string(snapshot.Credentials[0].Raw), `"usage_age_seconds":90`) {
		t.Fatal("metadata poll did not complete")
	}
	tt.Frame()
	queue.drain()
	if !tt.HasText("剩余 95%") {
		t.Fatalf("metadata poll replaced manual result: %q", tt.Texts())
	}
	setClaudeRecorded(srv, claudeRecorded(at.Add(time.Minute), 81))
	a.Svc.Refresh(id)
	waitFor(t, tt, queue, "new usage", func() bool { return tt.HasText("剩余 19%") })
}
