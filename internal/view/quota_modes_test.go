package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestPassiveQuotaIndependentModes(t *testing.T) {
	for _, modes := range []struct{ global, claude bool }{
		{false, false}, {false, true}, {true, false}, {true, true},
	} {
		global, claude := modes.global, modes.claude
		t.Run(fmt.Sprintf("global=%t/claude=%t", global, claude), func(t *testing.T) {
			a, id, srv, upstream, queue := passiveApp(t)
			t.Cleanup(a.cancelQuotaWake)
			if err := a.Svc.UpdateSettings(func(s *store.Settings) {
				s.PassiveQuota, s.ClaudePassiveQuota = global, claude
			}); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := a.Svc.Snapshot(id)
			cr := snapshot.Credentials[0]
			other := cpa.Credential{AuthIndex: "2", Provider: "custom", SupportsQuota: true, QuotaProvider: "custom"}
			var e, custom *quotaEntry
			tt := ui.NewTester(func(c *ui.Context) {
				e, _, _ = a.quotaEntryFor(snapshot, cr, time.Now())
				custom, _, _ = a.quotaEntryFor(snapshot, other, time.Now())
			}, managerWidth, managerHeight)
			waitFor(t, tt, queue, "both quota modes", func() bool {
				return e.res != nil && !e.fetching && (global || custom.res != nil) && !custom.fetching
			})
			if e.fromCPA != claude || custom.fromCPA != global {
				t.Fatalf("sources: Claude CPA=%t, custom CPA=%t", e.fromCPA, custom.fromCPA)
			}
			wantReads := 3
			if claude {
				wantReads = 0
			}
			if n := upstream.count("GET"); n != wantReads {
				t.Fatalf("Claude upstream reads=%d, want %d", n, wantReads)
			}
			assertPassivePluginRequests(t, srv, global)
			_, tips := a.trayQuota(2, time.Now())
			if len(tips) != 1 || strings.Contains(tips[0], "CPA 观测") != claude {
				t.Fatalf("Claude tray source follows global instead of independent mode: %q", tips)
			}
		})
	}
}

func TestPassiveQuotaProviderNormalization(t *testing.T) {
	s := store.Settings{PassiveQuota: false, ClaudePassiveQuota: true}
	for _, cr := range []cpa.Credential{
		{Provider: "claude"}, {Provider: " ClAuDe "}, {Type: " CLAUDE "},
	} {
		if !passiveQuota(s, cr) {
			t.Fatalf("Claude mode ignored provider name: %+v", cr)
		}
	}
	if passiveQuota(s, cpa.Credential{Provider: "codex", Type: "claude"}) {
		t.Fatal("non-Claude account inherited Claude's mode")
	}
}

func assertPassivePluginRequests(t *testing.T, srv *passiveCPA, passive bool) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	plugins, reads := 0, 0
	for _, request := range srv.requests {
		switch request {
		case "GET /v8/management/plugins":
			plugins++
		case "GET /v8/management/plugins/custom/quota":
			reads++
		}
	}
	want := 1
	if passive {
		want = 0
	}
	if plugins != want || reads != want {
		t.Fatalf("custom discovery=%d, reads=%d, want %d each", plugins, reads, want)
	}
}
