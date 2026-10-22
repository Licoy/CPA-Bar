package quota

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

const passiveStamp = "2026-10-22T00:00:00.123456789Z"

func passiveCredential(t *testing.T, provider string, quota any) cpa.Credential {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"provider": provider, "auth_index": "test", "quota": quota,
		"observed_at": "2026-10-23T00:00:00Z", "model_quotas": map[string]any{"model": map[string]any{"observed_at": passiveStamp, "signals": map[string]string{"x-codex-primary-used-percent": "0"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var credential cpa.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

func TestPassiveWindows(t *testing.T) {
	observed, _ := time.Parse(time.RFC3339Nano, passiveStamp)
	for _, tc := range []struct {
		name, provider string
		signals        map[string]string
		remaining      []float64
		kinds          []WindowKind
		resets         []time.Time
		plan           string
	}{
		{name: "claude ratios and expired reset", provider: "claude", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": "0.53", "Anthropic-Ratelimit-Unified-5h-Reset": "1700000000", "Anthropic-Ratelimit-Unified-7d-Utilization": "1"},
			remaining: []float64{.47, 0}, kinds: []WindowKind{KindShort, KindLong}, resets: []time.Time{time.Unix(1700000000, 0), {}}},
		{name: "codex swapped windows relative anchor", provider: "codex", signals: map[string]string{
			"X-Codex-Primary-Used-Percent": "51", "X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "3600",
			"x-codex-secondary-used-percent": "0", "x-codex-secondary-window-minutes": "300", "x-codex-secondary-reset-at": "1700000000", "x-codex-secondary-reset-after-seconds": "7200", "x-codex-plan-type": "plus"},
			remaining: []float64{.49, 1}, kinds: []WindowKind{KindLong, KindShort}, resets: []time.Time{observed.Add(time.Hour), time.Unix(1700000000, 0)}, plan: "Plus"},
		{name: "codex weekly only", provider: "codex", signals: map[string]string{"x-codex-primary-used-percent": "100", "x-codex-primary-window-minutes": "10080"},
			remaining: []float64{0}, kinds: []WindowKind{KindLong}, resets: []time.Time{{}}},
		{name: "codex unknown duration", provider: "codex", signals: map[string]string{"x-codex-primary-used-percent": "50"},
			remaining: []float64{.5}, kinds: []WindowKind{KindOther}, resets: []time.Time{{}}},
		{name: "devin percent left", provider: "devin", signals: map[string]string{"daily_quota_remaining_percent": "95%", "weekly_quota_remaining_percent": "45%", "daily_quota_reset_at": "2026-10-21T00:00:00Z", "plan": "Pro"},
			remaining: []float64{.95, .45}, kinds: []WindowKind{KindShort, KindLong}, resets: []time.Time{time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), {}}, plan: "Pro"},
		{name: "devin zero and full", provider: "devin", signals: map[string]string{"daily_quota_remaining_percent": "0%", "weekly_quota_remaining_percent": "100%"},
			remaining: []float64{0, 1}, kinds: []WindowKind{KindShort, KindLong}, resets: []time.Time{{}, {}}},
		{name: "claude unused", provider: "claude", signals: map[string]string{"anthropic-ratelimit-unified-5h-utilization": "0"},
			remaining: []float64{1}, kinds: []WindowKind{KindShort}, resets: []time.Time{{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, stamp, err := Passive(passiveCredential(t, tc.provider, map[string]any{"observed_at": passiveStamp, "signals": tc.signals}))
			if err != nil || result == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !stamp.Equal(observed) || result.Plan != tc.plan || len(result.Actions) != 0 {
				t.Fatalf("stamp=%v result=%+v", stamp, result)
			}
			windows := result.Groups[0].Windows
			if len(windows) != len(tc.remaining) {
				t.Fatalf("windows=%+v", windows)
			}
			for i, w := range windows {
				if !w.Known || math.Abs(w.Remaining-tc.remaining[i]) > 1e-10 || w.Kind != tc.kinds[i] || !w.ResetAt.Equal(tc.resets[i]) {
					t.Fatalf("window %d: %+v", i, w)
				}
			}
		})
	}
}

func TestPassiveMissing(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		quota          any
	}{
		{"unsupported", "gemini", map[string]any{"observed_at": passiveStamp, "signals": map[string]string{"bad": "NaN"}}},
		{"null quota", "codex", nil},
		{"no observation", "codex", map[string]any{"signals": map[string]string{"x-codex-primary-used-percent": "0"}}},
		{"no signals", "claude", map[string]any{"observed_at": passiveStamp}},
		{"model only", "codex", map[string]any{"observed_at": passiveStamp, "signals": map[string]string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, stamp, err := Passive(passiveCredential(t, tc.provider, tc.quota))
			if r != nil || !stamp.IsZero() || err != nil {
				t.Fatalf("%+v %v %v", r, stamp, err)
			}
		})
	}
	var cr cpa.Credential
	if err := json.Unmarshal([]byte(`{"provider":"claude","observed_at":"2026-10-22T00:00:00Z"}`), &cr); err != nil {
		t.Fatal(err)
	}
	if r, stamp, err := Passive(cr); r != nil || !stamp.IsZero() || err != nil {
		t.Fatalf("%+v %v %v", r, stamp, err)
	}
}

func TestPassiveInvalid(t *testing.T) {
	for _, tc := range []struct {
		provider string
		key      string
		values   []any
	}{
		{"claude", "anthropic-ratelimit-unified-5h-utilization", []any{"NaN", "Inf", "-0.1", "1.01", "0.5 trailing", 0.5, nil, true}},
		{"codex", "x-codex-primary-used-percent", []any{"NaN", "+Inf", "-1", "101", 51}},
		{"codex", "x-codex-primary-window-minutes", []any{"-1", "0", "NaN", "300.5"}},
		{"codex", "x-codex-primary-reset-after-seconds", []any{"-1", "Inf", "999999999999999999999999"}},
		{"codex", "x-codex-primary-reset-at", []any{"invalid", "0", "-1", "1700000000.5"}},
		{"devin", "daily_quota_remaining_percent", []any{"NaN%", "Inf%", "-1%", "101%", "95%%", 95}},
		{"devin", "daily_quota_reset_at", []any{"invalid", 1700000000}},
	} {
		for _, value := range tc.values {
			t.Run(tc.key+"/"+stringMustJSON(t, value), func(t *testing.T) {
				quota := map[string]any{"observed_at": passiveStamp, "signals": map[string]any{tc.key: value}}
				r, stamp, err := Passive(passiveCredential(t, tc.provider, quota))
				var message *Msg
				if r != nil || !stamp.IsZero() || !errors.As(err, &message) || message.ZH == "" || message.EN == "" {
					t.Fatalf("result=%+v stamp=%v err=%v", r, stamp, err)
				}
			})
		}
	}
	for _, quota := range []any{"bad", []string{}, map[string]any{"observed_at": 42}, map[string]any{"observed_at": ""}, map[string]any{"observed_at": "invalid"}, map[string]any{"observed_at": passiveStamp, "signals": []string{}}, map[string]any{"observed_at": passiveStamp, "signals": nil}} {
		if r, _, err := Passive(passiveCredential(t, "claude", quota)); err == nil || r != nil {
			t.Fatalf("quota=%v r=%+v err=%v", quota, r, err)
		}
	}
}

func stringMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPassiveResetWithoutUsage(t *testing.T) {
	r, _, err := Passive(passiveCredential(t, "claude", map[string]any{"observed_at": passiveStamp, "signals": map[string]string{"anthropic-ratelimit-unified-5h-reset": "1700000000"}}))
	if err != nil || r == nil {
		t.Fatalf("%+v %v", r, err)
	}
	w := r.Groups[0].Windows[0]
	if w.Known || w.Remaining != 0 {
		t.Fatalf("missing usage became full: %+v", w)
	}
	if short, long := r.Summary(); short != nil || long != nil {
		t.Fatalf("unknown window included in summary")
	}
}

func TestPassiveCodexAdditional(t *testing.T) {
	signals := map[string]string{"x-codex-code-review-primary-used-percent": "10", "x-codex-code-review-primary-window-minutes": "10080", "x-codex-bengalfox-primary-used-percent": "20", "x-codex-additional-other-secondary-used-percent": "30"}
	cr := passiveCredential(t, "codex", map[string]any{"observed_at": passiveStamp, "signals": signals})
	r, _, err := Passive(cr)
	if err != nil || r == nil || len(r.Groups) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	if short, long := r.Summary(); short != nil || long == nil || long.Kind != KindOther {
		t.Fatalf("additional-only summary=%+v %+v", short, long)
	}
	for _, g := range r.Groups {
		if g.Title.IsZero() || g.Windows[0].Kind != KindOther || g.Windows[0].Label.EN == "Primary limit" {
			t.Fatalf("%+v", g)
		}
	}
	signals["x-codex-primary-used-percent"] = "50"
	signals["x-codex-primary-window-minutes"] = "300"
	signals["x-codex-secondary-used-percent"] = "60"
	signals["x-codex-secondary-window-minutes"] = "10080"
	r, _, err = Passive(passiveCredential(t, "codex", map[string]any{"observed_at": passiveStamp, "signals": signals}))
	short, long := r.Summary()
	if err != nil || short == nil || long == nil || short.Remaining != .5 || long.Remaining != .4 {
		t.Fatalf("summary=%+v %+v err=%v", short, long, err)
	}
}
