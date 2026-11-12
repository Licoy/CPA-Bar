package quota

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const claudeMetadataUsage = `{
 "five_hour":{"utilization":42,"resets_at":"2026-11-13T01:00:00Z"},
 "seven_day":{"utilization":16,"resets_at":"2026-11-18T01:00:00Z"},
 "seven_day_breakdown":{"sonnet":{"utilization":10},"fable":{"utilization":20}},
 "five_hour_metadata":{"models":["sonnet"],"limit_dollars":null},
 "seven_day_summary":{"total_tokens":123},
 "limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable5"}},"is_active":true,"percent":20,"resets_at":"2026-11-18T01:00:00Z"}]
}`

func claudeMetadataResults(t *testing.T, usage string) []*Result {
	t.Helper()
	cr := claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
	passive, stamp, err := Passive(cr)
	if err != nil || stamp.Format(time.RFC3339Nano) != passiveStamp {
		t.Fatalf("passive: %v %v", stamp, err)
	}
	stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(usage), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: codexOK(`{}`)})
	active, err := fetchClaude(context.Background(), stub, cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.sent) != 3 || !reflect.DeepEqual(active.Groups, passive.Groups) {
		t.Fatalf("active/passive mismatch: requests=%d active=%+v passive=%+v", len(stub.sent), active, passive)
	}
	return []*Result{passive, active}
}

func TestClaudeUsageMetadataNotWindows(t *testing.T) {
	for _, result := range claudeMetadataResults(t, claudeMetadataUsage) {
		windows := result.Groups[0].Windows
		if len(windows) != 3 {
			t.Fatalf("metadata rendered as quota windows: %+v", windows)
		}
		for i, label := range []string{"5-hour limit", "7-day limit", "7-day Fable 5"} {
			if windows[i].Label.EN != label || !windows[i].Known || windows[i].ResetAt.IsZero() {
				t.Fatalf("lost real window: %+v", windows[i])
			}
		}
		short, long := result.Summary()
		if short == nil || long == nil || short.Kind != KindShort || long.Kind != KindLong || math.Abs(short.Remaining-.58) > 1e-9 || long.Remaining != .84 {
			t.Fatalf("summary changed: %+v %+v", short, long)
		}
	}
	usage := mustClaudeUsage(t, claudeMetadataUsage)
	before, _ := json.Marshal(usage)
	if _, err := claudeUsageResult(usage); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(usage)
	if string(before) != string(after) {
		t.Fatal("parser mutated usage input")
	}
}

func TestClaudeUsageMetadataKeepsUnknownAndMoney(t *testing.T) {
	usage := `{"five_hour_unknown":{"percent":null,"resets_at":"2026-11-18T01:00:00Z"},"seven_day_unknown":{"resetsAt":"2026-11-18T01:00:00Z"},"seven_day_null":{"used_percent":null,"resets_at":null},"five_hour_null":{"utilization":null},"seven_day_money":{"remaining_dollars":0},"five_hour_metadata":{},"seven_day_breakdown":{"total_tokens":123}}`
	for _, result := range claudeMetadataResults(t, usage) {
		windows := result.Groups[0].Windows
		if len(windows) != 5 {
			t.Fatalf("unknown or money window lost: %+v", windows)
		}
		for _, win := range windows {
			if win.Known || win.Remaining != 0 {
				t.Fatalf("unknown quota invented: %+v", win)
			}
			if strings.HasSuffix(win.Label.EN, "unknown") && win.ResetAt.IsZero() {
				t.Fatalf("reset lost: %+v", win)
			}
			if win.Label.EN == "seven_day_money" && win.Detail != "remaining $0.000000" {
				t.Fatal(win)
			}
		}
	}
}

func TestClaudeUsageMetadataDoesNotHideInvalidQuota(t *testing.T) {
	for _, row := range []string{`{"utilization":"bad"}`, `{"percent":false}`, `{"used_percent":-1}`, `{"resetsAt":42}`, `{"remaining_dollars":"bad"}`} {
		usage := `{"seven_day_future":` + row + `}`
		cr := claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
		if result, _, err := Passive(cr); err == nil || result != nil {
			t.Fatalf("invalid quota hidden: %s result=%+v err=%v", row, result, err)
		}
		stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(usage), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: codexOK(`{}`)})
		if result, err := fetchClaude(context.Background(), stub, cr); err == nil || result != nil || len(stub.sent) != 3 {
			t.Fatalf("invalid active quota hidden: %s result=%+v err=%v requests=%d", row, result, err, len(stub.sent))
		}
	}
}

func TestClaudeUsageMetadataOnlyHasNoQuota(t *testing.T) {
	for _, result := range claudeMetadataResults(t, `{"seven_day_breakdown":{"sonnet":{"utilization":10}},"five_hour_metadata":{},"seven_day_summary":{"total_tokens":123}}`) {
		short, long := result.Summary()
		if len(result.Groups[0].Windows) != 0 || short != nil || long != nil || len(result.Notes) != 1 || result.Notes[0].EN != "No recorded quota data" {
			t.Fatalf("metadata invented quota: %+v", result)
		}
	}
}
