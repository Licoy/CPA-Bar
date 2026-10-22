package view

import "time"

func (a *App) cancelQuotaWake() {
	a.quotaWakeID++
	if a.quotaWake != nil {
		a.quotaWake.Stop()
	}
	a.quotaWake, a.quotaWakeAt = nil, time.Time{}
}

func (a *App) syncQuotaWake() {
	s := a.Svc.Settings()
	if s.QuotaRefreshMinutes != a.quotaWakeMinutes || s.PassiveQuota != a.quotaWakePassive {
		a.cancelQuotaWake()
		a.quotaWakeMinutes, a.quotaWakePassive = s.QuotaRefreshMinutes, s.PassiveQuota
	}
}

// Only displayed entries register a deadline; on expiry, rendering decides
// which accounts are still displayed. In-flight reads redraw on completion,
// so an overdue entry waiting for the concurrency bound needs no busy loop.
func (a *App) scheduleQuotaWake(e *quotaEntry, now time.Time) {
	if a.quitting || a.quotaWakePassive || e.fetching || e.acting {
		return
	}
	at := e.nextRead(time.Duration(a.quotaWakeMinutes) * time.Minute)
	if !at.After(now) || (!a.quotaWakeAt.IsZero() && !at.Before(a.quotaWakeAt)) {
		return
	}
	a.cancelQuotaWake()
	a.quotaWakeAt = at
	id := a.quotaWakeID
	a.quotaWake = time.AfterFunc(at.Sub(now), func() { runOnMain(func() { a.expireQuotaWake(id) }) })
}

func (a *App) expireQuotaWake(id uint64) {
	if id != a.quotaWakeID {
		return
	}
	a.cancelQuotaWake()
	a.syncQuotaWake()
	if a.quitting || a.quotaWakePassive {
		return
	}
	a.updateTray()
	if a.manager != nil && !a.manager.IsDestroyed() && a.manager.IsVisible() {
		a.manager.Invalidate()
	}
	if a.panel != nil && !a.panel.IsDestroyed() && a.panel.IsVisible() {
		a.panel.Invalidate()
	}
}
