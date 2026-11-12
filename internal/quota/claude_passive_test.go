package quota

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

const claudeFullUsage = `{
 "five_hour":{"utilization":42.5,"resets_at":"2026-11-13T01:00:00Z"},
 "seven_day":{"utilization":16,"resets_at":"2026-11-18T01:00:00Z"},
 "seven_day_sonnet":{"utilization":10},"seven_day_oauth_apps":{"utilization":12},
 "seven_day_opus":{"utilization":13},"seven_day_cowork":{"utilization":14},
 "five_hour_future":{"utilization":15},"seven_day_future":{"utilization":null,"resets_at":"2026-11-18T01:00:00Z"},
 "iguana_necktie":{"limit_dollars":100,"used_dollars":5.043595,"remaining_dollars":94.956405,"utilization":5.043595,"resets_at":"2026-12-01T00:00:00Z"},
 "additional_credit_balance":{"limit_dollars":20,"used_dollars":0,"remaining_dollars":20,"utilization":0,"resets_at":null},
 "limits":[{"kind":"session","percent":45},{"kind":"weekly_all","percent":17},
 {"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"is_active":false,"percent":90},
 {"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"is_active":true,"percent":20,"resets_at":"2026-11-18T01:00:00Z"},
 {"kind":"future_window","percent":3}],
 "extra_usage":{"is_enabled":false,"used_credits":1250,"monthly_limit":5000,"utilization":25,"user_disabled":true,"spend_limit_reached":false,"disabled_reason":"user request"},
 "cedar_ember":{"eligible":true,"grants":[{"id":"real","resets_total":5,"resets_left":2}]}}
`

func claudeRecorded(t *testing.T, sub string) cpa.Credential {
	t.Helper()
	raw := `{"provider":"claude","auth_index":"test","claude_subscription":` + sub + `,"quota":{"observed_at":"2026-11-14T00:00:00Z","signals":{"anthropic-ratelimit-unified-5h-utilization":"0.99"}}}`
	var cr cpa.Credential
	if err := json.Unmarshal([]byte(raw), &cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func TestClaudeRecordedFullUsage(t *testing.T) {
	cr := claudeRecorded(t, `{"usage":`+claudeFullUsage+`,"usage_observed_at":"`+passiveStamp+`","observed_at":"2026-11-14T00:00:00Z"}`)
	result, stamp, err := Passive(cr)
	if err != nil || result == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if stamp.Format(time.RFC3339Nano) != passiveStamp {
		t.Fatal(stamp)
	}
	windows := result.Groups[0].Windows
	if len(windows) != 12 {
		t.Fatalf("windows (%d)=%+v", len(windows), windows)
	}
	short, long := result.Summary()
	if short.Remaining != .55 || long.Remaining != .83 || short.ResetAt.IsZero() || long.ResetAt.IsZero() {
		t.Fatalf("summary=%+v %+v", short, long)
	}
	countFable := 0
	for _, win := range windows {
		if strings.Contains(win.Label.EN, "Fable") {
			countFable++
			if win.Remaining != .8 || win.ResetAt.IsZero() {
				t.Fatalf("Fable=%+v", win)
			}
		}
		if win.Label.EN == "7-day future" && (win.Known || win.Remaining != 0 || win.ResetAt.IsZero()) {
			t.Fatal(win)
		}
		if win.Label.EN == "Cloud session credits" && !strings.Contains(win.Detail, "94.956405") {
			t.Fatal(win)
		}
	}
	if countFable != 1 || len(result.Facts) != 3 || result.Facts[0].Value != "2" || result.Facts[1].Value != "$12.50 / $50.00" || !strings.Contains(result.Notes[len(result.Notes)-1].EN, "user request") {
		t.Fatalf("facts=%+v", result.Facts)
	}
	stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(claudeFullUsage), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: codexOK(`{}`)})
	active, err := fetchClaude(context.Background(), stub, cr)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(active.Groups, result.Groups) || !reflect.DeepEqual(active.Facts, result.Facts) || !reflect.DeepEqual(active.Notes, result.Notes) || len(stub.sent) != 3 {
		t.Fatalf("active=%+v passive=%+v requests=%d", active, result, len(stub.sent))
	}
}

func TestClaudeRecordedNormalizedAndNull(t *testing.T) {
	cr := claudeRecorded(t, `{"observed_at":"`+passiveStamp+`","windows":[{"kind":"five_hour","used_percent":null,"resets_at":"2026-11-18T00:00:00Z","inactive":false},{"kind":"seven_day","model":"sonnet","used_percent":50},{"kind":"seven_day","inactive":true}]}`)
	result, _, err := Passive(cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups[0].Windows) != 2 || result.Groups[0].Windows[0].Known || result.Groups[0].Windows[0].Remaining != 0 || result.Groups[0].Windows[1].Label.EN != "7-day Sonnet" {
		t.Fatalf("%+v", result)
	}
	for _, usage := range []string{`{"five_hour":null}`, `{"five_hour":{"utilization":null}}`, `{"cedar_ember":{"remaining_dollars":99}}`} {
		result, _, err := Passive(claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range result.Groups[0].Windows {
			if w.Known || w.Remaining != 0 {
				t.Fatal(w)
			}
		}
		if len(result.Facts) != 0 {
			t.Fatal(result.Facts)
		}
	}
}

func TestClaudeRecordedMalformedNeverFallsBack(t *testing.T) {
	for _, usage := range []string{`[]`, `{"five_hour":true}`, `{"five_hour":{"utilization":"bad"}}`, `{"five_hour":{"resets_at":"bad"}}`, `{"limits":{}}`, `{"limits":[{"kind":"session","percent":-1}]}`, `{"iguana_necktie":{"remaining_dollars":"bad"}}`, `{"extra_usage":{"is_enabled":"true"}}`, `{"cedar_ember":{"grants":[{}]}}`} {
		cr := claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
		if result, _, err := Passive(cr); err == nil || result != nil {
			t.Fatalf("usage=%s result=%+v err=%v", usage, result, err)
		}
		_, stamp, _ := PassiveObservationMeta(cr)
		if stamp.Format(time.RFC3339Nano) != passiveStamp {
			t.Fatalf("lost error watermark: %v", stamp)
		}
	}
}

func TestClaudeRecordedIdentity(t *testing.T) {
	base := `{"usage":{"five_hour":{"utilization":20}},"usage_observed_at":"` + passiveStamp + `","observed_at":"2026-11-14T00:00:00Z","usage_age_seconds":1,"evaluation":{"state":"ready"}}`
	original, stamp, err := PassiveObservationMeta(claudeRecorded(t, base))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(strings.ReplaceAll(base, `"usage_age_seconds":1`, `"usage_age_seconds":999`), `"state":"ready"`, `"state":"blocked"`)
	changed = strings.ReplaceAll(changed, `"observed_at":"2026-11-14`, `"observed_at":"2026-11-15`)
	same, next, err := PassiveObservationMeta(claudeRecorded(t, changed))
	if err != nil || original != same || !stamp.Equal(next) {
		t.Fatalf("identity drifted %s %s %v", original, same, err)
	}
	for _, change := range []string{strings.ReplaceAll(base, `"utilization":20`, `"utilization":21`), strings.ReplaceAll(base, passiveStamp, "2026-11-16T00:00:00Z")} {
		different, _, _ := PassiveObservationMeta(claudeRecorded(t, change))
		if different == original {
			t.Fatal("new observation ignored")
		}
	}
	normalized := `{"observed_at":"` + passiveStamp + `","windows":[{"kind":"five_hour","used_percent":20,"age_seconds":1}]}`
	one, _, _ := PassiveObservationMeta(claudeRecorded(t, normalized))
	two, _, _ := PassiveObservationMeta(claudeRecorded(t, strings.ReplaceAll(normalized, `"age_seconds":1`, `"age_seconds":500`)))
	if one != two {
		t.Fatal("normalized age changed identity")
	}
}

func TestClaudeRecordedLegacyModels(t *testing.T) {
	result, _, err := Passive(passiveCredential(t, "claude", map[string]any{"observed_at": passiveStamp, "signals": map[string]string{"anthropic-ratelimit-unified-7d-opus-utilization": "0.2", "anthropic-ratelimit-unified-7d-sonnet-utilization": "0.3", "anthropic-ratelimit-unified-7d_oi-utilization": "0.4"}}))
	if err != nil || len(result.Groups[0].Windows) != 3 {
		t.Fatalf("%+v %v", result, err)
	}
	labels := []string{}
	for _, w := range result.Groups[0].Windows {
		labels = append(labels, w.Label.EN)
	}
	if !reflect.DeepEqual(labels, []string{"7-day Opus", "7-day Sonnet", "7-day Fable 5"}) {
		t.Fatal(labels)
	}
}

func TestClaudeRecordedExactNumericIdentity(t *testing.T) {
	one, _, _ := PassiveObservationMeta(claudeRecorded(t, `{"usage":{"large":9007199254740992},"usage_observed_at":"`+passiveStamp+`"}`))
	two, _, _ := PassiveObservationMeta(claudeRecorded(t, `{"usage":{"large":9007199254740993},"usage_observed_at":"`+passiveStamp+`"}`))
	if one == two {
		t.Fatal("large integers lost observation identity")
	}
}

func TestClaudeRecordedActiveFableUnknown(t *testing.T) {
	usage := `{"iguana_necktie":{"utilization":80},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5"}},"percent":90},{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"is_active":true,"percent":null,"resets_at":"2026-11-18T00:00:00Z"}]}`
	result, _, err := Passive(claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	windows := result.Groups[0].Windows
	if len(windows) != 1 || windows[0].Known || windows[0].Remaining != 0 || windows[0].ResetAt.IsZero() {
		t.Fatalf("%+v", windows)
	}
}

func TestClaudeRecordedMissingUsageTimestamp(t *testing.T) {
	cr := claudeRecorded(t, `{"usage":{"five_hour":{"utilization":10}},"observed_at":"`+passiveStamp+`","windows":[{"kind":"five_hour","used_percent":20}]}`)
	if result, _, err := Passive(cr); err == nil || result != nil {
		t.Fatalf("used header time for usage: %+v %v", result, err)
	}
}

func TestPassiveObservationMetaCodexMalformedWatermark(t *testing.T) {
	cr := passiveCredential(t, "codex", map[string]any{"observed_at": passiveStamp, "signals": map[string]any{"x-codex-primary-used-percent": false}})
	identity, stamp, err := PassiveObservationMeta(cr)
	if err == nil || identity == "" || stamp.Format(time.RFC3339Nano) != passiveStamp {
		t.Fatalf("identity=%s stamp=%v err=%v", identity, stamp, err)
	}
}

func TestClaudeRecordedGrantsRefreshReplacesUsageCount(t *testing.T) {
	for _, reply := range []codexReply{
		codexOK(`{"cedar_ember":{"eligible":true,"grants":[{"id":"real","resets_total":5,"resets_left":1}]}}`),
		{status: 503, body: `{"error":"unavailable"}`},
	} {
		stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(claudeFullUsage), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: reply})
		result, err := fetchClaude(context.Background(), stub, claudeRecorded(t, `{}`))
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		expected := "1"
		if reply.status != 200 {
			expected = "2"
		}
		for _, fact := range result.Facts {
			if fact.Label.EN == "Resets remaining" {
				count++
				if fact.Value != expected {
					t.Fatal(fact)
				}
			}
		}
		if count != 1 || len(stub.sent) != 3 {
			t.Fatalf("count=%d requests=%d", count, len(stub.sent))
		}
	}
	for _, windows := range []string{`[]`, `[{"kind":"five_hour","inactive":true,"used_percent":null}]`} {
		result, _, err := Passive(claudeRecorded(t, `{"windows":`+windows+`,"observed_at":"`+passiveStamp+`"}`))
		if err != nil || result == nil || len(result.Notes) == 0 {
			t.Fatalf("empty normalized=%+v %v", result, err)
		}
	}
}
