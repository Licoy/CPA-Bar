package quota

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeRecordedOptionalCurrency(t *testing.T) {
	for _, tc := range []struct{ name, field, amount string }{
		{"missing", "", "$12.50 / $50.00"},
		{"null", `"currency":null,`, "$12.50 / $50.00"},
		{"empty", `"currency":"",`, "$12.50 / $50.00"},
		{"whitespace", `"currency":" \t\n ",`, "$12.50 / $50.00"},
		{"USD", `"currency":"USD",`, "$12.50 / $50.00"},
		{"normalized USD", `"currency":" usd ",`, "$12.50 / $50.00"},
		{"EUR", `"currency":"EUR",`, "EUR 12.50 / EUR 50.00"},
		{"normalized EUR", `"currency":" eur ",`, "EUR 12.50 / EUR 50.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := strings.Replace(claudeFullUsage, `"extra_usage":{`, `"extra_usage":{`+tc.field+`"resets_at":"2026-12-01T00:00:00Z",`, 1)
			cr := claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
			result, stamp, err := Passive(cr)
			if err != nil || result == nil {
				t.Fatalf("optional currency blocked quota: result=%+v err=%v", result, err)
			}
			if stamp.Format(time.RFC3339Nano) != passiveStamp {
				t.Fatalf("lost observation time: %v", stamp)
			}
			assertClaudeCurrencyData(t, result, tc.amount)
			stub := newCodexStub(map[string]codexReply{claudeUsageURL: codexOK(usage), claudeProfileURL: codexOK(`{}`), claudeGrantsURL: codexOK(`{}`)})
			active, err := fetchClaude(context.Background(), stub, cr)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(active.Groups, result.Groups) || !reflect.DeepEqual(active.Facts, result.Facts) || !reflect.DeepEqual(active.Notes, result.Notes) || len(stub.sent) != 3 {
				t.Fatalf("active/passive differ: active=%+v passive=%+v requests=%d", active, result, len(stub.sent))
			}
		})
	}
}

func assertClaudeCurrencyData(t *testing.T, result *Result, amount string) {
	t.Helper()
	for _, want := range []struct {
		label, reset string
		remaining    float64
	}{
		{"5-hour limit", "2026-11-13T01:00:00Z", .55},
		{"7-day limit", "2026-11-18T01:00:00Z", .83},
		{"7-day Fable 5", "2026-11-18T01:00:00Z", .8},
		{"7-day future", "2026-11-18T01:00:00Z", 0},
	} {
		found := false
		for _, win := range result.Groups[0].Windows {
			if win.Label.EN == want.label {
				found = true
				if win.Remaining != want.remaining || win.ResetAt.Format(time.RFC3339Nano) != want.reset || win.Known != (want.label != "7-day future") {
					t.Fatalf("window changed: %+v", win)
				}
			}
		}
		if !found {
			t.Fatalf("missing %s", want.label)
		}
	}
	baseline, err := claudeUsageResult(mustClaudeUsage(t, claudeFullUsage))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Groups, baseline.Groups) || !reflect.DeepEqual(result.Notes, baseline.Notes) {
		t.Fatalf("lost windows or extra status: %+v", result)
	}
	if len(result.Facts) != 4 || result.Facts[0].Value != "2" || result.Facts[1].Value != "2026-12-01T00:00:00Z" || result.Facts[2].Value != amount || result.Facts[3].Value != "25.00%" {
		t.Fatalf("lost resets, amount or utilization: %+v", result.Facts)
	}
}

func mustClaudeUsage(t *testing.T, raw string) map[string]any {
	t.Helper()
	var usage map[string]any
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestClaudeRecordedInvalidCurrencyTypes(t *testing.T) {
	for _, value := range []string{`true`, `false`, `0`, `12.5`, `[]`, `["USD"]`, `{}`, `{"code":"USD"}`} {
		t.Run(value, func(t *testing.T) {
			usage := strings.Replace(claudeFullUsage, `"extra_usage":{`, `"extra_usage":{"currency":`+value+`,`, 1)
			cr := claudeRecorded(t, `{"usage":`+usage+`,"usage_observed_at":"`+passiveStamp+`"}`)
			result, _, err := Passive(cr)
			_, stamp, metaErr := PassiveObservationMeta(cr)
			var message *Msg
			if result != nil || !errors.As(err, &message) || message.EN != "Invalid recorded quota field: extra_usage.currency" || message.ZH != "已记录额度字段无效：extra_usage.currency" || metaErr != nil || stamp.Format(time.RFC3339Nano) != passiveStamp {
				t.Fatalf("invalid currency lost field error or watermark: result=%+v stamp=%v err=%v", result, stamp, err)
			}
			if active, err := claudeUsageResult(mustClaudeUsage(t, usage)); active != nil || !errors.As(err, &message) || message.EN != "Invalid recorded quota field: extra_usage.currency" {
				t.Fatalf("active invalid currency: result=%+v err=%v", active, err)
			}
		})
	}
}
