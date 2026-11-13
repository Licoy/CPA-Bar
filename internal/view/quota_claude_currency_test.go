package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/quota"
)

func TestClaudePassiveNullableCurrencyDisplay(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	_, legacyAt, err := quota.PassiveObservationMeta(snapshot.Credentials[0])
	if err != nil || legacyAt.IsZero() {
		t.Fatalf("initial legacy observation: %v, %v", legacyAt, err)
	}
	at := legacyAt.Add(time.Minute)
	raw := strings.Replace(string(claudeRecorded(at, 42)), `"extra_usage":{`, `"extra_usage":{"currency":null,`, 1)
	setClaudeRecorded(srv, json.RawMessage(raw))
	a.Svc.Refresh(id)
	a.page = "accounts"
	accounts := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, accounts, queue, "Claude quota with nullable currency", func() bool { return accounts.HasText("7 天 Fable 5") })
	panel := ui.NewTester(a.panelView, panelWidth, 1800)
	panel.Frame()
	queue.drain()
	for name, screen := range map[string]*ui.Tester{"accounts": accounts, "panel": panel} {
		assertClaudeTimingSource(t, screen, at)
		text := strings.Join(screen.Texts(), "\n")
		for _, want := range []string{"5 小时限额", "7 天限额", "7 天 Fable 5", "$12.50 / $50.00", "额外用量已启用", "剩余重置次数 3", "最后更新时间", at.Local().Format("01/02 15:04:05")} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q: %q", name, want, screen.Texts())
			}
		}
		if strings.Contains(text, "读取额度失败") {
			t.Errorf("%s rejected nullable currency: %s", name, text)
		}
	}
	if upstream.count("GET") != 0 {
		t.Fatal("nullable currency queried upstream")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, request := range srv.requests {
		if strings.Contains(request, "api-call") || strings.Contains(request, "plugins") {
			t.Errorf("passive request: %s", request)
		}
	}
}
