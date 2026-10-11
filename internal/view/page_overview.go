package view

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func init() {
	RegisterPage(Page{
		ID:       "overview",
		Order:    0,
		Group:    GroupMonitor,
		Title:    func() string { return T("概览", "Overview") },
		Instance: true,
		View:     overviewPage,
	})
}

// overviewPage is the instance's dashboard: cards with the latest figures
// and the history of the last refreshes, after Pulse's overview.
func overviewPage(c *ui.Context, env *PageEnv) {
	s := env.Snap
	a := env.App

	ErrorBanner(c, s.Err)
	if s.Profile.Paused {
		Notice(c, T("此实例已暂停自动刷新，可在“实例”页恢复。", "Automatic refresh is paused for this instance; resume it on the Instances page."))
	}
	if s.Stale {
		Notice(c, fmt.Sprintf(T("最近一次刷新失败，以下数据来自 %s，可能已过期（标 * 的数字）。", "The last refresh failed; the data below is from %s and may be out of date (figures marked *)."), Ago(s.LastSuccess, env.Now)))
	}

	w, _ := c.Size()
	cols := ResponsiveColumns(w-sidebarWidth-48, 250, 3)
	ui.Grid(c).Columns(cols).Gap(14).Children(func() {
		connectionCard(c, env)
		if accountsCard(c, s) {
			a.page = "accounts"
		}
		availabilityCard(c, env)
	})
	ui.Grid(c).Columns(min(cols, 2)).Gap(14).Children(func() {
		if attentionCard(c, s, env.Now) {
			a.page = "accounts"
		}
		providersCard(c, s)
	})
	// Usage takes a line of its own.
	if a.usageCard(c, s.Profile.ID, env.Now) {
		a.page = "usage"
	}
	detailsCard(c, env)
}

func connectionCard(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	s := env.Snap
	Panel(c).Children(func() {
		if PanelTitle(c, icActivity, TintBlue, T("连接", "Connection"), "", true) {
			env.App.page = "logs"
		}
		last, ok := lastSample(s.History)
		switch {
		case ok && last.OK && s.Conn == core.ConnOnline:
			v, unit := latencyParts(last.Latency)
			BigValue(c, T("响应延迟", "Response time"), v, unit, t.Text)
		default:
			BigValue(c, checkedLine(s, env.Now), ConnLabel(s.Conn), "", ConnColor(t, s.Conn))
		}
		Metrics(c,
			Metric{Label: T("状态", "Status"), Value: ConnLabel(s.Conn), Color: ConnColor(t, s.Conn)},
			Metric{Label: T("最近成功", "Last success"), Value: Ago(s.LastSuccess, env.Now)},
			Metric{Label: T("刷新间隔", "Interval"), Value: strconv.Itoa(refreshRate(env)) + T(" 秒", "s")},
		)
		top := time.Duration(0)
		for _, h := range s.History {
			top = max(top, h.Latency)
		}
		cols := make([][]DotSeg, len(s.History))
		for i, h := range s.History {
			if !h.OK {
				cols[i] = []DotSeg{{Value: 1, Color: TintRed.C(t)}}
				continue
			}
			cols[i] = []DotSeg{{Value: float64(h.Latency) / float64(max(top, 1)), Color: TintBlue.C(t)}}
		}
		DotChart(c, TintBlue, cols, 1)
	})
}

// accountsCard reports a click on its title.
func accountsCard(c *ui.Context, s core.InstanceSnapshot) bool {
	t := c.Theme()
	acc := s.Accounts
	var open bool
	Panel(c).Children(func() {
		open = PanelTitle(c, icUsers, TintGreen, T("账号", "Accounts"), "", true)
		ui.Row(c).AlignItems(ui.End).Children(func() {
			BigValue(c, fmt.Sprintf(T("可用，共 %s 个", "Available of %s"), staleNumIf(acc.Total, s)),
				staleNumIf(acc.Available, s), "", t.Text).Grow(1)
			if n := acc.Abnormal(); s.HasData && n > 0 {
				Pill(c, fmt.Sprintf(T("! %d 个异常", "! %d need attention"), n), t.Warning).Margin(0, 0, 8, 0)
			}
		})
		Metrics(c,
			Metric{Label: T("冷却中", "Cooling"), Value: staleNumIf(acc.Cooling, s), Color: pick(acc.Cooling > 0, t.Warning, t.Text)},
			Metric{Label: T("异常", "Error"), Value: staleNumIf(acc.Error, s), Color: pick(acc.Error > 0, t.Danger, t.Text)},
			Metric{Label: T("已停用", "Disabled"), Value: staleNumIf(acc.Disabled, s)},
		)
		top := 0
		for _, h := range s.History {
			top = max(top, h.Accounts.Total)
		}
		cols := make([][]DotSeg, len(s.History))
		for i, h := range s.History {
			if !h.OK {
				continue
			}
			cols[i] = []DotSeg{
				{Value: float64(h.Accounts.Available), Color: TintGreen.C(t)},
				{Value: float64(h.Accounts.Cooling), Color: TintOrange.C(t)},
				{Value: float64(h.Accounts.Error), Color: TintRed.C(t)},
			}
		}
		DotChart(c, TintGreen, cols, float64(top))
	})
	return open
}

func availabilityCard(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	s := env.Snap
	Panel(c).Children(func() {
		PanelTitle(c, icGauge, TintPurple, T("可用率", "Availability"), "", false)
		pct := "—"
		if s.HasData && s.Accounts.Total > 0 {
			pct = strconv.Itoa(s.Accounts.Available*100/s.Accounts.Total) + pick(s.Stale, "*", "")
		}
		BigValue(c, T("可用账号占比", "Accounts available"), pct, pick(pct == "—", "", "%"), t.Text)
		var ok, n int
		var sum, peak time.Duration
		for _, h := range s.History {
			n++
			if h.OK {
				ok++
				sum += h.Latency
				peak = max(peak, h.Latency)
			}
		}
		avg, success := "—", "—"
		if ok > 0 {
			avg = fmtLatency(sum / time.Duration(ok))
		}
		if n > 0 {
			success = strconv.Itoa(ok*100/n) + "%"
		}
		Metrics(c,
			Metric{Label: T("刷新成功率", "Refresh success"), Value: success, Color: pick(ok < n, t.Warning, t.Text)},
			Metric{Label: T("平均延迟", "Average"), Value: avg},
			Metric{Label: T("峰值延迟", "Peak"), Value: pick(ok > 0, fmtLatency(peak), "—")},
		)
		cols := make([][]DotSeg, len(s.History))
		for i, h := range s.History {
			if !h.OK || h.Accounts.Total == 0 {
				continue
			}
			cols[i] = []DotSeg{{Value: float64(h.Accounts.Available) / float64(h.Accounts.Total), Color: TintPurple.C(t)}}
		}
		DotChart(c, TintPurple, cols, 1)
	})
}

// attentionCard lists the credentials that need attention; it reports a
// click on its title.
func attentionCard(c *ui.Context, s core.InstanceSnapshot, now time.Time) bool {
	t := c.Theme()
	var attention []cpa.Credential
	for _, cr := range sortedForPanel(s.Credentials) {
		if h := cr.Health(); h == cpa.HealthError || h == cpa.HealthCooling {
			attention = append(attention, cr)
		}
	}
	var open bool
	Panel(c).Children(func() {
		trailing := ""
		if s.HasData {
			trailing = fmt.Sprintf(T("%d 个", "%d"), len(attention))
		}
		open = PanelTitle(c, icAlert, TintOrange, T("需要关注的账号", "Needs Attention"), trailing, true)
		switch {
		case !s.HasData:
			ui.Text(c, T("尚无数据。", "No data yet.")).FontSize(13).TextColor(t.TextMuted)
			return
		case len(attention) == 0:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(6, 0).Children(func() {
				ui.Icon(c, icCheck).FontSize(16).TextColor(t.Success)
				ui.Text(c, T("没有异常账号。", "No abnormal accounts.")).FontSize(13).TextColor(t.TextMuted)
			})
			return
		}
		ui.Column(c).Gap(6).Children(func() {
			for i, cr := range attention {
				if i == 8 {
					ui.Text(c, fmt.Sprintf(T("还有 %d 个，请在“账号”页查看", "%d more on the Accounts page"), len(attention)-8)).
						FontSize(12).TextColor(t.TextMuted)
					break
				}
				h := cr.Health()
				ui.Row(c).Key(credKeyOf(cr)).Gap(10).Padding(8, 10).Radius(10).Background(pal(t).Inset).AlignItems(ui.Center).Children(func() {
					credTile(c, cr, 28, pal(t).Inset)
					ui.Column(c).Grow(1).Gap(2).Children(func() {
						ui.Text(c, PrivName(cr.DisplayName())).FontSize(13).FontWeight(500).SingleLine()
						detail := providerLabel(cr.ProviderName())
						if h == cpa.HealthCooling && cr.NextRetryAfter != nil {
							detail += " · " + fmt.Sprintf(T("%s恢复", "recovers %s"), Until(*cr.NextRetryAfter, now))
						}
						if cr.StatusMessage != "" {
							detail += " · " + PrivText(cr.StatusMessage)
						}
						ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Selectable()
					})
					Pill(c, HealthLabel(h), HealthColor(t, h))
				})
			}
		})
	})
	return open
}

// providersCard shows the accounts of each provider, as Pulse's energy by
// app.
func providersCard(c *ui.Context, s core.InstanceSnapshot) {
	t := c.Theme()
	type row struct {
		id, name         string
		total, available int
	}
	var rows []row
	for _, cr := range s.Credentials {
		name := providerLabel(cr.ProviderName())
		i := slices.IndexFunc(rows, func(r row) bool { return r.name == name })
		if i < 0 {
			rows = append(rows, row{id: cr.ProviderName(), name: name})
			i = len(rows) - 1
		}
		rows[i].total++
		if cr.Health() == cpa.HealthAvailable {
			rows[i].available++
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.total != b.total {
			return b.total - a.total
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
	Panel(c).Children(func() {
		trailing := ""
		if s.HasData {
			trailing = fmt.Sprintf(T("%d 个提供商", "%d providers"), len(rows))
		}
		PanelTitle(c, icCloud, TintIndigo, T("按提供商", "By Provider"), trailing, false)
		if len(rows) == 0 {
			ui.Text(c, T("尚无账号。", "No accounts yet.")).FontSize(13).TextColor(t.TextMuted)
			return
		}
		ui.Column(c).Gap(12).Children(func() {
			for _, r := range rows {
				ui.Column(c).Key(r.name).Gap(6).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ProviderIcon(c, r.id, 16)
						ui.Text(c, r.name).FontSize(13).FontWeight(500).Grow(1).SingleLine()
						ui.Text(c, fmt.Sprintf(T("%d / %d 可用", "%d / %d available"), r.available, r.total)).
							FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
					})
					col := TintGreen.C(t)
					if r.available < r.total {
						col = TintOrange.C(t)
					}
					if r.available == 0 {
						col = TintRed.C(t)
					}
					Bar(c, float64(r.available)/float64(max(r.total, 1)), col)
				})
			}
		})
	})
}

func detailsCard(c *ui.Context, env *PageEnv) {
	s := env.Snap
	root := cpa.RootURL(s.Profile.BaseURL)
	Panel(c).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Grow(1).Children(func() {
				PanelTitle(c, icServer, TintGray, T("实例信息", "Instance"), "", false)
			})
			if PillButton(c, icCopy, T("复制地址", "Copy Address"), false).Clicked() {
				c.WriteClipboard(root)
				c.Toast(T("已复制地址", "Address copied"))
			}
			if PillButton(c, icExternal, T("打开管理页", "Open Web UI"), false).Clicked() {
				c.OpenURL(root + "/management.html")
			}
		})
		ui.Grid(c).Columns(2).GapX(24).GapY(10).Children(func() {
			kv(c, T("管理接口", "Management API"), PrivURL(s.Profile.BaseURL))
			kv(c, T("最近检查", "Last check"), Ago(s.LastAttempt, env.Now))
			v := "—"
			if s.Server.Version != "" {
				v = s.Server.Version
				if s.Server.Commit != "" {
					v += " (" + s.Server.Commit + ")"
				}
			}
			kv(c, T("服务器版本", "Server version"), v)
			kv(c, T("待处理账号", "Pending accounts"), staleNumIf(s.Accounts.Pending, s))
		})
	})
}

// refreshRate is the instance's refresh interval in seconds while the
// window shows it.
func refreshRate(env *PageEnv) int {
	st := env.App.Svc.Settings()
	if env.App.manager != nil && env.App.manager.IsVisible() {
		return st.ActiveRefresh
	}
	return st.BackgroundRefresh
}

func lastSample(h []core.Sample) (core.Sample, bool) {
	if len(h) == 0 {
		return core.Sample{}, false
	}
	return h[len(h)-1], true
}

// latencyParts splits a duration into a number and its unit.
func latencyParts(d time.Duration) (string, string) {
	if d < time.Millisecond {
		return "<1", "ms"
	}
	if d < time.Second {
		return strconv.Itoa(int(d.Milliseconds())), "ms"
	}
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64), "s"
}

func fmtLatency(d time.Duration) string {
	v, unit := latencyParts(d)
	return v + " " + unit
}

// kv is a labeled value in a card.
func kv(c *ui.Context, label, value string) {
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted).Width(110)
		ui.Text(c, value).FontSize(12).Grow(1).Basis(0).Selectable()
	})
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
