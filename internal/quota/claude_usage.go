package quota

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

func claudeUsageResult(usage map[string]any) (*Result, error) {
	windows, err := claudeDisplayWindows(usage)
	if err != nil {
		return nil, err
	}
	facts, notes, err := claudeUsageFacts(usage)
	if err != nil {
		return nil, err
	}
	result := &Result{Groups: []Group{{Windows: windows}}, Facts: facts, Notes: notes}
	if len(windows) == 0 && len(result.Facts) == 0 && len(result.Notes) == 0 {
		result.Notes = append(result.Notes, Text{"暂无已记录的额度数据", "No recorded quota data"})
	}
	return result, nil
}

func claudeLabel(key, model string) (Text, WindowKind) {
	switch key {
	case "weekly_opus", "weekly_sonnet", "weekly_fable", "weekly_haiku", "weekly_mythos":
		key = "seven_day_" + strings.TrimPrefix(key, "weekly_")
	}
	if key == "session" {
		key = "five_hour"
	}
	if key == "weekly_all" {
		key = "seven_day"
	}
	if key == "weekly_scoped" && model != "" {
		key = "seven_day_" + model
	}
	if key == "iguana_necktie" || key == "seven_day_overage_included" {
		key = "seven_day_fable"
	}
	for _, w := range claudeWindows {
		if w.key == key && model == "" {
			return w.label, w.kind
		}
	}
	if model != "" && (key == "five_hour" || key == "seven_day") {
		key += "_" + model
	}
	if strings.HasPrefix(key, "seven_day_") {
		name := strings.TrimPrefix(key, "seven_day_")
		switch strings.ToLower(strings.ReplaceAll(name, " ", "")) {
		case "fable", "fable5":
			name = "Fable 5"
		case "opus":
			name = "Opus"
		case "sonnet":
			name = "Sonnet"
		case "cowork":
			name = "Cowork"
		case "haiku":
			name = "Haiku"
		case "mythos":
			name = "Mythos"
		case "oauth_apps":
			name = "OAuth 应用"
		}
		return Text{"7 天 " + name, "7-day " + strings.ReplaceAll(name, "OAuth 应用", "OAuth apps")}, KindOther
	}
	if strings.HasPrefix(key, "five_hour_") {
		name := strings.TrimPrefix(key, "five_hour_")
		return Text{"5 小时 " + name, "5-hour " + name}, KindOther
	}
	return Same(key), KindOther
}

func claudeNumeric(row map[string]any, key string) (float64, bool, error) {
	value, exists := row[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	n, ok := value.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false, passiveInvalid(key)
	}
	return n, true, nil
}

func claudeReadWindow(row map[string]any, key, model string) (Window, error) {
	label, kind := claudeLabel(key, model)
	percent, _ := claudeFirstField(row, "utilization", "percent", "used_percent")
	used, known, err := claudeNumeric(row, percent)
	if err != nil {
		return Window{}, err
	}
	var reset time.Time
	resetKey, _ := claudeFirstField(row, "resets_at", "resetsAt")
	if value := row[resetKey]; value != nil {
		stamp, ok := value.(string)
		if !ok {
			return Window{}, passiveInvalid(key + "." + resetKey)
		}
		reset, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return Window{}, passiveInvalid(key + "." + resetKey)
		}
	}
	win := usedWindow(label, used, known, reset)
	win.Kind = kind
	return win, nil
}

func claudeDollarPool(row map[string]any) bool {
	for _, key := range []string{"limit_dollars", "used_dollars", "remaining_dollars"} {
		if row[key] != nil {
			return true
		}
	}
	return false
}

func claudeMoneyDetail(row map[string]any) (string, error) {
	var parts []string
	for _, field := range []struct{ key, label string }{{"used_dollars", "used"}, {"limit_dollars", "limit"}, {"remaining_dollars", "remaining"}} {
		n, known, err := claudeNumeric(row, field.key)
		if err != nil {
			return "", err
		}
		if known {
			parts = append(parts, fmt.Sprintf("%s $%.6f", field.label, n))
		}
	}
	return strings.Join(parts, " · "), nil
}

func claudeDisplayWindows(usage map[string]any) ([]Window, error) {
	var out []Window
	keys := make([]string, 0, len(usage))
	for key := range usage {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Main account windows precede model-specific windows in summaries.
	keys = append([]string{"five_hour", "seven_day"}, keys...)
	seen := map[string]bool{}
	fable := claudeFableLimit(usage)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		value := usage[key]
		row, isObject := value.(map[string]any)
		flat := key == "five_hour" || key == "seven_day" || key == "iguana_necktie" || strings.HasPrefix(key, "five_hour_") || strings.HasPrefix(key, "seven_day_")
		dollars := isObject && claudeDollarPool(row)
		if !flat && !dollars || value == nil {
			continue
		}
		if !isObject {
			return nil, passiveInvalid(key)
		}
		// Window prefixes also cover metadata such as seven_day_breakdown.
		// A present null field still identifies a genuine unknown quota.
		if _, present := claudeFirstField(row, "utilization", "percent", "used_percent", "resets_at", "resetsAt"); !present && !dollars {
			continue
		}
		win, err := claudeReadWindow(row, key, "")
		if err != nil {
			return nil, err
		}
		if !dollars && fable != nil && (key == "iguana_necktie" || key == "seven_day_fable" || key == "seven_day_overage_included") {
			continue
		}
		if dollars {
			win.Label = Same(key)
			if key == "iguana_necktie" {
				win.Label = Text{"云端会话额度", "Cloud session credits"}
			}
			if key == "additional_credit_balance" {
				win.Label = Text{"追加余额", "Additional credit balance"}
			}
			win.Detail, err = claudeMoneyDetail(row)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, win)
	}
	return claudeLimitsWindows(usage, out, fable)
}

func claudeLimitsWindows(usage map[string]any, previous []Window, fable any) ([]Window, error) {
	out := slices.Clone(previous)
	value, exists := usage["limits"]
	if !exists || value == nil {
		return out, nil
	}
	limits, ok := value.([]any)
	if !ok {
		return nil, passiveInvalid("limits")
	}
	for _, value := range limits {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, passiveInvalid("limits[]")
		}
		key, ok := row["kind"].(string)
		if !ok || key == "" {
			return nil, passiveInvalid("limits.kind")
		}
		model, err := claudeScopeModel(row)
		if err != nil {
			return nil, err
		}
		if value := row["is_active"]; value != nil {
			if _, ok := value.(bool); !ok {
				return nil, passiveInvalid("limits.is_active")
			}
		}
		isFable := key == "weekly_fable" || key == "weekly_scoped" && claudeIsFable(model)
		win, err := claudeReadWindow(row, key, model)
		if err != nil {
			return nil, err
		}
		if isFable && fable != nil && !reflect.DeepEqual(row, fable) {
			continue
		}

		replaced := false
		for i, old := range out {
			if old.Label == win.Label {
				if _, exists := claudeFirstField(row, "resets_at", "resetsAt"); !exists {
					win.ResetAt = old.ResetAt
				}
				out[i] = win
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, win)
		}
	}
	return out, nil
}

func claudeIsFable(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))
	return name == "fable" || name == "fable5"
}

func claudeScopeModel(row map[string]any) (string, error) {
	value := row["scope"]
	if value == nil {
		return "", nil
	}
	scope, ok := value.(map[string]any)
	if !ok {
		return "", passiveInvalid("limits.scope")
	}
	value = scope["model"]
	if value == nil {
		return "", nil
	}
	model, ok := value.(map[string]any)
	if !ok {
		return "", passiveInvalid("limits.scope.model")
	}
	value = model["display_name"]
	if value == nil {
		return "", nil
	}
	name, ok := value.(string)
	if !ok {
		return "", passiveInvalid("limits.scope.model.display_name")
	}
	return strings.TrimSpace(name), nil
}

func claudeFirstField(row map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if _, exists := row[key]; exists {
			return key, true
		}
	}
	return "", false
}
