package quota

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

var allocationTime = time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)

func allocationFixture(t *testing.T, edits func(map[string]any, map[string]any)) cpa.Credential {
	t.Helper()
	stamp := allocationTime.Format(time.RFC3339)
	window := func(kind string, hours int) map[string]any {
		return map[string]any{"kind": kind, "observed_at": stamp, "used_percent": 0,
			"resets_at": allocationTime.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)}
	}
	sub := map[string]any{"enabled": true, "strategy": "quota-aware-weighted", "evaluated_at": stamp,
		"observed_at": stamp, "complete_at": stamp, "complete": true,
		"observation_max_age_seconds": 200000, "reserve": map[string]any{},
		"windows": []any{window("five_hour", 5), window("seven_day", 100)}}
	root := map[string]any{"provider": "claude", "claude_subscription": sub, "cooldowns": []any{}}
	if edits != nil {
		edits(root, sub)
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var cr cpa.Credential
	if err := json.Unmarshal(data, &cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func allocationWindow(sub map[string]any, index int) map[string]any {
	return sub["windows"].([]any)[index].(map[string]any)
}

func allocationShare(t *testing.T, rows []ClaudeTargetAllocation, scope string, want *float64) {
	t.Helper()
	for _, row := range rows {
		if row.Scope != scope {
			continue
		}
		if want == nil {
			if row.Share != nil || row.Err == nil {
				t.Fatalf("want unknown with reason, got %+v", row)
			}
		} else if row.Share == nil || row.Err != nil || math.Abs(*row.Share-*want) > 1e-8 {
			t.Fatalf("scope %q: want %v, got %+v", scope, *want, row)
		}
		return
	}
	t.Fatalf("missing scope %q: %+v", scope, rows)
}

func allocationPercent(value float64) *float64 { return &value }

func TestClaudeTargetAllocationCompatibility(t *testing.T) {
	unsupported := []func(map[string]any, map[string]any){
		func(root, _ map[string]any) { delete(root, "claude_subscription") },
		func(root, _ map[string]any) { root["provider"] = "codex" },
		func(_, sub map[string]any) { sub["enabled"] = false },
		func(_, sub map[string]any) { sub["strategy"] = "quota-aware" },
		func(_, sub map[string]any) { delete(sub, "enabled") },
	}
	var credentials []cpa.Credential
	for _, edit := range unsupported {
		credentials = append(credentials, allocationFixture(t, edit))
	}
	credentials = append(credentials, allocationFixture(t, func(_, sub map[string]any) {
		sub["strategy"] = " Quota-Aware-Weighted "
	}))
	rows := ClaudeTargetAllocations(credentials, allocationTime, allocationTime)
	for i := range unsupported {
		if rows[i] != nil {
			t.Fatalf("unsupported credential %d exposed target: %+v", i, rows[i])
		}
	}
	allocationShare(t, rows[len(rows)-1], "", allocationPercent(100))
}

func TestClaudeTargetAllocationPoolPriorityAndWeights(t *testing.T) {
	credentials := []cpa.Credential{
		allocationFixture(t, func(root, sub map[string]any) {
			root["weight"] = 99
			sub["evaluation"] = map[string]any{"score": 99999}
		}),
		allocationFixture(t, func(root, sub map[string]any) {
			root["weight"] = 1
			allocationWindow(sub, 1)["used_percent"] = 50
		}),
		allocationFixture(t, func(root, _ map[string]any) { root["priority"], root["weight"] = 9, 0 }),
	}
	rows := ClaudeTargetAllocations(credentials, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", allocationPercent(200.0/3))
	allocationShare(t, rows[1], "", allocationPercent(100.0/3))
	allocationShare(t, rows[2], "", allocationPercent(0))
	credentials[1].Priority = allocationPercent(1)
	rows = ClaudeTargetAllocations(credentials, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", allocationPercent(0))
	allocationShare(t, rows[1], "", allocationPercent(100))
}

func TestClaudeTargetAllocationTimeAnchor(t *testing.T) {
	credentials := []cpa.Credential{allocationFixture(t, nil), allocationFixture(t, func(_, sub map[string]any) {
		allocationWindow(sub, 0)["inactive"] = true
		allocationWindow(sub, 1)["resets_at"] = allocationTime.Add(50 * time.Hour).Format(time.RFC3339)
	})}
	// Both short windows must stay inactive so their reset cannot gate weekly budgets.
	credentials[0] = allocationFixture(t, func(_, sub map[string]any) { allocationWindow(sub, 0)["inactive"] = true })
	local := allocationTime.Add(-300 * time.Hour)
	rows := ClaudeTargetAllocations(credentials, local, local)
	allocationShare(t, rows[0], "", allocationPercent(100.0/3))
	rows = ClaudeTargetAllocations(credentials, local, local.Add(25*time.Hour))
	allocationShare(t, rows[0], "", allocationPercent(25))
	rows = ClaudeTargetAllocations(credentials, local, local.Add(-time.Hour))
	allocationShare(t, rows[0], "", allocationPercent(100.0/3))
}

func TestClaudeTargetAllocationUnknownAndZero(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any, map[string]any)
		zero bool
	}{
		{"missing windows", func(_, sub map[string]any) { delete(sub, "windows") }, false},
		{"missing evaluated time", func(_, sub map[string]any) { delete(sub, "evaluated_at") }, false},
		{"epoch evaluated time", func(_, sub map[string]any) { sub["evaluated_at"] = "1970-01-01T00:00:00Z" }, false},
		{"epoch observed time", func(_, sub map[string]any) { allocationWindow(sub, 1)["observed_at"] = "1970-01-01T00:00:00Z" }, false},
		{"epoch reset time", func(_, sub map[string]any) { allocationWindow(sub, 1)["resets_at"] = "1970-01-01T00:00:00Z" }, false},
		{"invalid time", func(_, sub map[string]any) { sub["evaluated_at"] = "bad" }, false},
		{"invalid usage", func(_, sub map[string]any) { allocationWindow(sub, 1)["used_percent"] = "bad" }, false},
		{"missing usage", func(_, sub map[string]any) { delete(allocationWindow(sub, 1), "used_percent") }, false},
		{"invalid exponent", func(_, sub map[string]any) { sub["weighted_exponent"] = 6 }, false},
		{"stale", func(_, sub map[string]any) {
			allocationWindow(sub, 1)["observed_at"] = allocationTime.Add(-200000 * time.Second).Format(time.RFC3339)
		}, false},
		{"invalid reserve", func(_, sub map[string]any) { allocationWindow(sub, 1)["reserve_percent"] = 101 }, false},
		{"unknown jitter", func(_, sub map[string]any) { sub["reserve"] = map[string]any{"seven_day_jitter_percent": 5} }, false},
		{"unavailable without report", func(root, _ map[string]any) { root["unavailable"] = true; delete(root, "cooldowns") }, false},
		{"disabled", func(root, _ map[string]any) { root["disabled"] = true }, true},
		{"exhausted", func(_, sub map[string]any) { allocationWindow(sub, 1)["used_percent"] = 100 }, true},
		{"reset", func(_, sub map[string]any) {
			allocationWindow(sub, 1)["resets_at"] = allocationTime.Format(time.RFC3339)
		}, true},
		{"reserve reached", func(_, sub map[string]any) {
			allocationWindow(sub, 1)["used_percent"] = 90
			allocationWindow(sub, 1)["reserve_percent"] = 10
		}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rows := ClaudeTargetAllocations([]cpa.Credential{allocationFixture(t, test.edit), allocationFixture(t, nil)}, allocationTime, allocationTime)
			var want *float64
			if test.zero {
				want = allocationPercent(0)
			}
			allocationShare(t, rows[0], "", want)
			// Unknown candidates stay unknown without becoming denominator weight.
			allocationShare(t, rows[1], "", allocationPercent(100))
		})
	}
}

func TestClaudeTargetAllocationCompleteFiveHourFallback(t *testing.T) {
	for _, complete := range []bool{false, true} {
		cr := allocationFixture(t, func(_, sub map[string]any) {
			sub["complete"] = complete
			sub["complete_at"] = "1970-01-01T00:00:00Z" // Invalid full time falls back to observed_at.
			sub["windows"] = sub["windows"].([]any)[:1]
			allocationWindow(sub, 0)["inactive"] = true
			sub["weighted_exponent"] = 0 // Upstream's zero setting means exponent one.
		})
		var want *float64
		if complete {
			want = allocationPercent(100)
		}
		rows := ClaudeTargetAllocations([]cpa.Credential{cr}, allocationTime, allocationTime)
		allocationShare(t, rows[0], "", want)
	}
	// A stale full observation cannot prove that an absent weekly window is inactive.
	cr := allocationFixture(t, func(_, sub map[string]any) {
		sub["complete_at"] = allocationTime.Add(-200000 * time.Second).Format(time.RFC3339)
		sub["windows"] = sub["windows"].([]any)[:1]
	})
	rows := ClaudeTargetAllocations([]cpa.Credential{cr}, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", nil)
}

func TestClaudeTargetAllocationModelsAndCooldown(t *testing.T) {
	modelAccount := allocationFixture(t, func(_, sub map[string]any) {
		window := map[string]any{"kind": "seven_day", "model": "sonnet", "used_percent": 75,
			"observed_at": allocationTime.Format(time.RFC3339), "resets_at": allocationTime.Add(100 * time.Hour).Format(time.RFC3339)}
		sub["windows"] = append(sub["windows"].([]any), window)
	})
	rows := ClaudeTargetAllocations([]cpa.Credential{modelAccount, allocationFixture(t, nil)}, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", allocationPercent(50))
	allocationShare(t, rows[0], "sonnet", allocationPercent(20))
	allocationShare(t, rows[1], "sonnet", allocationPercent(80))
	cooling := allocationFixture(t, func(root, _ map[string]any) {
		root["cooldowns"] = []any{map[string]any{"scope": "credential", "remaining_seconds": 60}}
	})
	credentials := []cpa.Credential{cooling, allocationFixture(t, nil)}
	rows = ClaudeTargetAllocations(credentials, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", allocationPercent(0))
	rows = ClaudeTargetAllocations(credentials, allocationTime, allocationTime.Add(time.Minute))
	allocationShare(t, rows[0], "", allocationPercent(50))
	modelCooling := allocationFixture(t, func(root, _ map[string]any) {
		root["cooldowns"] = []any{map[string]any{"scope": "model", "model_key": "sonnet", "remaining_seconds": 60}}
	})
	rows = ClaudeTargetAllocations([]cpa.Credential{modelAccount, modelCooling}, allocationTime, allocationTime)
	allocationShare(t, rows[1], "", allocationPercent(50))
	allocationShare(t, rows[1], "sonnet", allocationPercent(0))
}

func TestClaudeTargetAllocationReserveExponentAndFiveHour(t *testing.T) {
	a := allocationFixture(t, func(_, sub map[string]any) {
		allocationWindow(sub, 0)["used_percent"] = 50
		allocationWindow(sub, 0)["reserve_percent"] = 20
		allocationWindow(sub, 1)["reserve_percent"] = 50
		sub["reserve"] = map[string]any{"five_hour_jitter_percent": 5, "seven_day_jitter_percent": 5}
		sub["five_hour_weighting"], sub["weighted_exponent"] = true, 2
	})
	// A: weekly score .5 squared, short factor 30/80 -> .09375; B: 1.
	rows := ClaudeTargetAllocations([]cpa.Credential{a, allocationFixture(t, nil)}, allocationTime, allocationTime)
	allocationShare(t, rows[0], "", allocationPercent(.09375/1.09375*100))
	if len(rows[0]) != 1 {
		t.Fatalf("redundant model scopes: %+v", rows)
	}
}
