package quota

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// PassiveObservationMeta identifies the selected recorded data, excluding cache age
// and routing evaluation; its watermark never substitutes header time for usage time.
func PassiveObservationMeta(cr cpa.Credential) (string, time.Time, error) {
	if ProviderKey(cr) == "claude" {
		source, stamp, _, err := claudePassiveSource(cr.Raw)
		return source + "@" + stamp.Format(time.RFC3339Nano), stamp, err
	}
	_, stamp, err := passiveSnapshot(cr.Raw)
	var root map[string]json.RawMessage
	if json.Unmarshal(cr.Raw, &root) != nil {
		return string(cr.Raw), stamp, err
	}
	if stamp.IsZero() {
		var fields map[string]json.RawMessage
		if json.Unmarshal(root["quota"], &fields) == nil {
			stamp, _ = claudeObserved(fields["observed_at"], "quota.observed_at")
		}
	}
	var q any
	if json.Unmarshal(root["quota"], &q) != nil {
		return string(root["quota"]), stamp, err
	}
	data, _ := json.Marshal(q)
	return string(data), stamp, err
}

func claudePassiveSource(raw json.RawMessage) (string, time.Time, string, error) {
	if len(raw) == 0 {
		return "", time.Time{}, "", nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return "", time.Time{}, "", passiveInvalid("credential")
	}
	if block, ok := root["claude_subscription"]; ok && !jsonNull(block) {
		var sub map[string]json.RawMessage
		if json.Unmarshal(block, &sub) != nil || sub == nil {
			return string(block), time.Time{}, "", passiveInvalid("claude_subscription")
		}
		for _, key := range []string{"usage", "windows"} {
			data, exists := sub[key]
			if !exists || jsonNull(data) {
				continue
			}
			stampKey := "observed_at"
			if key == "usage" {
				stampKey = "usage_observed_at"
			}
			stamp, err := claudeObserved(sub[stampKey], "claude_subscription."+stampKey)
			var value any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return string(data), stamp, key, passiveInvalid("claude_subscription." + key)
			}
			if key == "windows" {
				value = stripClaudeAges(value)
			}
			canonical, _ := json.Marshal(value)
			return key + ":" + string(canonical), stamp, key, err
		}
	}
	signals, stamp, err := passiveSnapshot(raw)
	// Keep the verified legacy watermark even if signals are malformed.
	if stamp.IsZero() {
		var q map[string]json.RawMessage
		if json.Unmarshal(root["quota"], &q) == nil {
			stamp, _ = claudeObserved(q["observed_at"], "quota.observed_at")
		}
	}
	if signals == nil {
		return "invalid:" + string(root["quota"]), stamp, "", err
	}
	data, _ := json.Marshal(signals)
	return "signals:" + string(data), stamp, "signals", err
}

func jsonNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func claudeObserved(raw json.RawMessage, field string) (time.Time, error) {
	var stamp string
	if json.Unmarshal(raw, &stamp) != nil {
		return time.Time{}, passiveInvalid(field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || parsed.IsZero() {
		return time.Time{}, passiveInvalid(field)
	}
	return parsed, nil
}

func stripClaudeAges(value any) any {
	rows, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, len(rows))
	for i, row := range rows {
		if fields, ok := row.(map[string]any); ok {
			copied := make(map[string]any, len(fields))
			for key, value := range fields {
				if key != "age_seconds" {
					copied[key] = value
				}
			}
			result[i] = copied
		} else {
			result[i] = row
		}
	}
	return result
}

func passiveClaudeRecorded(cr cpa.Credential) (*Result, time.Time, error) {
	identity, stamp, kind, err := claudePassiveSource(cr.Raw)
	if err != nil || kind == "" {
		return nil, stamp, err
	}
	data := strings.TrimPrefix(identity, kind+":")
	if kind == "signals" {
		var signals map[string]string
		if err = json.Unmarshal([]byte(data), &signals); err != nil {
			return nil, stamp, err
		}
		result, err := passiveClaude(signals)
		return result, stamp, err
	}
	if kind == "windows" {
		result, err := claudeNormalized([]byte(data))
		return result, stamp, err
	}
	var usage map[string]any
	if json.Unmarshal([]byte(data), &usage) != nil || usage == nil {
		return nil, stamp, passiveInvalid("claude_subscription.usage")
	}
	result, err := claudeUsageResult(usage)
	return result, stamp, err
}

func claudeNormalized(data []byte) (*Result, error) {
	var rows []map[string]any
	if json.Unmarshal(data, &rows) != nil {
		return nil, passiveInvalid("claude_subscription.windows")
	}
	windows := make([]Window, 0, len(rows))
	for _, row := range rows {
		key, ok := row["kind"].(string)
		if !ok || key == "" {
			return nil, passiveInvalid("windows.kind")
		}
		model := ""
		if row["model"] != nil {
			model, ok = row["model"].(string)
			if !ok {
				return nil, passiveInvalid("windows.model")
			}
		}
		if row["inactive"] != nil {
			inactive, valid := row["inactive"].(bool)
			if !valid {
				return nil, passiveInvalid("windows.inactive")
			}
			if inactive {
				continue
			}
		}
		win, err := claudeReadWindow(row, key, model)
		if err != nil {
			return nil, err
		}
		windows = append(windows, win)
	}
	result := &Result{Groups: []Group{{Windows: windows}}}
	if len(windows) == 0 {
		result.Notes = []Text{{"暂无已记录的额度数据", "No recorded quota data"}}
	}
	return result, nil
}
