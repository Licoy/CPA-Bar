package view

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/usage"
)

func withUsage(t *testing.T, a *App, id string, recs ...usage.Record) {
	t.Helper()
	a.Usage = usage.New(usage.Options{Dir: t.TempDir()})
	t.Cleanup(a.Usage.Close)
	if err := a.Usage.Add(id, recs...); err != nil {
		t.Fatal(err)
	}
}

func usageRecords(now time.Time) []usage.Record {
	return []usage.Record{
		{Time: now.Add(-2 * time.Hour), RequestID: "r1", AuthIndex: "1", Model: "claude-sonnet-4-5", Tokens: usage.Tokens{Input: 1000, Output: 200, Total: 1200}},
		{Time: now.Add(-time.Hour), RequestID: "r2", AuthIndex: "1", Model: "claude-sonnet-4-5", Failed: true, Status: 429, Tokens: usage.Tokens{Total: 300}},
		{Time: now.Add(-48 * time.Hour), RequestID: "old", AuthIndex: "1", Tokens: usage.Tokens{Total: 99999}},
	}
}

// The panel and the accounts show the last day of usage; older records
// are left out.
func TestUsageSummaries(t *testing.T) {
	a, id := newTestApp(t, true)
	withUsage(t, a, id, usageRecords(time.Now())...)

	a.panelInstance = id
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	if !tt.HasText("24 小时：2 次请求 · 1.5K Token · 成功率 50%") {
		t.Errorf("panel lacks the usage line; texts: %q", tt.Texts())
	}

	a.page = "accounts"
	tm := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tm.HasText("24 小时：2 次请求 · 1.5K Token · 1 次失败") {
		t.Errorf("account lacks its usage; texts: %q", tm.Texts())
	}

	a.page = "overview"
	tm.Frame()
	if !tm.HasText("用量（24 小时）") || !tm.HasText("1.5K") {
		t.Errorf("overview lacks the usage card; texts: %q", tm.Texts())
	}
	if err := tm.Click("查看用量"); err != nil {
		t.Fatal(err)
	}
	tm.Frame()
	if a.page != "usage" {
		t.Errorf("page %q after 查看用量", a.page)
	}
}

func TestCompactNum(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 950: "950", 1000: "1K", 1260: "1.3K", 3_400_000: "3.4M", 1_100_000_000: "1.1B"} {
		if got := compactNum(n); got != want {
			t.Errorf("compactNum(%d) = %q, want %q", n, got, want)
		}
	}
}
