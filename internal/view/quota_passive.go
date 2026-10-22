package view

import (
	"encoding/json"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
)

// A cache belongs to one connection and mode, so responses started before
// a mode or connection change cannot publish into the current display.
func (a *App) quotaCurrent(q *quotaInstance) bool {
	return a.quotas[q.id] == q && q.passive == a.Svc.Settings().PassiveQuota
}

func (e *quotaEntry) observe(cr cpa.Credential) {
	res, at, err := quota.Passive(cr)
	var envelope map[string]any
	raw := cr.Raw
	versionAt := at
	if json.Unmarshal(cr.Raw, &envelope) == nil {
		raw, _ = json.Marshal(envelope["quota"])
		if recorded, ok := envelope["quota"].(map[string]any); ok {
			stamp, _ := recorded["observed_at"].(string)
			versionAt, _ = time.Parse(time.RFC3339Nano, stamp)
		}
	}
	// The server version remains usable even when its signals are invalid;
	// compare server clocks only and keep the latest watermark on missing data.
	if !versionAt.IsZero() && versionAt.Before(e.observedAt) {
		return
	}
	observation := "observed:" + string(raw)
	if e.observation == observation {
		return
	}
	e.observation = observation
	if !versionAt.IsZero() {
		e.observedAt = versionAt
	}
	e.res, e.at, e.err = res, at, err
	e.failures = 0
	e.provider = quota.Provider{}
	e.fromCPA = true
}

func (e *quotaEntry) sourceText() string {
	source := T("手动查询", "Manual query")
	if e.fromCPA {
		source = T("CPA 观测", "CPA observation")
	}
	if !e.at.IsZero() {
		source += " · " + e.at.Local().Format("01/02 15:04:05")
	}
	return source
}
