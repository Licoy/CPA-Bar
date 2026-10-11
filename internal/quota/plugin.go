package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// pluginProvider reads a quota through the plugin that serves it, at
// GET /plugins/<id>/quota, in CPA's normalized shape.
func pluginProvider(id string) Provider {
	return Provider{
		ID: "plugin:" + id,
		Fetch: func(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
			raw, err := c.PluginQuota(ctx, id, cr.AuthIndex)
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("the plugin's quota is not a JSON object: %w", err)
			}
			return normalized(m), nil
		},
	}
}

// normalized reads CPA's normalized quota: a subscription, a summary of
// metrics and groups of buckets. Antigravity answers in the same shape.
func normalized(m map[string]any) *Result {
	r := &Result{Groups: summaryGroups(m)}
	sub := get(m, "subscription")
	for _, k := range []string{"tierName|tier_name", "plan", "tierId|tier_id"} {
		if s := str(get(sub, k)); s != "" {
			r.Plan = s
			break
		}
	}
	metrics, _ := get(m, "summary").([]any)
	for _, x := range metrics {
		if f, ok := metricFact(x); ok {
			r.Facts = append(r.Facts, f)
		}
	}
	return r
}

// metricFact reads one summary metric: key, label and a finite number,
// with an optional unit, shown as a number or in its currency.
func metricFact(x any) (Fact, bool) {
	key, label := str(get(x, "key")), str(get(x, "label"))
	v, ok := get(x, "value").(float64)
	if key == "" || label == "" || !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return Fact{}, false
	}
	val := strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
	if str(get(x, "format")) == "currency" {
		if cur := str(get(x, "currency")); len(cur) == 3 && strings.ToUpper(cur) == cur {
			val = money(v, cur)
		}
	}
	if unit := str(get(x, "unit")); unit != "" {
		val += " " + unit
	}
	return Fact{Label: Same(label), Value: val}, true
}

// money writes an amount in a currency: a symbol for the common ones, the
// code otherwise.
func money(v float64, cur string) string {
	amount := strconv.FormatFloat(v, 'f', 2, 64)
	switch cur {
	case "USD":
		return "$" + amount
	case "EUR":
		return "€" + amount
	case "GBP":
		return "£" + amount
	case "CNY", "JPY":
		return "¥" + amount
	}
	return amount + " " + cur
}

// bucketOrder puts the five-hour limits before the weekly ones.
var bucketOrder = map[string]int{"5h": 0, "five-hour": 0, "five_hour": 0, "weekly": 1, "week": 1}

// orderKind is the kind of the windows bucketOrder knows.
var orderKind = map[int]WindowKind{0: KindShort, 1: KindLong}

// summaryGroups reads groups of buckets, keeping the buckets that state a
// remaining fraction (a number or "NN%"), five-hour before weekly.
func summaryGroups(m map[string]any) []Group {
	raw, _ := get(m, "groups").([]any)
	var out []Group
	for gi, g := range raw {
		title := str(get(g, "displayName|display_name"))
		if title == "" {
			title = fmt.Sprintf("Quota Group %d", gi+1)
		}
		type bucket struct {
			w     Window
			order int
			label string
		}
		var bs []bucket
		buckets, _ := get(g, "buckets").([]any)
		for bi, b := range buckets {
			frac, ok := fraction(get(b, "remainingFraction|remaining_fraction"))
			if !ok {
				continue
			}
			window := str(get(b, "window"))
			label := str(get(b, "displayName|display_name"))
			if label == "" {
				label = str(get(b, "description"))
			}
			if label == "" {
				label = window
			}
			if label == "" {
				label = fmt.Sprintf("bucket %d", bi+1)
			}
			order, known := bucketOrder[strings.ToLower(window)]
			if !known {
				order = math.MaxInt
			}
			bs = append(bs, bucket{
				w:     Window{Label: Same(label), Remaining: clamp01(frac), Known: true, ResetAt: instant(get(b, "resetTime|reset_time")), Kind: orderKind[order]},
				order: order, label: label,
			})
		}
		if len(bs) == 0 {
			continue
		}
		slices.SortStableFunc(bs, func(a, b bucket) int {
			if a.order != b.order {
				if a.order < b.order {
					return -1
				}
				return 1
			}
			return strings.Compare(a.label, b.label)
		})
		grp := Group{Title: Same(title), Note: Same(str(get(g, "description")))}
		for _, b := range bs {
			grp.Windows = append(grp.Windows, b.w)
		}
		out = append(out, grp)
	}
	return out
}

// fraction reads a remaining fraction: a number, or text like "65%".
func fraction(v any) (float64, bool) {
	if f, ok := num(v); ok {
		return f, true
	}
	if s := str(v); strings.HasSuffix(s, "%") {
		if f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64); err == nil {
			return f / 100, true
		}
	}
	return 0, false
}
