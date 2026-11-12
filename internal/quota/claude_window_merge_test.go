package quota

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

const claudeNullableWindows = `{
 "five_hour":{"utilization":10,"resets_at":"2026-11-13T01:00:00Z","limit_dollars":null,"used_dollars":null,"remaining_dollars":null},
 "seven_day":{"utilization":20,"resets_at":"2026-11-18T01:00:00Z","limit_dollars":null,"used_dollars":null,"remaining_dollars":null},
 "limits":[{"kind":"session","percent":45},{"kind":"weekly_all","percent":17,"resets_at":null},
 {"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"is_active":true,"percent":20,"resets_at":"2026-11-18T02:00:00Z"}]}`

func TestClaudeNullableWindowsMergeActiveAndPassive(t *testing.T) {
	cr := claudeRecorded(t, `{"usage":`+claudeNullableWindows+`,"usage_observed_at":"`+passiveStamp+`"}`)
	passive, _, err := Passive(cr)
	if err != nil {
		t.Fatal(err)
	}
	stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(claudeNullableWindows), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: codexOK(`{}`)})
	active, err := fetchClaude(context.Background(), stub, cr)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	expected := []Window{
		{Label: Text{"5 小时限额", "5-hour limit"}, Known: true, Remaining: .55, Kind: KindShort, ResetAt: reset},
		{Label: Text{"7 天限额", "7-day limit"}, Known: true, Remaining: .83, Kind: KindLong},
		{Label: Text{"7 天 Fable 5", "7-day Fable 5"}, Known: true, Remaining: .8, ResetAt: time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)},
	}
	for name, result := range map[string]*Result{"active": active, "passive": passive} {
		if len(result.Groups) != 1 || !reflect.DeepEqual(result.Groups[0].Windows, expected) {
			t.Fatalf("%s windows=%+v, want %+v", name, result.Groups, expected)
		}
		short, long := result.Summary()
		if short == nil || long == nil || *short != expected[0] || *long != expected[1] {
			t.Fatalf("%s summary=%+v %+v", name, short, long)
		}
	}
}

func TestClaudeWindowIdentityAndDollarBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		windows     []Window
		invalid     bool
	}{
		{name: "model aliases", usage: `{"seven_day_sonnet":{"utilization":10},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Sonnet"}},"percent":25}]}`, windows: []Window{{Label: Text{"7 天 Sonnet", "7-day Sonnet"}, Known: true, Remaining: .75}}},
		{name: "unknown remains unknown", usage: `{"five_hour":{"utilization":10,"used_dollars":null},"limits":[{"kind":"session","percent":null}]}`, windows: []Window{{Label: Text{"5 小时限额", "5-hour limit"}, Kind: KindShort}}},
		{name: "distinct money pool", usage: `{"iguana_necktie":{"used_dollars":0,"remaining_dollars":20},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":25}]}`, windows: []Window{{Label: Text{"云端会话额度", "Cloud session credits"}, Detail: "used $0.000000 · remaining $20.000000"}, {Label: Text{"7 天 Fable 5", "7-day Fable 5"}, Known: true, Remaining: .75}}},
		{name: "zero is a real money pool", usage: `{"additional_credit_balance":{"limit_dollars":null,"used_dollars":0,"remaining_dollars":null}}`, windows: []Window{{Label: Text{"追加余额", "Additional credit balance"}, Detail: "used $0.000000"}}},
		{name: "null fable aliases merge", usage: `{"iguana_necktie":{"used_dollars":null,"utilization":10},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"percent":25}]}`, windows: []Window{{Label: Text{"7 天 Fable 5", "7-day Fable 5"}, Known: true, Remaining: .75}}},
		{name: "invalid flat amount", usage: `{"five_hour":{"utilization":10,"used_dollars":"bad"}}`, invalid: true},
		{name: "invalid arbitrary amount", usage: `{"future_pool":{"used_dollars":false}}`, invalid: true},
		{name: "negative amount", usage: `{"future_pool":{"used_dollars":-1}}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := Passive(claudeRecorded(t, `{"usage":`+tc.usage+`,"usage_observed_at":"`+passiveStamp+`"}`))
			if tc.invalid {
				if err == nil || result != nil || !strings.Contains(err.Error(), "used_dollars") {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Groups[0].Windows, tc.windows) {
				t.Fatalf("windows=%+v, want %+v", result.Groups[0].Windows, tc.windows)
			}
		})
	}
}
