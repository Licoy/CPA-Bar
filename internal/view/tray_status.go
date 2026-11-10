package view

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

// The menu bar can show the quota of the tray instance's first enabled
// accounts instead of the logo, in one of the quota styles: a component
// an account, led by its provider's logo, with its short and long limits,
// or the first two accounts stacked in one component, a limit each. The
// tray instance is the panel's default one, else the first; its pinned
// accounts come first. Its quota is read as for the accounts page, only
// while the instance is online, so a refused key is not tried again and
// again.

const trayMaxAccounts = 2

// trayCell is one limit of an account as the menu bar shows it.
type trayCell struct {
	// provider is the account's provider id, for its logo.
	provider string
	// label is the limit's short name where an account shows two.
	label string
	// left is the percent left, tier its color (quotaTier).
	left, tier int
	// empty is a limit without a percentage: no level, and its texts are
	// "--" or the provider's remark (a balance, say).
	empty bool
	// percent is "80%", countdown the time left until the refill; meter
	// and battery are the texts the meters and battery styles are set to
	// show; ring is the value inside a ring, empty where it does not fit.
	percent, countdown, meter, battery, ring string
}

// trayComponent is an account on its own, whose limits the cells are, or
// two accounts stacked, a cell each, with no provider of its own.
type trayComponent struct {
	provider string
	cells    []trayCell
}

// trayRender is what the menu bar draws in a quota style.
type trayRender struct {
	style                           string
	vertical, hideIcons, ringValues bool
	components                      []trayComponent
}

// trayAccount is the quota of an account the menu bar shows.
type trayAccount struct {
	provider string
	// short and long are its main limits, nil when unknown.
	short, long *quota.Window
}

// buildTray lays out the accounts in the settings' style.
func buildTray(s store.Settings, accts []trayAccount, now time.Time) trayRender {
	r := trayRender{style: s.TrayStyle, vertical: s.TrayBatteryVertical, hideIcons: s.TrayHideIcons, ringValues: s.TrayRingValues}
	for _, g := range trayGroups(s, len(accts)) {
		var comp trayComponent
		if len(g) == 1 {
			comp.provider = accts[g[0]].provider
		}
		for _, i := range g {
			comp.cells = append(comp.cells, trayCells(s, accts[i], len(g) == 1, now)...)
		}
		r.components = append(r.components, comp)
	}
	return r
}

// trayGroups are the accounts of each component, by index: the first two
// stacked where the settings and the style ask for it, else one each.
func trayGroups(s store.Settings, n int) [][]int {
	var out [][]int
	for i := range n {
		if i == 1 && s.TrayStack && store.TrayStyleStacks(s.TrayStyle) {
			out[0] = append(out[0], i)
			continue
		}
		out = append(out, []int{i})
	}
	return out
}

// trayWindows are the limits an account shows: the chosen one, else its
// other one; automatically both where it has the component to itself,
// else the short one.
func trayWindows(a trayAccount, window string, alone bool) []quota.Window {
	first, second := a.short, a.long
	if window == store.TrayWindowLong {
		first, second = a.long, a.short
	}
	var out []quota.Window
	if first != nil {
		out = append(out, *first)
	}
	if second != nil && (len(out) == 0 || window == store.TrayWindowAuto && alone) {
		out = append(out, *second)
	}
	return out
}

func trayCells(s store.Settings, a trayAccount, alone bool, now time.Time) []trayCell {
	ws := trayWindows(a, s.TrayWindow, alone)
	if len(ws) == 0 {
		return []trayCell{{provider: a.provider, empty: true, percent: "--", countdown: "--", meter: "--", battery: "--", ring: "--"}}
	}
	cells := make([]trayCell, len(ws))
	for i, w := range ws {
		c := trayCell{provider: a.provider}
		if len(ws) > 1 {
			c.label = trayLimitName(w.Label)
			if !English() {
				c.label = strings.ReplaceAll(c.label, " ", "")
			}
		}
		if !w.Known {
			c.empty = true
			c.percent = cmp.Or(w.Detail, "--")
			c.countdown, c.meter, c.battery = c.percent, c.percent, c.percent
			cells[i] = c
			continue
		}
		c.left, c.tier = percent(w.Remaining), quotaTier(w.Remaining)
		c.percent, c.ring = fmt.Sprintf("%d%%", c.left), strconv.Itoa(c.left)
		reset := "--"
		c.countdown = "--"
		if !w.ResetAt.IsZero() {
			reset = trayResetTime(w.ResetAt, now)
			c.countdown = shortDuration(w.ResetAt.Sub(now))
		}
		switch s.TrayMetersDetail {
		case store.TrayDetailReset:
			c.meter = reset
		case store.TrayDetailBoth:
			c.meter = c.percent + " · " + reset
		default:
			c.meter = c.percent
		}
		c.battery = c.countdown
		if s.TrayBatteryDetail == store.TrayBatteryPercent {
			c.battery = c.percent
		}
		cells[i] = c
	}
	return cells
}

// trayResetTime is when a limit refills: the time on today's date, else
// the date, "10/18".
func trayResetTime(at, now time.Time) string {
	d, n := at.Local(), now.Local()
	if d.Year() == n.Year() && d.YearDay() == n.YearDay() {
		return d.Format("15:04")
	}
	return fmt.Sprintf("%d/%d", int(d.Month()), d.Day())
}

// shortDuration is a time left in its two largest short units, a zero
// second one left out: "1天6小时", "1d 6h". Seconds are rounded up.
func shortDuration(d time.Duration) string {
	v := max(0, int(math.Ceil(d.Seconds())))
	amounts := [...]int{v / 86400, v % 86400 / 3600, v % 3600 / 60, v % 60}
	units := [...]string{"d", "h", "m", "s"}
	sep := " "
	if !English() {
		units, sep = [...]string{"天", "小时", "分", "秒"}, ""
	}
	first := 0
	for first < 3 && amounts[first] == 0 {
		first++
	}
	parts := []string{strconv.Itoa(amounts[first]) + units[first]}
	if first < 2 && amounts[first+1] > 0 {
		parts = append(parts, strconv.Itoa(amounts[first+1])+units[first+1])
	}
	return strings.Join(parts, sep)
}

// trayAccounts are the accounts of s the menu bar shows: the first
// enabled ones that have a quota, pinned first, in the order of the
// account list.
func trayAccounts(s core.InstanceSnapshot, limit int) []cpa.Credential {
	var out []cpa.Credential
	for _, cr := range pinnedFirst(sortedForPanel(s.Credentials), s.Profile.PinnedAccounts) {
		if len(out) == limit {
			break
		}
		if cr.Health() == cpa.HealthDisabled || !quota.Supported(cr) {
			continue
		}
		out = append(out, cr)
	}
	return out
}

// trayQuota is the quota of the tray instance's accounts, with a tooltip
// line each. It starts the reads that are due.
func (a *App) trayQuota(limit int, now time.Time) (accts []trayAccount, tips []string) {
	s, ok := a.trayInstance()
	if !ok || s.Profile.Paused {
		return nil, nil
	}
	for _, cr := range trayAccounts(s, limit) {
		var e *quotaEntry
		if s.Conn == core.ConnOnline {
			e, _, _ = a.quotaEntryFor(s, cr, now)
		} else if q := a.quotas[s.Profile.ID]; q != nil {
			e = q.entries[cr.AuthIndex]
		}
		acct := trayAccount{provider: cr.ProviderName()}
		if e != nil && e.res != nil {
			acct.short, acct.long = e.res.Summary()
		}
		tip := trayTip(cr, acct)
		if e != nil && a.Svc.Settings().PassiveQuota {
			if e.res != nil {
				tip += " · " + e.sourceText()
			} else {
				tip += " · " + T("等待 CPA 额度数据", "Waiting for CPA quota data")
				if e.err != nil {
					tip += " · " + quotaErrText(e.err)
				}
			}
		}
		accts = append(accts, acct)
		tips = append(tips, tip)
	}
	return accts, tips
}

// trayTip is an account's line in the tooltip: its name and the percent
// left of its main limits.
func trayTip(cr cpa.Credential, a trayAccount) string {
	var parts []string
	for _, w := range []*quota.Window{a.short, a.long} {
		if w != nil {
			parts = append(parts, fmt.Sprintf("%s %d%%", T(w.Label.ZH, w.Label.EN), percent(w.Remaining)))
		}
	}
	if len(parts) == 0 {
		parts = []string{T("额度未知", "quota unknown")}
	}
	return PrivName(cr.DisplayName()) + "  " + strings.Join(parts, " · ")
}

// trayLimitName is a limit's label without the word for limit: "周限额" →
// "每周", "5-hour limit" → "5-hour".
func trayLimitName(label quota.Text) string {
	s := T(label.ZH, label.EN)
	for _, suffix := range []string{"剩余额度", "限额", "额度", " remaining", " limit", " quota"} {
		if cut, ok := strings.CutSuffix(strings.ToLower(s), suffix); ok && cut != "" {
			s = s[:len(cut)]
			break
		}
	}
	switch s {
	case "周":
		return "每周"
	case "月度":
		return "每月"
	}
	return s
}

// trayInstance is the instance whose accounts the menu bar shows.
func (a *App) trayInstance() (core.InstanceSnapshot, bool) {
	snaps := a.Svc.Snapshots()
	id := a.Svc.Settings().DefaultInstanceID
	for _, s := range snaps {
		if s.Profile.ID == id {
			return s, true
		}
	}
	if len(snaps) > 0 {
		return snaps[0], true
	}
	return core.InstanceSnapshot{}, false
}

// percent is the share left, rounded down as the panel shows it.
func percent(f float64) int { return int(math.Floor(max(0, min(1, f))*100 + 1e-9)) }

// trayAccountLimit is how many accounts the menu bar shows in a style:
// none with the icon, and only where the menu bar can draw them (macOS).
func trayAccountLimit(style string) int {
	if !mygoTrayTitleSupported() || style == store.TrayStyleIcon {
		return 0
	}
	return trayMaxAccounts
}
