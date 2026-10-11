package quota

import "testing"

// Summary takes the first known short and long limits, wherever they are,
// and falls back on the first known limit when none is marked.
func TestSummary(t *testing.T) {
	r := &Result{Groups: []Group{
		{Windows: []Window{{Kind: KindOther, Known: true, Remaining: 0.1}, {Kind: KindShort, Remaining: 0.2}}},
		{Windows: []Window{{Kind: KindLong, Known: true, Remaining: 0.6}, {Kind: KindShort, Known: true, Remaining: 0.9}}},
	}}
	short, long := r.Summary()
	if short == nil || short.Remaining != 0.9 || long == nil || long.Remaining != 0.6 {
		t.Errorf("summary = %+v, %+v", short, long)
	}

	r = &Result{Groups: []Group{{Windows: []Window{{Known: false}, {Known: true, Remaining: 0.4}}}}}
	if short, long := r.Summary(); short != nil || long == nil || long.Remaining != 0.4 {
		t.Errorf("unmarked summary = %+v, %+v", short, long)
	}
	if short, long := (*Result)(nil).Summary(); short != nil || long != nil {
		t.Error("a nil result has a summary")
	}
}

// The providers mark their five-hour and weekly limits.
func TestWindowKinds(t *testing.T) {
	claude := claudeUsageWindows(map[string]any{
		"five_hour":      map[string]any{"utilization": 10.0},
		"seven_day":      map[string]any{"utilization": 40.0},
		"seven_day_opus": map[string]any{"utilization": 90.0},
	})
	if len(claude) != 3 || claude[0].Kind != KindShort || claude[1].Kind != KindLong || claude[2].Kind != KindOther {
		t.Errorf("claude kinds: %+v", claude)
	}
	if s, l := (&Result{Groups: []Group{{Windows: claude}}}).Summary(); s.Remaining != 0.9 || l.Remaining != 0.6 {
		t.Errorf("claude summary %v %v", s.Remaining, l.Remaining)
	}
	if !kimiShort(map[string]any{"window": nil}, map[string]any{}, map[string]any{"duration": 300.0, "timeUnit": "TIME_UNIT_MINUTE"}) {
		t.Error("a 300-minute kimi window is not short")
	}
	if kimiShort(nil, nil, map[string]any{"duration": 7.0, "timeUnit": "TIME_UNIT_DAY"}) {
		t.Error("a 7-day kimi window is short")
	}
}
