package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/quota"
)

func TestClaudeMetadataAbsentFromAccountsAndPanel(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	_, legacyAt, err := quota.PassiveObservationMeta(snapshot.Credentials[0])
	if err != nil || legacyAt.IsZero() {
		t.Fatalf("legacy observation: %v %v", legacyAt, err)
	}
	at := legacyAt.Add(time.Minute)
	raw := strings.Replace(string(claudeRecorded(at, 42)), `"usage":{`, `"usage":{"seven_day_breakdown":{"sonnet":{"utilization":10}},"five_hour_metadata":{"models":["sonnet"]},"seven_day_unknown":{"percent":null,"resets_at":"2026-11-19T09:00:00Z"},`, 1)
	setClaudeRecorded(srv, json.RawMessage(raw))
	a.Svc.Refresh(id)
	a.page = "accounts"
	accounts := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, accounts, queue, "Claude windows without metadata", func() bool { return accounts.HasText("7 天 Fable 5") })
	panel := ui.NewTester(a.panelView, panelWidth, 1800)
	panel.Frame()
	for name, screen := range map[string]*ui.Tester{"accounts": accounts, "panel": panel} {
		assertClaudeTimingSource(t, screen, at)
		text := strings.Join(screen.Texts(), "\n")
		for _, want := range []string{"5 小时限额", "7 天限额", "7 天 Fable 5", "7 天 unknown", "—", "剩余重置次数 3", "最后更新时间", at.Local().Format("01/02 15:04:05"), time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC).Local().Format("01/02 15:04")} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q: %s", name, want, text)
			}
		}
		for _, unwanted := range []string{"breakdown", "metadata", "读取额度失败"} {
			if strings.Contains(text, unwanted) {
				t.Errorf("%s rendered %q: %s", name, unwanted, text)
			}
		}
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		for name, screen := range map[string]*ui.Tester{"accounts": ui.NewTester(a.managerView, managerWidth, managerHeight), "panel": ui.NewTester(a.panelView, panelWidth, panelHeight)} {
			saveSnapshot(t, filepath.Join(dir, "claude-metadata-"+name+".png"), screen)
		}
	}
	if upstream.count("GET") != 0 {
		t.Fatal("metadata display queried upstream")
	}
}
