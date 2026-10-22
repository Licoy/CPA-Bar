package quota

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Passive reads only the account's recorded quota, never model-scoped signals
// or an upstream endpoint; the returned time is the account observation time.
func Passive(cr cpa.Credential) (*Result, time.Time, error) {
	provider := ProviderKey(cr)
	if provider != "claude" && provider != "codex" && provider != "devin" {
		return nil, time.Time{}, nil
	}
	signals, observed, err := passiveSnapshot(cr.Raw)
	if err != nil || signals == nil {
		return nil, time.Time{}, err
	}
	var result *Result
	switch provider {
	case "claude":
		result, err = passiveClaude(signals)
	case "codex":
		result, err = passiveCodex(signals, observed)
	case "devin":
		result, err = passiveDevin(signals)
	}
	if err != nil || result == nil {
		return nil, time.Time{}, err
	}
	return result, observed, nil
}

func passiveSnapshot(rawJSON json.RawMessage) (map[string]string, time.Time, error) {
	if len(rawJSON) == 0 {
		return nil, time.Time{}, nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(rawJSON, &raw) != nil || raw == nil {
		return nil, time.Time{}, passiveInvalid("credential")
	}
	data, exists := raw["quota"]
	if !exists || strings.TrimSpace(string(data)) == "null" {
		return nil, time.Time{}, nil
	}
	var quota map[string]json.RawMessage
	if json.Unmarshal(data, &quota) != nil || quota == nil {
		return nil, time.Time{}, passiveInvalid("quota")
	}
	data, exists = quota["observed_at"]
	if !exists {
		return nil, time.Time{}, nil
	}
	var stamp string
	if json.Unmarshal(data, &stamp) != nil || stamp == "" {
		return nil, time.Time{}, passiveInvalid("quota.observed_at")
	}
	observed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || observed.IsZero() {
		return nil, time.Time{}, passiveInvalid("quota.observed_at")
	}
	var fields map[string]json.RawMessage
	if data, exists = quota["signals"]; !exists {
		return nil, time.Time{}, nil
	}
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, time.Time{}, passiveInvalid("quota.signals")
	}
	signals, err := passiveSignals(fields)
	return signals, observed, err
}

func passiveSignals(fields map[string]json.RawMessage) (map[string]string, error) {
	signals := make(map[string]string, len(fields))
	for k, v := range fields {
		var s string
		if string(v) == "null" || json.Unmarshal(v, &s) != nil {
			return nil, passiveInvalid("quota.signals." + k)
		}
		key := strings.ToLower(k)
		if old, exists := signals[key]; exists && old != s {
			return nil, passiveInvalid("quota.signals." + k)
		}
		signals[key] = s
	}
	return signals, nil
}

func passiveInvalid(field string) error {
	return msg("已记录额度字段无效："+field, "Invalid recorded quota field: "+field)
}

func passiveNumber(signals map[string]string, key string, limit float64) (float64, bool, error) {
	text, exists := signals[key]
	if !exists {
		return 0, false, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > limit {
		return 0, false, passiveInvalid(key)
	}
	return value, true, nil
}

func passiveUnix(signals map[string]string, key string) (time.Time, error) {
	text, exists := signals[key]
	if !exists {
		return time.Time{}, nil
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || sec <= 0 || sec > 253402300799 {
		return time.Time{}, passiveInvalid(key)
	}
	return time.Unix(sec, 0), nil
}

func passiveClaude(signals map[string]string) (*Result, error) {
	var windows []Window
	for i, period := range []string{"5h", "7d"} {
		prefix := "anthropic-ratelimit-unified-" + period
		used, known, err := passiveNumber(signals, prefix+"-utilization", 1)
		if err != nil {
			return nil, err
		}
		reset, err := passiveUnix(signals, prefix+"-reset")
		if err != nil {
			return nil, err
		}
		if !known && reset.IsZero() {
			continue
		}
		remaining := 0.0
		if known {
			remaining = 1 - used
		}
		windows = append(windows, Window{Label: claudeWindows[i].label, Kind: claudeWindows[i].kind, Known: known, Remaining: remaining, ResetAt: reset})
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return &Result{Groups: []Group{{Windows: windows}}}, nil
}

func passiveDevin(signals map[string]string) (*Result, error) {
	var windows []Window
	for i, period := range []string{"daily", "weekly"} {
		key := period + "_quota_remaining_percent"
		percent, known := 0.0, false
		if text, exists := signals[key]; exists {
			var ok bool
			percent, ok = devinPercent(strings.TrimSuffix(strings.TrimSpace(text), "%"))
			if !ok {
				return nil, passiveInvalid(key)
			}
			known = true
		}
		var reset time.Time
		if text, exists := signals[period+"_quota_reset_at"]; exists {
			var err error
			reset, err = time.Parse(time.RFC3339Nano, text)
			if err != nil || reset.IsZero() {
				return nil, passiveInvalid(period + "_quota_reset_at")
			}
		}
		if !known && reset.IsZero() {
			continue
		}
		labels := []Text{{"每日剩余额度", "Daily remaining"}, {"每周剩余额度", "Weekly remaining"}}
		kinds := []WindowKind{KindShort, KindLong}
		windows = append(windows, Window{Label: labels[i], Kind: kinds[i], Known: known, Remaining: percent / 100, ResetAt: reset})
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return &Result{Plan: signals["plan"], Groups: []Group{{Windows: windows}}}, nil
}
