package quota

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func passiveCodex(signals map[string]string, observed time.Time) (*Result, error) {
	names := passiveCodexPrefixes(signals)
	result := &Result{Plan: codexPlanLabel(signals["x-codex-plan-type"])}
	for _, prefix := range names {
		windows, err := passiveCodexWindows(signals, prefix, observed)
		if err != nil {
			return nil, err
		}
		if len(windows) == 0 {
			continue
		}
		var title Text
		if prefix != "x-codex-" {
			name := strings.TrimSuffix(strings.TrimPrefix(prefix, "x-codex-"), "-")
			if display := strings.TrimSpace(signals[prefix+"limit-name"]); display != "" {
				name = display
			}
			title = Text{"附加限额 · " + name, "Additional limit · " + name}
			if name == "code-review" {
				title = Text{"代码审查", "Code review"}
			}
			for i := range windows {
				windows[i].Kind = KindOther
				windows[i].Label = Text{title.ZH + " · " + windows[i].Label.ZH, title.EN + " · " + windows[i].Label.EN}
			}
		}
		result.Groups = append(result.Groups, Group{Title: title, Windows: windows})
	}
	if len(result.Groups) == 0 {
		return nil, nil
	}
	return result, nil
}

func passiveCodexPrefixes(signals map[string]string) []string {
	prefixes := map[string]bool{"x-codex-": true}
	for key := range signals {
		for _, suffix := range []string{
			"primary-used-percent", "primary-window-minutes", "primary-reset-at", "primary-reset-after-seconds",
			"secondary-used-percent", "secondary-window-minutes", "secondary-reset-at", "secondary-reset-after-seconds",
		} {
			if strings.HasPrefix(key, "x-codex-") && strings.HasSuffix(key, "-"+suffix) {
				prefixes[strings.TrimSuffix(key, suffix)] = true
			}
		}
	}
	names := []string{"x-codex-"}
	for prefix := range prefixes {
		if prefix != "x-codex-" {
			names = append(names, prefix)
		}
	}
	sort.Strings(names[1:])
	return names
}

func passiveCodexWindows(signals map[string]string, prefix string, observed time.Time) ([]Window, error) {
	var windows []Window
	for _, period := range []string{"primary", "secondary"} {
		base := prefix + period + "-"
		used, known, err := passiveNumber(signals, base+"used-percent", 100)
		if err != nil {
			return nil, err
		}
		minutes, hasMinutes, err := passiveNumber(signals, base+"window-minutes", 1<<53)
		if err != nil {
			return nil, err
		}
		if hasMinutes && (minutes == 0 || minutes != math.Trunc(minutes)) {
			return nil, passiveInvalid(base + "window-minutes")
		}
		reset, err := passiveCodexReset(signals, base, observed)
		if err != nil {
			return nil, err
		}
		if !known && reset.IsZero() {
			continue
		}
		label, kind := passiveCodexLabel(period, minutes, hasMinutes)
		remaining := 0.0
		if known {
			remaining = 1 - used/100
		}
		windows = append(windows, Window{Label: label, Kind: kind, Known: known, Remaining: remaining, ResetAt: reset})
	}
	return windows, nil
}

func passiveCodexLabel(period string, minutes float64, hasMinutes bool) (Text, WindowKind) {
	label, kind := Text{"主要限额", "Primary limit"}, KindOther
	if period == "secondary" {
		label = Text{"次要限额", "Secondary limit"}
	}
	switch {
	case minutes == 300:
		label, kind = Text{"5 小时限额", "5-hour limit"}, KindShort
	case minutes == 10080:
		label, kind = Text{"周限额", "Weekly limit"}, KindLong
	case minutes >= codexMinMonthSeconds/60 && minutes <= codexMaxMonthSeconds/60:
		label, kind = Text{"月度限额", "Monthly limit"}, KindLong
	case hasMinutes:
		label = Text{fmt.Sprintf("%g 分钟限额", minutes), fmt.Sprintf("%g-minute limit", minutes)}
	}
	return label, kind
}

func passiveCodexReset(signals map[string]string, base string, observed time.Time) (time.Time, error) {
	absolute, err := passiveUnix(signals, base+"reset-at")
	if err != nil {
		return time.Time{}, err
	}
	// Validate both fields even when the absolute reset takes precedence.
	seconds, hasRelative, err := passiveNumber(signals, base+"reset-after-seconds", float64((1<<63-1)/int64(time.Second)))
	if err != nil {
		return time.Time{}, err
	}
	if !absolute.IsZero() {
		return absolute, nil
	}
	if hasRelative {
		return observed.Add(time.Duration(seconds * float64(time.Second))), nil
	}
	return time.Time{}, nil
}
