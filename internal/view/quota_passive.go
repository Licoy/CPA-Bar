package view

import (
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

// A cache belongs to one connection and mode, so responses started before
// a mode or connection change cannot publish into the current display.
func (a *App) quotaCurrent(q *quotaInstance) bool {
	s := a.Svc.Settings()
	return a.quotas[q.id] == q && q.passive == s.PassiveQuota && q.claudePassive == s.ClaudePassiveQuota
}

func passiveQuota(s store.Settings, cr cpa.Credential) bool {
	if quota.ProviderKey(cr) == "claude" {
		return s.ClaudePassiveQuota
	}
	return s.PassiveQuota
}

func (q *quotaInstance) passiveFor(cr cpa.Credential) bool {
	return passiveQuota(store.Settings{PassiveQuota: q.passive, ClaudePassiveQuota: q.claudePassive}, cr)
}

func (e *quotaEntry) observe(cr cpa.Credential) {
	res, at, err := quota.Passive(cr)
	identity, versionAt, metaErr := quota.PassiveObservationMeta(cr)
	if metaErr != nil {
		res, err = nil, metaErr
	}
	// The server version remains usable even when its signals are invalid;
	// compare server clocks only and keep the latest watermark on missing data.
	if !versionAt.IsZero() && versionAt.Before(e.observedAt) {
		return
	}
	observation := "observed:" + identity
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
