package view

import (
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/usage"
)

// usageDay is the usage of an instance over the last 24 hours, worked out
// again when the records change and at most once a minute otherwise.
type usageDay struct {
	ver    uint64
	at     time.Time
	sum    usage.Summary
	byAuth map[string]usageAcct
}

// usageAcct is the usage of one account, by its auth index.
type usageAcct struct {
	requests, failed int
	tokens           int64
}

// usageDayOf is the last day of usage of an instance; nil when usage is not
// collected at all.
func (a *App) usageDayOf(id string, now time.Time) *usageDay {
	if a.Usage == nil || id == "" {
		return nil
	}
	d := StateFor(a, "usage-day", id, func() *usageDay { return &usageDay{} })
	ver := a.Usage.Version()
	if d.byAuth != nil && d.ver == ver && now.Sub(d.at) < time.Minute {
		return d
	}
	d.ver, d.at = ver, now
	from := now.Add(-24 * time.Hour)
	d.byAuth = map[string]usageAcct{}
	a.Usage.Read(id, func(recs []usage.Record) {
		d.sum = usage.Summarize(recs, usage.Filter{From: from}, time.Hour, now)
		for i := len(recs) - 1; i >= 0 && !recs[i].Time.Before(from); i-- {
			r := recs[i]
			if r.AuthIndex == "" {
				continue
			}
			u := d.byAuth[r.AuthIndex]
			u.requests++
			if r.Failed && !r.Canceled {
				u.failed++
			}
			u.tokens += r.Tokens.Total
			d.byAuth[r.AuthIndex] = u
		}
	})
	return d
}

// compactNum shortens a count: 950, 1.2K, 3.4M, 1.1B.
func compactNum(n int64) string {
	f := float64(n)
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1e3)) + "K"
	case n < 1_000_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1e6)) + "M"
	}
	return trimZero(fmt.Sprintf("%.1f", f/1e9)) + "B"
}

func trimZero(s string) string {
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

// usageStateLabel names the state of an instance's usage stream.
func usageStateLabel(t *ui.Theme, st usage.Status) (string, ui.Color) {
	if st.ManagerPlus {
		switch st.State {
		case usage.StateLive:
			return T("已从 Manager-Plus 同步", "Synced from Manager-Plus"), t.Success
		case usage.StateConnecting:
			return T("正在读取 Manager-Plus", "Reading Manager-Plus"), t.TextMuted
		case usage.StateRetrying:
			return T("Manager-Plus 暂时无法访问", "Manager-Plus unreachable"), t.Warning
		case usage.StateRejected:
			return T("Manager-Plus 拒绝了管理员密钥", "Admin key refused by Manager-Plus"), t.Danger
		}
	}
	switch st.State {
	case usage.StateLive:
		return T("实时采集中", "Collecting"), t.Success
	case usage.StateConnecting:
		return T("正在连接用量流", "Connecting"), t.TextMuted
	case usage.StateRetrying:
		return T("用量流已断开，稍后重连", "Disconnected; retrying"), t.Warning
	case usage.StateRejected:
		return T("用量流拒绝了管理密钥", "Key refused by the usage stream"), t.Danger
	case usage.StateHomeMode:
		return T("Home 模式不提供用量流", "No usage stream in home mode"), t.TextMuted
	case usage.StateUnsupported:
		return T("此地址不支持用量流", "This address has no usage stream"), t.Warning
	}
	return T("未采集用量", "Not collecting"), t.TextMuted
}

// usageLine is one line of the last day's usage, for the panel. It says
// nothing when usage is not collected.
func (a *App) usageLine(c *ui.Context, id string, now time.Time) {
	d := a.usageDayOf(id, now)
	if d == nil {
		return
	}
	t := c.Theme()
	st := a.Usage.Status(id)
	s := d.sum
	ui.Row(c.Key("usage-line")).Gap(8).AlignItems(ui.Center).Padding(8, 12).Radius(10).Background(pal(t).Card).Children(func() {
		ui.Icon(c, icActivity).Size(14, 14).TextColor(t.TextMuted)
		var text string
		if s.Requests == 0 {
			text = T("24 小时内没有请求", "No requests in 24 hours")
		} else {
			text = fmt.Sprintf(T("24 小时：%d 次请求 · %s Token", "24 h: %d requests · %s tokens"), s.Requests, compactNum(s.Tokens.Total))
			if rate, ok := s.SuccessRate(); ok {
				text += fmt.Sprintf(T(" · 成功率 %.0f%%", " · %.0f%% success"), rate*100)
			}
		}
		ui.Text(c, text).FontSize(12).SingleLine().Grow(1)
		if st.State != usage.StateLive {
			label, col := usageStateLabel(t, st)
			ui.Text(c, label).FontSize(11).TextColor(col).SingleLine()
		}
	})
}

// usageCard is the last day's usage on the overview, with the hourly
// tokens. It reports whether the usage page was asked for.
func (a *App) usageCard(c *ui.Context, id string, now time.Time) bool {
	d := a.usageDayOf(id, now)
	if d == nil {
		return false
	}
	t := c.Theme()
	st := a.Usage.Status(id)
	s := d.sum
	open := false
	Panel(c).Children(func() {
		label, col := usageStateLabel(t, st)
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			PanelTitle(c, icActivity, TintOrange, T("用量（24 小时）", "Usage (24 h)"), "", false)
			ui.Box(c).Grow(1)
			StatusText(c, label, col)
		})
		stats := func() {
			ui.Row(c).Gap(10).Children(func() {
				Stat(c, strconv.Itoa(s.Requests), T("请求", "Requests"), t.Text)
				Stat(c, compactNum(s.Tokens.Total), "Token", t.Text)
				rate := "—"
				if r, ok := s.SuccessRate(); ok {
					rate = fmt.Sprintf("%.1f%%", r*100)
				}
				Stat(c, rate, T("成功率", "Success"), pick(s.Failed > 0, t.Warning, t.Text))
			})
		}
		// The card spans the page: when wide, the chart sits beside the
		// figures rather than stretched under them.
		if w, _ := c.Size(); w-sidebarWidth-48 >= 760 {
			ui.Row(c).Gap(32).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(3).Basis(0).Children(stats)
				ui.Column(c).Grow(2).Basis(0).Children(func() { usageSpark(c, s.Buckets) })
			})
		} else {
			stats()
			usageSpark(c, s.Buckets)
		}
		if PillButton(c, icActivity, T("查看用量", "View Usage"), false).Clicked() {
			open = true
		}
	})
	return open
}

// usageSpark draws the tokens of each slot as bars.
func usageSpark(c *ui.Context, buckets []usage.Bucket) {
	t := c.Theme()
	var peak int64
	for _, b := range buckets {
		peak = max(peak, b.Tokens)
	}
	const h = 36
	ui.Row(c.Key("spark")).Height(h).Gap(2).AlignItems(ui.End).Children(func() {
		for i, b := range buckets {
			bh := float32(2)
			if peak > 0 && b.Tokens > 0 {
				bh = max(3, float32(h)*float32(b.Tokens)/float32(peak))
			}
			col := pal(t).Line
			if b.Tokens > 0 {
				col = t.Accent
			}
			ui.Box(c.Key(fmt.Sprint("b", i))).Grow(1).Basis(0).Height(bh).Radius(2).Background(col)
		}
	})
}

// usageAccountLine is the last day's usage of one account, "" when it has
// none.
func (a *App) usageAccountLine(id, authIndex string, now time.Time) string {
	d := a.usageDayOf(id, now)
	if d == nil || authIndex == "" {
		return ""
	}
	u, ok := d.byAuth[authIndex]
	if !ok {
		return ""
	}
	s := fmt.Sprintf(T("24 小时：%d 次请求 · %s Token", "24 h: %d requests · %s tokens"), u.requests, compactNum(u.tokens))
	if u.failed > 0 {
		s += fmt.Sprintf(T(" · %d 次失败", " · %d failed"), u.failed)
	}
	return s
}
