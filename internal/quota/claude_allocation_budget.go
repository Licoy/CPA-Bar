package quota

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

type claudeAllocationSubscription struct {
	EvaluatedAt time.Time `json:"evaluated_at"`
	ObservedAt  time.Time `json:"observed_at"`
	CompleteAt  time.Time `json:"complete_at"`
	Complete    bool      `json:"complete"`
	MaxAge      float64   `json:"observation_max_age_seconds"`
	Exponent    *float64  `json:"weighted_exponent"`
	FiveWeight  bool      `json:"five_hour_weighting"`
	Reserve     struct {
		Five       *float64 `json:"five_hour_percent"`
		Week       *float64 `json:"seven_day_percent"`
		FiveJitter *float64 `json:"five_hour_jitter_percent"`
		WeekJitter *float64 `json:"seven_day_jitter_percent"`
	} `json:"reserve"`
	Windows []claudeAllocationWindow `json:"windows"`
}

type claudeAllocationWindow struct {
	Kind       string    `json:"kind"`
	Model      string    `json:"model"`
	Used       *float64  `json:"used_percent"`
	Reset      time.Time `json:"resets_at"`
	ObservedAt time.Time `json:"observed_at"`
	Inactive   bool      `json:"inactive"`
	Reserve    *float64  `json:"reserve_percent"`
}

func claudeAllocationWeight(cr cpa.Credential, input claudeAllocationInput, scope string, now time.Time, elapsed float64) (float64, error) {
	if input.err != nil {
		return 0, input.err
	}
	if cr.Disabled || cr.Status == cpa.StatusDisabled || (input.Weight != nil && *input.Weight <= 0) {
		return 0, nil
	}
	if cr.Unavailable && input.Cooldowns == nil {
		return 0, errors.New("credential availability is unknown")
	}
	if input.Cooldowns != nil {
		for _, cooldown := range *input.Cooldowns {
			if cooldown.Remaining == nil || *cooldown.Remaining < 0 || (cooldown.Scope != "credential" && cooldown.Scope != "model") {
				return 0, errors.New("invalid credential cooldown")
			}
			if *cooldown.Remaining > elapsed && (cooldown.Scope == "credential" || (scope != "" && strings.EqualFold(cooldown.Model, scope))) {
				return 0, nil
			}
		}
	}
	sub := input.Subscription
	if !validClaudeAllocationInstant(sub.EvaluatedAt) || !validClaudeAllocationInstant(now) {
		return 0, errors.New("missing server evaluation time")
	}
	return claudeAllocationBudget(sub, scope, now)
}

func claudeAllocationBudget(sub claudeAllocationSubscription, scope string, now time.Time) (float64, error) {
	completeAt := sub.CompleteAt
	if !validClaudeAllocationInstant(completeAt) {
		completeAt = sub.ObservedAt
	}
	complete := sub.Complete && validClaudeAllocationInstant(completeAt) && sub.MaxAge > 0 && now.Sub(completeAt).Seconds() < sub.MaxAge
	five, week, exhausted := false, false, false
	weeklyScore, fiveScore, fiveFactor := math.Inf(1), math.Inf(1), 1.0
	for _, window := range sub.Windows {
		if window.Model != "" && !strings.Contains(scope, strings.ToLower(window.Model)) {
			continue
		}
		if window.Kind != "five_hour" && window.Kind != "seven_day" {
			continue
		}
		five = five || window.Kind == "five_hour"
		week = week || (window.Kind == "seven_day" && window.Model == "")
		score, factor, err := claudeAllocationWindowBudget(window, sub, now)
		if err != nil {
			return 0, err
		}
		exhausted = exhausted || score <= 0
		if window.Kind == "seven_day" && !window.Inactive {
			weeklyScore = math.Min(weeklyScore, score)
		}
		if window.Kind == "five_hour" {
			fiveScore, fiveFactor = math.Min(fiveScore, score), math.Min(fiveFactor, factor)
		}
	}
	if !five || (!week && !complete) {
		return 0, errors.New("unknown quota window")
	}
	if math.IsInf(weeklyScore, 1) && complete {
		weeklyScore = fiveScore
	}
	if math.IsInf(weeklyScore, 1) {
		return 0, errors.New("unknown weekly budget")
	}
	if exhausted {
		return 0, nil
	}
	exponent := 1.0
	if sub.Exponent != nil {
		exponent = *sub.Exponent
	}
	if exponent < 0 || exponent > 5 {
		return 0, errors.New("invalid weighted exponent")
	}
	if exponent == 0 {
		exponent = 1
	}
	weight := math.Pow(weeklyScore, exponent)
	if sub.FiveWeight {
		weight *= fiveFactor
	}
	return weight, nil
}

func claudeAllocationWindowBudget(window claudeAllocationWindow, sub claudeAllocationSubscription, now time.Time) (float64, float64, error) {
	if sub.MaxAge <= 0 || !validClaudeAllocationInstant(window.ObservedAt) || now.Sub(window.ObservedAt).Seconds() >= sub.MaxAge {
		return 0, 0, errors.New("stale or missing quota observation")
	}
	reserve, err := claudeAllocationReserve(window, sub)
	if err != nil {
		return 0, 0, err
	}
	hours, used := 168.0, 0.0
	if window.Kind == "five_hour" {
		hours = 5
	}
	if !window.Inactive {
		if window.Used == nil || *window.Used < 0 || !validClaudeAllocationInstant(window.Reset) {
			return 0, 0, errors.New("unknown quota window")
		}
		// Match the upstream Web target: a fresh observation past reset has no
		// allocatable budget until refreshed, while stale observations stay unknown.
		if !window.Reset.After(now) {
			return 0, 0, nil
		}
		used, hours = math.Min(*window.Used, 100), math.Max(window.Reset.Sub(now).Hours(), 1)
	}
	remaining := math.Max(0, 100-used-reserve)
	factor := 0.0
	if reserve < 100 {
		factor = remaining / (100 - reserve)
	}
	return remaining / hours, factor, nil
}

func validClaudeAllocationInstant(stamp time.Time) bool {
	return stamp.After(time.Unix(0, 0))
}

func claudeAllocationReserve(window claudeAllocationWindow, sub claudeAllocationSubscription) (float64, error) {
	reserve := window.Reserve
	if reserve == nil {
		base, jitter := sub.Reserve.Week, sub.Reserve.WeekJitter
		if window.Kind == "five_hour" {
			base, jitter = sub.Reserve.Five, sub.Reserve.FiveJitter
		}
		if jitter != nil && *jitter != 0 {
			return 0, errors.New("missing effective quota reserve")
		}
		reserve = base
	}
	if reserve == nil {
		return 0, nil
	}
	if *reserve < 0 || *reserve > 100 {
		return 0, errors.New("invalid quota reserve")
	}
	return *reserve, nil
}
