package quota

import (
	"encoding/json"
	"testing"
)

func TestClaudeRecordedServerCompatibleFields(t *testing.T) {
	for _, tc := range []struct {
		name, usage, label, amount string
		remaining                  float64
		known, reset, invalid      bool
	}{
		{name: "flat percent camel reset", usage: `{"five_hour":{"percent":25,"resetsAt":"2026-11-18T00:00:00Z"}}`, label: "5-hour limit", remaining: .75, known: true, reset: true},
		{name: "flat used percent", usage: `{"seven_day":{"used_percent":50}}`, label: "7-day limit", remaining: .5, known: true},
		{name: "utilization priority", usage: `{"five_hour":{"utilization":25,"percent":50,"used_percent":75}}`, label: "5-hour limit", remaining: .75, known: true},
		{name: "percent priority", usage: `{"five_hour":{"percent":25,"used_percent":50}}`, label: "5-hour limit", remaining: .75, known: true},
		{name: "null utilization stops", usage: `{"five_hour":{"utilization":null,"percent":50}}`, label: "5-hour limit"},
		{name: "null percent stops", usage: `{"five_hour":{"percent":null,"used_percent":50}}`, label: "5-hour limit"},
		{name: "null reset stops", usage: `{"five_hour":{"percent":25,"resets_at":null,"resetsAt":"2026-11-18T00:00:00Z"}}`, label: "5-hour limit", remaining: .75, known: true},
		{name: "limit utilization", usage: `{"limits":[{"kind":"session","utilization":25,"percent":50,"resetsAt":"2026-11-18T00:00:00Z"}]}`, label: "5-hour limit", remaining: .75, known: true, reset: true},
		{name: "limit used percent", usage: `{"limits":[{"kind":"weekly_all","used_percent":50}]}`, label: "7-day limit", remaining: .5, known: true},
		{name: "weekly opus", usage: `{"limits":[{"kind":"weekly_opus","percent":25}]}`, label: "7-day Opus", remaining: .75, known: true},
		{name: "weekly sonnet", usage: `{"limits":[{"kind":"weekly_sonnet","percent":25}]}`, label: "7-day Sonnet", remaining: .75, known: true},
		{name: "weekly fable", usage: `{"limits":[{"kind":"weekly_fable","percent":25}]}`, label: "7-day Fable 5", remaining: .75, known: true},
		{name: "weekly haiku", usage: `{"limits":[{"kind":"weekly_haiku","percent":25}]}`, label: "7-day Haiku", remaining: .75, known: true},
		{name: "weekly mythos", usage: `{"limits":[{"kind":"weekly_mythos","percent":25}]}`, label: "7-day Mythos", remaining: .75, known: true},
		{name: "bad preferred percent", usage: `{"five_hour":{"utilization":"bad","percent":50}}`, invalid: true},
		{name: "bad preferred reset", usage: `{"five_hour":{"resets_at":"bad","resetsAt":"2026-11-18T00:00:00Z"}}`, invalid: true},
		{name: "dollar currency", usage: `{"extra_usage":{"currency":"USD","used_credits":1250,"monthly_limit":5000}}`, amount: "$12.50 / $50.00"},
		{name: "other currency", usage: `{"extra_usage":{"currency":"EUR","used_credits":1250,"monthly_limit":5000}}`, amount: "EUR 12.50 / EUR 50.00"},
		{name: "default currency", usage: `{"extra_usage":{"used_credits":1250,"monthly_limit":5000}}`, amount: "$12.50 / $50.00"},
		{name: "invalid currency", usage: `{"extra_usage":{"currency":true,"used_credits":1250}}`, invalid: true},
		{name: "null currency", usage: `{"extra_usage":{"currency":null,"used_credits":1250}}`, amount: "$12.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := claudeRecorded(t, `{"usage":`+tc.usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
			result, _, err := Passive(cr)
			if tc.invalid {
				if err == nil || result != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(tc.usage), &raw); err != nil {
				t.Fatal(err)
			}
			shared, err := claudeUsageResult(raw)
			if err != nil {
				t.Fatal(err)
			}
			if tc.label != "" {
				windows := result.Groups[0].Windows
				if len(windows) != 1 || windows[0].Label.EN != tc.label || windows[0].Remaining != tc.remaining || windows[0].Known != tc.known || !windows[0].ResetAt.IsZero() != tc.reset {
					t.Fatalf("windows=%+v", windows)
				}
				if shared.Groups[0].Windows[0] != windows[0] {
					t.Fatal("active/passive shared parsing differs")
				}
			}
			if tc.amount != "" && (len(result.Facts) != 1 || result.Facts[0].Value != tc.amount) {
				t.Fatalf("facts=%+v", result.Facts)
			}
		})
	}
}

func TestClaudeRecordedParsingPreservesInputs(t *testing.T) {
	var usage map[string]any
	if err := json.Unmarshal([]byte(claudeFullUsage), &usage); err != nil {
		t.Fatal(err)
	}
	original := []Window{{Label: Text{"5 小时限额", "5-hour limit"}, Kind: KindShort, Known: true, Remaining: .9}}
	ages := []any{map[string]any{"kind": "five_hour", "used_percent": 20.0, "age_seconds": 30.0}}
	input := map[string]any{"usage": usage, "windows": original, "ages": ages}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claudeUsageResult(usage); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeLimitsWindows(usage, original, claudeFableLimit(usage)); err != nil {
		t.Fatal(err)
	}
	filtered := stripClaudeAges(ages).([]any)
	if _, exists := filtered[0].(map[string]any)["age_seconds"]; exists {
		t.Fatal("age not filtered")
	}
	after, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("input mutated\nbefore %s\nafter %s", before, after)
	}
}
