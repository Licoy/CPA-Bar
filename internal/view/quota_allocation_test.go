package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestClaudeAllocationLabelAndScopes(t *testing.T) {
	previous := lang.Load().(string)
	t.Cleanup(func() { setResolvedLanguage(previous) })
	general, model := 25.0, 75.0
	for _, language := range []string{store.LanguageZH, store.LanguageEN} {
		setResolvedLanguage(language)
		rows := []quota.ClaudeTargetAllocation{{Share: &general}, {Scope: "sonnet", Share: &model}, {Scope: "opus", Err: errors.New("missing model reserve")}}
		screen := ui.NewTester(func(c *ui.Context) { applyTheme(c); allocationView(c, rows) }, 420, 200)
		label := T("分配 25.00%", "Allocation 25.00%")
		r, ok := screen.Find(label)
		if !ok || screen.HasText("2500.00%") {
			t.Fatalf("wrong percentage: %q", screen.Texts())
		}
		screen.Move(r.X+r.W/2, r.Y+r.H/2)
		screen.Advance(time.Second)
		texts := strings.Join(screen.Texts(), "\n")
		for _, want := range []string{T("实时目标分配占比", "Live target allocation share"), T("通用", "General") + " · 25.00%", "sonnet · 75.00%", "opus · " + T("未知", "Unknown"), "missing model reserve"} {
			if !strings.Contains(texts, want) {
				t.Errorf("%s tooltip lacks %q: %s", language, want, texts)
			}
		}
		unknown := ui.NewTester(func(c *ui.Context) {
			applyTheme(c)
			allocationView(c, []quota.ClaudeTargetAllocation{{Err: errors.New("missing windows")}})
		}, 420, 100)
		if !unknown.HasText(T("分配 未知", "Allocation Unknown")) || unknown.HasText("0.00%") {
			t.Fatal(unknown.Texts())
		}
		legacy := ui.NewTester(func(c *ui.Context) { applyTheme(c); allocationView(c, nil) }, 420, 100)
		if len(legacy.Texts()) != 0 {
			t.Fatal("unsupported credential has allocation", legacy.Texts())
		}
	}
}

func TestClaudeTimingMissingAndManualSource(t *testing.T) {
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	age := 90.0
	previous := lang.Load().(string)
	t.Cleanup(func() { setResolvedLanguage(previous) })
	for _, language := range []string{store.LanguageZH, store.LanguageEN} {
		setResolvedLanguage(language)
		entry := &quotaEntry{fromCPA: true, at: at, timing: quotaTiming{age: &age}}
		screen := ui.NewTester(func(c *ui.Context) { applyTheme(c); entry.timingView(c) }, 370, 100)
		if !screen.HasText(T("下次可查询时间", "Next query allowed")) || !screen.HasText(T("未知", "Unknown")) {
			t.Fatal("missing next poll fabricated or hidden", screen.Texts())
		}
		entry.fromCPA = false
		screen.Frame()
		if !screen.HasText(T("手动查询 · ", "Manual query · ")+at.Local().Format("01/02 15:04:05")) || screen.HasText(T("缓存年龄", "Cache age")) || screen.HasText(T("下次可查询时间", "Next query allowed")) {
			t.Fatal("manual result mixed fork timing", screen.Texts())
		}
		entry.fromCPA, entry.timing = true, quotaTiming{}
		screen.Frame()
		if !screen.HasText(entry.sourceText()) || screen.HasText(T("缓存年龄", "Cache age")) || screen.HasText(T("下次可查询时间", "Next query allowed")) {
			t.Fatal("original CPA has fork timing", screen.Texts())
		}
	}
}

func TestClaudeAllocationAccountsAndPanelUseFullPool(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	at := time.Now().UTC()
	var files []any
	longEmail := "a-very-long-account-email-address@example.com"
	for i, used := range []int{75, 25} {
		var record map[string]any
		if err := json.Unmarshal(claudeTimingRecorded(at, "90", fmt.Sprintf("%q", at.Add(time.Minute).Format(time.RFC3339Nano))), &record); err != nil {
			t.Fatal(err)
		}
		sub := record["claude_subscription"].(map[string]any)
		sub["enabled"], sub["strategy"], sub["evaluated_at"], sub["observation_max_age_seconds"] = true, "quota-aware-weighted", at.Format(time.RFC3339Nano), 5
		sub["windows"] = []any{
			map[string]any{"kind": "five_hour", "used_percent": 0, "resets_at": at.Add(5 * time.Hour).Format(time.RFC3339Nano), "observed_at": at.Format(time.RFC3339Nano)},
			map[string]any{"kind": "seven_day", "used_percent": used, "resets_at": at.Add(100 * time.Hour).Format(time.RFC3339Nano), "observed_at": at.Format(time.RFC3339Nano)},
		}
		record["id"], record["name"], record["auth_index"], record["provider"], record["status"] = fmt.Sprint(i), fmt.Sprint(i)+".json", fmt.Sprint(i), "claude", "active"
		record["email"] = "second@example.com"
		if i == 0 {
			record["email"] = longEmail
		}
		files = append(files, record)
	}
	files = append(files, map[string]any{"id": "original", "name": "original.json", "auth_index": "original", "provider": "claude", "email": "original@example.com", "status": "active", "quota": json.RawMessage(passiveObservation(at, 53))})
	body, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.credentials = string(body)
	srv.mu.Unlock()
	a.Svc.Refresh(id)
	a.page = "accounts"
	accounts := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, accounts, queue, "Claude target allocation", func() bool { return accounts.HasText("分配 25.00%") })
	previous := lang.Load().(string)
	t.Cleanup(func() { setResolvedLanguage(previous) })
	srv.mu.Lock()
	requests := len(srv.requests)
	srv.mu.Unlock()
	for _, language := range []string{store.LanguageZH, store.LanguageEN} {
		setResolvedLanguage(language)
		accounts.Frame()
		a.panelLists = nil
		panel := ui.NewTester(a.panelView, panelWidth, 1800)
		for name, screen := range map[string]*ui.Tester{"accounts": accounts, "panel": panel} {
			if !screen.HasText(T("分配 25.00%", "Allocation 25.00%")) || !screen.HasText(T("分配 75.00%", "Allocation 75.00%")) {
				t.Fatalf("%s/%s wrong denominator: %q", language, name, screen.Texts())
			}
			allocations := 0
			for _, text := range screen.Texts() {
				if strings.HasPrefix(text, T("分配 ", "Allocation ")) {
					allocations++
				}
			}
			if allocations != 2 {
				t.Fatalf("%s original CPA has fork allocation: %q", name, screen.Texts())
			}
			share, _ := screen.Find(T("分配 25.00%", "Allocation 25.00%"))
			label := T("启用", "Enabled")
			if name == "panel" {
				label = fmt.Sprintf(T("启用 %s", "Enable %s"), longEmail)
			}
			toggle, ok := screen.Find(label)
			if !ok || toggle.X < share.X+share.W || share.Y+share.H < toggle.Y || toggle.Y+toggle.H < share.Y {
				t.Errorf("%s/%s allocation not before switch: share=%+v switch=%+v found=%v", language, name, share, toggle, ok)
			}
		}
		if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
			preview := NewApp(a.Svc)
			preview.selected, preview.panelInstance, preview.page = id, id, "accounts"
			saveSnapshot(t, filepath.Join(dir, "claude-allocation-panel-"+language+".png"), ui.NewTester(preview.panelView, panelWidth, panelHeight))
			saveSnapshot(t, filepath.Join(dir, "claude-allocation-accounts-"+language+".png"), ui.NewTester(preview.managerView, managerWidth, managerHeight))
		}
	}
	setResolvedLanguage(store.LanguageZH)
	st := StateFor(a, "accounts", id, newAccountsState)
	st.search = longEmail
	accounts.Frame()
	if !accounts.HasText("分配 25.00%") || accounts.HasText("分配 100.00%") || accounts.HasText("second@example.com") {
		t.Fatal("filtered list changed pool denominator", accounts.Texts())
	}
	accounts.Advance(5 * time.Second)
	if !accounts.HasText("分配 未知") || accounts.HasText("分配 25.00%") {
		t.Fatal("local allocation clock did not expire observations", accounts.Texts())
	}
	srv.mu.Lock()
	after := len(srv.requests)
	srv.mu.Unlock()
	if after != requests || upstream.count("GET") != 0 {
		t.Fatalf("display refresh made requests: before=%d after=%d upstream=%d", requests, after, upstream.count("GET"))
	}
}
