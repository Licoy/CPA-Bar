package quota

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// ClaudeTargetAllocation is a target within this instance's weighted Claude pool.
// A nil Share is unknown, while a nil credential row means unsupported routing.
type ClaudeTargetAllocation struct {
	Scope string
	// Share is a percentage in [0, 100], not a fraction.
	Share *float64
	Err   error
}

type claudeAllocationInput struct {
	Subscription claudeAllocationSubscription `json:"claude_subscription"`
	Weight       *float64                     `json:"weight"`
	Cooldowns    *[]claudeAllocationCooldown  `json:"cooldowns"`
	err          error
}

type claudeAllocationCooldown struct {
	Scope     string   `json:"scope"`
	Model     string   `json:"model_key"`
	Remaining *float64 `json:"remaining_seconds"`
}

// ClaudeTargetAllocations uses normalized server observations only; manual usage
// responses do not establish authoritative completeness or window reserves.
func ClaudeTargetAllocations(credentials []cpa.Credential, receivedAt, now time.Time) [][]ClaudeTargetAllocation {
	result := make([][]ClaudeTargetAllocation, len(credentials))
	inputs := make(map[int]claudeAllocationInput)
	scopes := []string{""}
	anchor := time.Time{}
	for i, cr := range credentials {
		if ProviderKey(cr) != "claude" || !claudeAllocationEnabled(cr.Raw) {
			continue
		}
		var input claudeAllocationInput
		input.err = json.Unmarshal(cr.Raw, &input)
		inputs[i] = input
		if input.err != nil {
			continue
		}
		if validClaudeAllocationInstant(input.Subscription.EvaluatedAt) {
			anchor = input.Subscription.EvaluatedAt
		}
		for _, window := range input.Subscription.Windows {
			model := strings.ToLower(strings.TrimSpace(window.Model))
			if model != "" && !slices.Contains(scopes, model) {
				scopes = append(scopes, model)
			}
		}
	}
	serverNow := anchor.Add(max(now.Sub(receivedAt), 0))
	for _, scope := range scopes {
		rows := claudeAllocationShares(credentials, inputs, scope, serverNow, max(now.Sub(receivedAt).Seconds(), 0))
		for i, row := range rows {
			result[i] = append(result[i], row)
		}
	}
	return pruneClaudeAllocationScopes(result)
}

func claudeAllocationEnabled(raw json.RawMessage) bool {
	var root struct {
		Subscription struct {
			Enabled  bool   `json:"enabled"`
			Strategy string `json:"strategy"`
		} `json:"claude_subscription"`
	}
	return json.Unmarshal(raw, &root) == nil && root.Subscription.Enabled &&
		strings.EqualFold(strings.TrimSpace(root.Subscription.Strategy), "quota-aware-weighted")
}

func claudeAllocationShares(credentials []cpa.Credential, inputs map[int]claudeAllocationInput, scope string, serverNow time.Time, elapsed float64) map[int]ClaudeTargetAllocation {
	rows := make(map[int]ClaudeTargetAllocation)
	weights := make(map[int]float64)
	activePriority := math.Inf(-1)
	for i, input := range inputs {
		weight, err := claudeAllocationWeight(credentials[i], input, scope, serverNow, elapsed)
		rows[i] = ClaudeTargetAllocation{Scope: scope, Err: err}
		if err != nil {
			continue
		}
		weights[i] = weight
		if weight > 0 {
			activePriority = math.Max(activePriority, claudeAllocationPriority(credentials[i]))
		}
	}
	total := 0.0
	for i, weight := range weights {
		if claudeAllocationPriority(credentials[i]) == activePriority {
			total += weight
		}
	}
	for i, weight := range weights {
		row := rows[i]
		priority := claudeAllocationPriority(credentials[i])
		share := 0.0
		if priority == activePriority && total > 0 {
			share = weight / total * 100
		}
		row.Share = &share
		rows[i] = row
	}
	return rows
}

func claudeAllocationPriority(cr cpa.Credential) float64 {
	if cr.Priority != nil {
		return *cr.Priority
	}
	return 0
}

func pruneClaudeAllocationScopes(rows [][]ClaudeTargetAllocation) [][]ClaudeTargetAllocation {
	keep := map[string]bool{"": true}
	for _, entries := range rows {
		for _, row := range entries {
			base := entries[0].Share
			if (row.Share == nil) != (base == nil) ||
				(row.Share != nil && base != nil && math.Abs(*row.Share-*base) >= .005) {
				keep[row.Scope] = true
			}
		}
	}
	for i, entries := range rows {
		var selected []ClaudeTargetAllocation
		for _, row := range entries {
			if keep[row.Scope] {
				selected = append(selected, row)
			}
		}
		rows[i] = selected
	}
	return rows
}
