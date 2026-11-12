package quota

import (
	"fmt"
	"strings"
)

func claudeUsageFacts(usage map[string]any) ([]Fact, []Text, error) {
	var facts []Fact
	var notes []Text
	if value := usage["extra_usage"]; value != nil {
		extra, ok := value.(map[string]any)
		if !ok {
			return nil, nil, passiveInvalid("extra_usage")
		}
		var err error
		facts, notes, err = claudeExtraFacts(extra)
		if err != nil {
			return nil, nil, err
		}
	}
	// Other cedar_ember payloads are credits, not reset grants.
	if block, ok := usage["cedar_ember"].(map[string]any); ok {
		if grants, exists := block["grants"]; exists && grants != nil {
			rows, ok := grants.([]any)
			if !ok {
				return nil, nil, passiveInvalid("cedar_ember.grants")
			}
			left := 0.0
			for _, value := range rows {
				row, ok := value.(map[string]any)
				if !ok {
					return nil, nil, passiveInvalid("cedar_ember.grants[]")
				}
				n, known, err := claudeNumeric(row, "resets_left")
				if err != nil || !known || n != float64(int(n)) {
					return nil, nil, passiveInvalid("cedar_ember.grants.resets_left")
				}
				left += n
			}
			facts = append([]Fact{{Text{"剩余重置次数", "Resets remaining"}, fmt.Sprintf("%.0f", left)}}, facts...)
		}
	}
	return facts, notes, nil
}

func claudeExtraFacts(extra map[string]any) ([]Fact, []Text, error) {
	var facts []Fact
	var notes []Text
	window, err := claudeReadWindow(extra, "extra_usage", "")
	if err != nil {
		return nil, nil, err
	}
	if !window.ResetAt.IsZero() {
		facts = append(facts, Fact{Text{"额外用量重置时间", "Extra usage resets"}, window.ResetAt.Format("2006-01-02T15:04:05Z07:00")})
	}

	used, hasUsed, err := claudeNumeric(extra, "used_credits")
	if err != nil {
		return nil, nil, err
	}
	limit, hasLimit, err := claudeNumeric(extra, "monthly_limit")
	if err != nil {
		return nil, nil, err
	}
	currency := "$"
	if value, exists := extra["currency"]; exists {
		code, ok := value.(string)
		if !ok || strings.TrimSpace(code) == "" {
			return nil, nil, passiveInvalid("extra_usage.currency")
		}
		code = strings.ToUpper(strings.TrimSpace(code))
		if code != "USD" {
			currency = code + " "
		}
	}
	amount := ""
	switch {
	case hasUsed && hasLimit:
		amount = fmt.Sprintf("%s%.2f / %s%.2f", currency, used/100, currency, limit/100)
	case hasUsed:
		amount = fmt.Sprintf("%s%.2f", currency, used/100)
	case hasLimit:
		amount = fmt.Sprintf("%s%.2f", currency, limit/100)
	}
	if amount != "" {
		facts = append(facts, Fact{Text{"额外用量", "Extra usage"}, amount})
	}
	for _, entry := range []struct {
		key     string
		on, off Text
	}{
		{"is_enabled", Text{"额外用量已启用", "Extra usage enabled"}, Text{"额外用量已停用", "Extra usage disabled"}},
		{"user_disabled", Text{"用户已停用额外用量", "Extra usage disabled by user"}, Text{}},
		{"spend_limit_reached", Text{"额外用量额度已耗尽", "Extra usage spending limit reached"}, Text{}},
	} {
		if value := extra[entry.key]; value != nil {
			on, ok := value.(bool)
			if !ok {
				return nil, nil, passiveInvalid("extra_usage." + entry.key)
			}
			note := entry.off
			if on {
				note = entry.on
			}
			if !note.IsZero() {
				notes = append(notes, note)
			}
		}
	}
	if value := extra["disabled_reason"]; value != nil {
		reason, ok := value.(string)
		if !ok {
			return nil, nil, passiveInvalid("extra_usage.disabled_reason")
		}
		if reason != "" {
			notes = append(notes, Text{"额外用量停用原因：" + reason, "Extra usage disabled: " + reason})
		}
	}
	if n, known, err := claudeNumeric(extra, "utilization"); err != nil {
		return nil, nil, err
	} else if known {
		facts = append(facts, Fact{Text{"额外用量已使用", "Extra usage used"}, fmt.Sprintf("%.2f%%", n)})
	}
	return facts, notes, nil
}
