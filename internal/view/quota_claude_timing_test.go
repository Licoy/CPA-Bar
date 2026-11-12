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
	"github.com/licoy/cpa-bar/internal/store"
)

func claudeTimingRecorded(at time.Time, age, next string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"quota":%s,"claude_subscription":{"usage_observed_at":%q,"observed_at":%q,"usage_age_seconds":%s,"next_poll_at":%s,"usage":{
	"five_hour":{"utilization":10,"limit_dollars":null,"used_dollars":null,"remaining_dollars":null},
	"seven_day":{"utilization":20,"limit_dollars":null,"used_dollars":null,"remaining_dollars":null},
	"limits":[{"kind":"session","percent":45},{"kind":"weekly_all","percent":17},
	{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"percent":20,"is_active":true}],
	"extra_usage":{"is_enabled":true,"used_credits":1250,"monthly_limit":5000,"currency":null}}}}`,
		passiveObservation(at.Add(time.Hour), 53), at.Format(time.RFC3339Nano), at.Add(time.Hour).Format(time.RFC3339Nano), age, next))
}

func TestClaudeTimingDisplaysOnAccountsAndPanel(t *testing.T) {
	a, id, srv, upstream, queue := passiveApp(t)
	snapshot, _ := a.Svc.Snapshot(id)
	_, legacyAt, err := quota.PassiveObservationMeta(snapshot.Credentials[0])
	if err != nil {
		t.Fatal(err)
	}
	at := legacyAt.Add(time.Minute)
	next := at.Add(5 * time.Minute)
	setClaudeRecorded(srv, claudeTimingRecorded(at, "90", fmt.Sprintf("%q", next.Format(time.RFC3339Nano))))
	a.Svc.Refresh(id)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, 1800)
	waitFor(t, tt, queue, "Claude timing", func() bool { return tt.HasText("CPA 缓存年龄 · 90 秒") })
	previous := lang.Load().(string)
	t.Cleanup(func() { setResolvedLanguage(previous) })
	for _, language := range []string{store.LanguageZH, store.LanguageEN} {
		setResolvedLanguage(language)
		tt.Frame()
		panel := ui.NewTester(a.panelView, panelWidth, 1800)
		panel.Frame()
		for name, screen := range map[string]*ui.Tester{"accounts": tt, "panel": panel} {
			for _, want := range []string{
				T("CPA 观测 · ", "CPA observation · ") + at.Local().Format("01/02 15:04:05"),
				T("CPA 缓存年龄 · 90 秒", "CPA cache age · 90 seconds"),
				T("下次可查询 · ", "Next query allowed · ") + next.Local().Format("01/02 15:04:05"),
			} {
				if !screen.HasText(want) {
					t.Errorf("%s/%s lacks %q: %q", language, name, want, screen.Texts())
				}
			}
			if screen.HasText(T("CPA 观测 · ", "CPA observation · ") + at.Add(time.Hour).Local().Format("01/02 15:04:05")) {
				t.Errorf("%s/%s substituted header time for usage query time", language, name)
			}
			for _, label := range []string{T("5 小时限额", "5-hour limit"), T("7 天限额", "7-day limit"), T("7 天 Fable 5", "7-day Fable 5")} {
				count := 0
				for _, text := range screen.Texts() {
					if text == label {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s/%s has %d %q labels, want one", language, name, count, label)
				}
			}
			if screen.HasText("five_hour") || screen.HasText("seven_day") || !screen.HasText("$12.50 / $50.00") {
				t.Errorf("%s/%s has raw aliases or lost nullable-currency extra usage: %q", language, name, screen.Texts())
			}
		}
		if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
			normal := ui.NewTester(a.panelView, panelWidth, panelHeight)
			normal.Frame()
			saveSnapshot(t, filepath.Join(dir, "claude-timing-panel-"+language+".png"), normal)
			accounts := ui.NewTester(a.managerView, managerWidth, managerHeight)
			accounts.Frame()
			saveSnapshot(t, filepath.Join(dir, "claude-timing-accounts-"+language+".png"), accounts)
		}
	}
	if upstream.count("GET") != 0 {
		t.Fatal("timing display queried upstream")
	}
}

func TestClaudeTimingMetadataUpdatesPreserveQuota(t *testing.T) {
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	cr := cpa.Credential{Provider: "claude", Raw: claudeTimingRecorded(at, "10", `"2026-11-12T00:05:00Z"`)}
	e := &quotaEntry{}
	e.observe(cr)
	result, observed, observation := e.res, e.at, e.observation
	cr.Raw = claudeTimingRecorded(at, "90", `"2026-11-12T00:10:00Z"`)
	e.observe(cr)
	if e.res != result || !e.at.Equal(observed) || e.observation != observation || !e.fromCPA {
		t.Fatal("metadata update changed quota provenance")
	}
	if *e.timing.age != 90 || e.timing.next.Minute() != 10 {
		t.Fatalf("metadata update ignored: %+v", e.timing)
	}
	manual := &quota.Result{Plan: "manual"}
	manualAt := at.Add(24 * time.Hour)
	e.res, e.at, e.fromCPA = manual, manualAt, false
	cr.Raw = claudeTimingRecorded(at, "120", `"2026-11-12T00:15:00Z"`)
	e.observe(cr)
	if e.res != manual || e.fromCPA || !e.at.Equal(manualAt) || e.observation != observation {
		t.Fatal("metadata update replaced manual query")
	}
	if *e.timing.age != 120 || e.timing.next.Minute() != 15 || len(e.timingTexts()) != 0 {
		t.Fatalf("manual query mixed CPA timing: %+v", e)
	}
	cr.Raw = claudeTimingRecorded(at.Add(-time.Minute), "999", `"2026-11-12T01:00:00Z"`)
	e.observe(cr)
	if *e.timing.age != 120 || e.timing.next.Minute() != 15 || e.res != manual {
		t.Fatal("old usage updated metadata or manual result")
	}
}

func TestClaudeTimingOptionalAndMalformedFields(t *testing.T) {
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, age, next string
		invalid         bool
	}{
		{"null", "null", "null", false},
		{"epoch", "null", `"1970-01-01T00:00:00Z"`, false},
		{"zero", "null", `"0001-01-01T00:00:00Z"`, false},
		{"age-type", `"90"`, "null", true},
		{"negative-age", "-1", "null", true},
		{"next-type", "null", "90", true},
		{"next-format", "null", `"tomorrow"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &quotaEntry{}
			e.observe(cpa.Credential{Provider: "claude", Raw: claudeTimingRecorded(at, tc.age, tc.next)})
			if e.res == nil || e.err != nil {
				t.Fatalf("timing replaced valid quota: %+v", e)
			}
			if (e.timing.err != nil) != tc.invalid {
				t.Fatalf("unexpected timing error: %v", e.timing.err)
			}
			if tc.invalid && len(e.timingTexts()) == 0 {
				t.Fatal("invalid timing was hidden")
			}
			if !tc.invalid && len(e.timingTexts()) != 0 {
				t.Fatalf("unknown timing fabricated: %q", e.timingTexts())
			}
		})
	}
	legacy := &quotaEntry{}
	legacy.observe(cpa.Credential{Provider: "claude", Raw: json.RawMessage(`{"quota":` + passiveObservation(at, 53) + `}`)})
	if legacy.res == nil || legacy.err != nil || len(legacy.timingTexts()) != 0 || !strings.Contains(legacy.sourceText(), at.Local().Format("01/02 15:04:05")) {
		t.Fatalf("legacy timing changed: %+v", legacy)
	}
}
