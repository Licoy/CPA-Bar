package view

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
)

// The quota of the accounts, shown on the accounts page and in the panel.
// Each instance keeps a cache of what was read, by auth index, shared by
// both windows. Only the rows on screen read their quota, a few at a time,
// again at the configured quota interval; failures wait longer and longer.
// Actions (a reset that spends a limited grant…) run only when the user
// confirms them, and are never retried.

const (
	// Plugin discovery keeps its own metadata cache interval.
	quotaPluginsTTL = 5 * time.Minute
	// quotaParallel bounds the reads running at once per instance.
	quotaParallel = 3
	// quotaBackoff is the first wait after a failure, doubled after each
	// further failure up to quotaBackoffMax.
	quotaBackoff    = 2 * time.Minute
	quotaBackoffMax = 30 * time.Minute
)

// quotaInstance is the quota cache of an instance.
type quotaInstance struct {
	// client is the client the cache was read with: a new one (another
	// address or key) starts afresh.
	id      string
	passive bool
	client  *cpa.Client
	entries map[string]*quotaEntry
	running int

	plugins     []cpa.Plugin
	pluginsAt   time.Time
	pluginsErr  error
	pluginsBusy bool
}

// quotaEntry is the quota of one account.
type quotaEntry struct {
	fetching      bool
	res           *quota.Result
	at            time.Time // when res was read
	err           error     // the last read's failure
	failedAt      time.Time
	failures      int
	provider      quota.Provider
	observation   string
	observedAt    time.Time
	fromCPA       bool
	manualPending *cpa.Credential

	acting bool
	// actMsg says how the last action went; actErr marks a failure,
	// actUnknown an action that may or may not have happened.
	actMsg     string
	actErr     bool
	actUnknown bool

	// ask is the action awaiting confirmation, "" for none.
	ask     string
	askOpen bool
}

func (a *App) quotaCache(id string, cl *cpa.Client) *quotaInstance {
	if a.quotas == nil {
		a.quotas = map[string]*quotaInstance{}
	}
	q := a.quotas[id]
	if q == nil || q.client != cl || q.passive != a.Svc.Settings().PassiveQuota {
		q = &quotaInstance{id: id, passive: a.Svc.Settings().PassiveQuota, client: cl, entries: map[string]*quotaEntry{}}
		a.quotas[id] = q
	}
	return q
}

// quotaRedraw redraws the menu bar item and the windows that may show a
// quota. It runs on the
// main thread.
func (a *App) quotaRedraw() {
	a.updateTray()
	if a.panel != nil && !a.panel.IsDestroyed() {
		a.panel.Invalidate()
	}
	if a.manager != nil && !a.manager.IsDestroyed() {
		a.manager.Invalidate()
	}
}

// quotaEntryFor returns the cached quota of an account and starts a read
// when it is due. ok is false when the account has no quota to read.
func (a *App) quotaEntryFor(s core.InstanceSnapshot, cr cpa.Credential, now time.Time) (*quotaEntry, *quotaInstance, bool) {
	a.syncQuotaWake()
	if !quota.Supported(cr) {
		return nil, nil, false
	}
	cl, err := a.Svc.Client(s.Profile.ID)
	if err != nil || cl == nil {
		return nil, nil, false
	}
	q := a.quotaCache(s.Profile.ID, cl)
	e := q.entries[cr.AuthIndex]
	if e == nil {
		e = &quotaEntry{}
		q.entries[cr.AuthIndex] = e
	}
	if q.passive {
		e.observe(cr)
	} else if e.due(now, time.Duration(a.Svc.Settings().QuotaRefreshMinutes)*time.Minute) {
		a.quotaFetch(q, e, cr, false)
	}
	if !q.passive {
		a.scheduleQuotaWake(e, now)
	}
	return e, q, true
}

// due reports that an entry should be read again.
func (e *quotaEntry) due(now time.Time, interval time.Duration) bool {
	if e.fetching || e.acting {
		return false
	}
	return !now.Before(e.nextRead(interval))
}

func (e *quotaEntry) nextRead(interval time.Duration) time.Time {
	if e.failures > 0 {
		wait := quotaBackoff << min(e.failures-1, 8)
		return e.failedAt.Add(min(wait, quotaBackoffMax))
	}
	if e.at.IsZero() {
		return time.Time{}
	}
	return e.at.Add(interval)
}

// quotaFetch reads an account's quota. manual reads skip the bound on
// parallel reads.
func (a *App) quotaFetch(q *quotaInstance, e *quotaEntry, cr cpa.Credential, manual bool) {
	if !a.quotaCurrent(q) || (!manual && q.passive) || e.fetching || e.acting || (!manual && q.running >= quotaParallel) {
		return
	}
	if quota.NeedsPlugins(cr) && !a.quotaPlugins(q, manual) {
		if manual && q.pluginsBusy {
			pending := cr
			e.manualPending = &pending
		}
		if q.pluginsErr != nil && !q.pluginsBusy {
			e.err, e.failedAt = q.pluginsErr, time.Now()
			e.failures++
		}
		return
	}
	p, ok := quota.For(cr, q.plugins)
	if !ok {
		e.err = errors.New(T("服务器上没有为此账号提供额度的插件", "No plugin on the server reports this account's quota"))
		e.failedAt = time.Now()
		e.failures++
		return
	}
	e.fetching = true
	q.running++
	cl := q.client
	observation := e.observation
	go func() {
		res, err := p.Fetch(context.Background(), cl, cr)
		runOnMain(func() {
			q.running--
			e.fetching = false
			if !a.quotaCurrent(q) {
				return
			}
			if e.observation != observation {
				a.quotaRedraw()
				return
			}
			e.provider = p
			if err != nil {
				e.err, e.failedAt = err, time.Now()
				e.failures++
			} else {
				e.res, e.at, e.err, e.failures = res, time.Now(), nil, 0
				e.fromCPA = false
			}
			a.quotaRedraw()
		})
	}()
}

// quotaPlugins reports that the server's plugin list is known, reading it
// when it is missing or old.
func (a *App) quotaPlugins(q *quotaInstance, manual bool) bool {
	if !a.quotaCurrent(q) || (q.passive && !manual) {
		return false
	}
	now := time.Now()
	known := !q.pluginsAt.IsZero() && q.pluginsErr == nil
	switch {
	case q.pluginsBusy:
		return known
	case known && now.Sub(q.pluginsAt) < quotaPluginsTTL:
		return true
	case q.pluginsErr != nil && !manual && now.Sub(q.pluginsAt) < quotaBackoff:
		return false
	}
	q.pluginsBusy = true
	cl := q.client
	go func() {
		list, err := cl.Plugins(context.Background())
		runOnMain(func() {
			q.pluginsBusy = false
			if !a.quotaCurrent(q) {
				return
			}
			q.pluginsAt = time.Now()
			q.pluginsErr = err
			if err == nil {
				q.plugins = list.Plugins
			}
			for _, e := range q.entries {
				if cr := e.manualPending; cr != nil {
					e.manualPending = nil
					if err != nil {
						e.err = err
					} else {
						a.quotaFetch(q, e, *cr, true)
					}
				}
			}
			a.quotaRedraw()
		})
	}()
	return known
}

// quotaAct runs a confirmed action, then reads the quota again.
func (a *App) quotaAct(q *quotaInstance, e *quotaEntry, cr cpa.Credential, action string) {
	p := e.provider
	if e.acting || e.fetching || p.Act == nil {
		return
	}
	e.acting = true
	e.actMsg, e.actErr, e.actUnknown = "", false, false
	cl := q.client
	go func() {
		done, err := p.Act(context.Background(), cl, cr, action)
		runOnMain(func() {
			e.acting = false
			if !a.quotaCurrent(q) {
				return
			}
			var m *quota.Msg
			switch {
			case err == nil:
				e.actMsg = quotaText(done)
			case errors.As(err, &m):
				e.actMsg, e.actErr, e.actUnknown = quotaText(m.Text), true, m.Unknown
			case IsIndeterminate(err):
				e.actMsg, e.actErr, e.actUnknown = ErrorText(err), true, true
			default:
				e.actMsg, e.actErr = ErrorText(err), true
			}
			// Show what the action changed; a read changes nothing.
			e.at = time.Time{}
			a.quotaFetch(q, e, cr, true)
			a.quotaRedraw()
		})
	}()
}

func quotaText(t quota.Text) string {
	if English() {
		return t.EN
	}
	return t.ZH
}

// quotaColor is the color of what is left: green, amber, then red.
func quotaColor(t *ui.Theme, remaining float64) ui.Color {
	return [...]ui.Color{t.Success, t.Warning, t.Danger}[quotaTier(remaining)]
}

// quotaTier is how much of a limit is left: 0 from 70%, 1 from 30%, else
// 2. The panel and the menu bar color it alike.
func quotaTier(remaining float64) int {
	switch {
	case remaining >= 0.7:
		return 0
	case remaining >= 0.3:
		return 1
	}
	return 2
}

// quotaPercent is the share left, rounded down so that a nearly spent
// limit never reads 100%.
func quotaPercent(w quota.Window) string {
	if !w.Known {
		if w.Detail != "" {
			return w.Detail
		}
		return "—"
	}
	if w.Remaining <= 0 {
		return T("已用尽", "Used up")
	}
	return fmt.Sprintf(T("剩余 %d%%", "%d%% left"), int(math.Floor(w.Remaining*100+1e-9)))
}

// quotaReset says when a limit refills: "10/11 23:10 · 4 小时 5 分钟后".
func quotaReset(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := at.Sub(now)
	var rel string
	switch {
	case d <= 0:
		rel = T("即将刷新", "refilling")
	case d < time.Hour:
		rel = fmt.Sprintf(T("%d 分钟后", "in %dm"), int(d.Minutes())+1)
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		rel = fmt.Sprintf(T("%d 小时 %d 分钟后", "in %dh %dm"), h, m)
	default:
		days, h := int(d.Hours())/24, int(d.Hours())%24
		rel = fmt.Sprintf(T("%d 天 %d 小时后", "in %dd %dh"), days, h)
	}
	return at.Local().Format("01/02 15:04") + " · " + rel
}

// quotaPlan shows an account's plan, once its quota is read, beside its
// name; a long name gives way, not the plan.
func (a *App) quotaPlan(c *ui.Context, s core.InstanceSnapshot, cr cpa.Credential, now time.Time) {
	if e, _, ok := a.quotaEntryFor(s, cr, now); ok && e.res != nil && e.res.Plan != "" {
		Pill(c.Key("plan"), e.res.Plan, TintPurple.Text(c.Theme())).Shrink(0)
	}
}

// quotaView shows an account's quota: the facts, each group of
// limits with its bars, and the refresh and the provider's actions. cols
// is how many limits share a line. It shows nothing for accounts without
// a quota.
func (a *App) quotaView(c *ui.Context, s core.InstanceSnapshot, cr cpa.Credential, now time.Time, cols int) {
	e, q, ok := a.quotaEntryFor(s, cr, now)
	if !ok {
		return
	}
	t := c.Theme()
	// The panel is narrow: there the buttons get a line of their own,
	// under the limits.
	compact := cols < 3
	r := e.res
	buttons := func() {
		if r != nil {
			for i, act := range r.Actions {
				reason := quotaText(act.Reason)
				b := BusyPillButton(c.Key(fmt.Sprint("act", i)), nil, quotaText(act.Label), e.acting && act.ID == e.ask, act.Disabled || e.acting || e.fetching || e.provider.Act == nil)
				if reason != "" {
					b.Tooltip(reason)
				}
				if b.Clicked() {
					e.ask, e.askOpen = act.ID, true
				}
			}
		}
		if BusyPillButton(c.Key("refresh"), icRefresh, T("刷新额度", "Refresh Quota"), e.fetching, e.acting).Clicked() {
			e.failures = 0
			a.quotaFetch(q, e, cr, true)
		}
	}
	ui.Column(c.Key("quota")).Gap(8).Padding(8, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(pal(t).Line).Children(func() {
		// Header: facts, refresh and actions. The plan sits beside the
		// account's name (quotaPlan); in the panel, without facts, there is
		// no header.
		facts := r != nil && len(r.Facts) > 0
		if facts || !compact {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				if facts {
					for i, f := range r.Facts {
						ui.Text(c.Key(fmt.Sprint("fact", i)), quotaText(f.Label)+" "+f.Value).FontSize(12).TextColor(t.TextMuted).SingleLine()
					}
				}
				if !compact {
					ui.Box(c).Grow(1)
					buttons()
				}
			})
		}

		switch {
		case r == nil && e.err != nil:
			ui.Text(c, fmt.Sprintf(T("读取额度失败：%s", "Could not read the quota: %s"), quotaErrText(e.err))).FontSize(12).TextColor(t.Danger).MaxLines(3).Selectable()
		case r == nil && q.passive && !e.fetching:
			ui.Text(c, T("等待 CPA 额度数据", "Waiting for CPA quota data")).FontSize(12).TextColor(t.TextMuted)
		case r == nil:
			ui.Text(c, T("正在读取额度…", "Reading the quota…")).FontSize(12).TextColor(t.TextMuted)
		default:
			if e.err != nil {
				ui.Text(c, fmt.Sprintf(T("刷新失败（%s），以下是 %s 读取的额度", "Refresh failed (%s); the quota below was read %s"), quotaErrText(e.err), Ago(e.at, now))).
					FontSize(11).TextColor(t.Warning).MaxLines(2).Selectable()
			}
			if q.passive {
				ui.Text(c, e.sourceText()).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
			quotaGroups(c, r, now, cols)
			for i, n := range r.Notes {
				ui.Text(c.Key(fmt.Sprint("note", i)), quotaText(n)).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			}
		}
		if compact {
			ui.Row(c.Key("buttons")).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1)
				buttons()
			})
		}
		if e.actMsg != "" {
			col := t.Success
			if e.actErr {
				col = t.Danger
				if e.actUnknown {
					col = t.Warning
				}
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.actMsg).FontSize(12).TextColor(col).MaxLines(3).Selectable().Grow(1).Basis(0)
				if PillButton(c.Key("dismiss"), nil, T("知道了", "Dismiss"), false).Clicked() {
					e.actMsg = ""
				}
			})
		}
	})
	a.quotaConfirm(c, s, q, e, cr)
}

// quotaGroups lays out the limits, cols to a line, under their group's
// title and note.
func quotaGroups(c *ui.Context, r *quota.Result, now time.Time, cols int) {
	t := c.Theme()
	if len(r.Groups) == 0 {
		ui.Text(c, T("没有额度数据", "No quota data")).FontSize(12).TextColor(t.TextMuted)
		return
	}
	cols = max(cols, 1)
	for gi, g := range r.Groups {
		ui.Column(c.Key(fmt.Sprint("group", gi))).Gap(6).Children(func() {
			if !g.Title.IsZero() {
				ui.Text(c, quotaText(g.Title)).FontSize(11).FontWeight(600).TextColor(t.TextMuted).SingleLine()
			}
			if !g.Note.IsZero() {
				ui.Text(c, quotaText(g.Note)).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}
			for start := 0; start < len(g.Windows); start += cols {
				ui.Row(c.Key(fmt.Sprint("line", start))).Gap(14).Children(func() {
					for i := start; i < start+cols; i++ {
						if i >= len(g.Windows) {
							ui.Box(c.Key(fmt.Sprint("pad", i))).Grow(1).Basis(0)
							continue
						}
						quotaWindow(c.Key(fmt.Sprint("w", i)), g.Windows[i], now)
					}
				})
			}
		})
	}
}

// quotaWindow is one limit: its label and what is left, the bar, and when
// it refills.
func quotaWindow(c *ui.Context, w quota.Window, now time.Time) {
	t := c.Theme()
	col := t.TextMuted
	if w.Known {
		col = quotaColor(t, w.Remaining)
	}
	ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, quotaText(w.Label)).FontSize(12).FontWeight(500).SingleLine().Grow(1).Basis(0)
			ui.Text(c, quotaPercent(w)).FontSize(12).FontWeight(600).TextColor(col).FontFeatures("tnum").SingleLine()
		})
		frac := w.Remaining
		if !w.Known {
			frac = 0
		}
		Bar(c, frac, col)
		reset := quotaReset(w.ResetAt, now)
		if w.Known && w.Detail != "" {
			if reset != "" {
				reset += " · "
			}
			reset += w.Detail
		}
		if reset != "" {
			ui.Text(c, reset).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
		}
	})
}

// quotaConfirm asks before an action runs. The message names the instance
// and the account, as every write does.
func (a *App) quotaConfirm(c *ui.Context, s core.InstanceSnapshot, q *quotaInstance, e *quotaEntry, cr cpa.Credential) {
	if !e.askOpen || e.res == nil {
		return
	}
	var act *quota.Action
	for i := range e.res.Actions {
		if e.res.Actions[i].ID == e.ask {
			act = &e.res.Actions[i]
		}
	}
	if act == nil || act.Disabled {
		e.askOpen = false
		return
	}
	title := quotaText(act.Label)
	msg := fmt.Sprintf(T("实例“%s”上的账号“%s”：\n%s", "Account “%[2]s” on instance “%[1]s”:\n%[3]s"), s.Profile.Name, PrivName(cr.DisplayName()), quotaText(act.Confirm))
	if ui.AlertDialog(c.Key("ask"), &e.askOpen, title, msg, T("取消", "Cancel"), fmt.Sprintf(T("确认%s", "Confirm: %s"), title)) == 1 {
		a.quotaAct(q, e, cr, act.ID)
	}
}

// quotaErrText explains a failed read; upstream failures keep the
// provider's own message.
func quotaErrText(err error) string {
	var m *quota.Msg
	if errors.As(err, &m) {
		return quotaText(m.Text)
	}
	var ue *quota.UpstreamError
	if errors.As(err, &ue) {
		return fmt.Sprintf(T("服务商返回 %s", "the provider answered %s"), ue.Error())
	}
	return ErrorText(err)
}
