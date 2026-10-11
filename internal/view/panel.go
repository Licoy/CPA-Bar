package view

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// The light panel next to the tray icon: an overview of all instances, or
// one instance's accounts, with the quick actions. It closes on Escape and
// when it loses the focus (a focusable flyout does both).

type panelList struct {
	instances ui.ListState
	creds     ui.ListState
}

func (a *App) panelView(c *ui.Context) {
	t := applyTheme(c)
	snaps := a.Svc.Snapshots()
	if a.panelInstance != "" && !slices.ContainsFunc(snaps, func(s core.InstanceSnapshot) bool { return s.Profile.ID == a.panelInstance }) {
		a.panelInstance = ""
		a.syncActive()
	}
	// A list's state belongs to one window: OpenPanel starts afresh.
	if a.panelLists == nil {
		a.panelLists = map[string]*panelList{}
	}
	lists := a.panelLists[a.panelInstance]
	if lists == nil {
		lists = &panelList{}
		a.panelLists[a.panelInstance] = lists
	}

	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.panelTop(c, snaps)
		ui.Column(c).Grow(1).Padding(4, 14, 10, 14).Gap(10).Children(func() {
			if len(snaps) == 0 {
				EmptyState(c, T("还没有实例", "No instances yet"),
					T("添加一个 CPA 服务的地址和管理密钥，即可在这里查看状态。", "Add a CPA server's address and management key to see its status here."))
				ui.Row(c).Justify(ui.Center).Children(func() {
					if ui.PrimaryButton(c, T("添加实例", "Add Instance")).Clicked() {
						a.OpenManager("instances")
					}
				})
				return
			}
			if a.panelInstance == "" {
				a.panelAll(c, snaps, lists)
			} else if snap, ok := a.Svc.Snapshot(a.panelInstance); ok {
				a.panelOne(c, snap, lists)
			}
		})
		a.panelFooter(c)
	})
}

// panelTop heads the panel: the logo, the instance shown, Back from an
// instance to all, Refresh and the project's page.
func (a *App) panelTop(c *ui.Context, snaps []core.InstanceSnapshot) {
	t := c.Theme()
	ui.Row(c).Gap(8).Padding(12, 14, 8, 14).AlignItems(ui.Center).Children(func() {
		AppLogo(c, 28)
		ui.Column(c).Grow(1).Basis(0).Gap(1).Padding(0, 0, 0, 2).Children(func() {
			ui.Text(c, "CPA-Bar").FontSize(15).FontWeight(700).SingleLine()
			for _, s := range snaps {
				if s.Profile.ID == a.panelInstance {
					ui.Text(c, s.Profile.Name).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			}
		})
		refreshing := slices.ContainsFunc(snaps, func(s core.InstanceSnapshot) bool {
			return instanceBusy(s) && (a.panelInstance == "" || s.Profile.ID == a.panelInstance)
		})
		if a.panelInstance != "" && PillButton(c, icBack, T("返回", "Back"), false).Tooltip(T("返回全部实例", "Back to all instances")).Clicked() {
			a.panelInstance = ""
			a.syncActive()
		}
		if len(snaps) > 0 && BusyPillButton(c, icRefresh, T("刷新", "Refresh"), refreshing, false).Tooltip(T("立即刷新", "Refresh now")).Clicked() {
			if a.panelInstance == "" {
				a.Svc.RefreshAll()
			} else {
				a.Svc.Refresh(a.panelInstance)
			}
		}
		if PillButton(c, icGithub, "", false).Label("GitHub").Tooltip(T("在 GitHub 上查看 CPA-Bar", "CPA-Bar on GitHub")).Clicked() {
			c.OpenURL(RepoURL)
		}
	})
}

// panelAll shows every instance with its state.
func (a *App) panelAll(c *ui.Context, snaps []core.InstanceSnapshot, lists *panelList) {
	t := c.Theme()
	o := a.Svc.Overview()
	ui.Row(c).Gap(8).Children(func() {
		online := t.Success
		if o.Problem > 0 {
			online = t.Danger
		}
		Stat(c, fmt.Sprintf("%d/%d", o.Online, o.Instances), T("在线实例", "Online"), online)
		Stat(c, staleNum(o.Accounts.Available, o.Stale), T("可用账号", "Available accounts"), t.Text)
		abn := t.Text
		if o.Accounts.Abnormal() > 0 {
			abn = t.Warning
		}
		Stat(c, staleNum(o.Accounts.Abnormal(), o.Stale), T("异常账号", "Abnormal accounts"), abn)
	})
	if o.Stale {
		Notice(c, T("带 * 的数字含有过期数据：部分实例最近一次刷新失败。", "Numbers marked * include stale data: some instances failed their last refresh."))
	}
	now := c.Now()
	ui.Text(c, T("实例", "Instances")).FontSize(12).FontWeight(600).TextColor(t.TextMuted).Padding(4, 4, 0, 4)
	lists.instances.Key = func(i int) any { return snaps[i].Profile.ID }
	ui.List(c, &lists.instances, len(snaps), func(i int) {
		s := snaps[i]
		ui.Column(c).Padding(0, 0, 6, 0).Children(func() {
			row := ui.Row(c).Padding(10, 12).Gap(10).Radius(12).AlignItems(ui.Center)
			bg := pal(t).Card
			if row.Hovered() {
				bg = bg.Mix(t.Text, 0.05)
			}
			row.Background(bg).Label(s.Profile.Name).OnClick(func() {
				a.panelInstance = s.Profile.ID
				a.syncActive()
			}).Children(func() {
				instanceTile(c, s, 32, bg)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, s.Profile.Name).FontSize(14).FontWeight(600).SingleLine().Grow(1)
						ui.Text(c, checkedLine(s, now)).FontSize(11).TextColor(t.TextMuted).SingleLine()
					})
					ui.Text(c, accountsLine(s)).FontSize(12).TextColor(t.TextMuted).SingleLine()
					if s.Err != nil {
						ui.Text(c, ErrorText(s.Err)).FontSize(11).TextColor(ConnColor(t, s.Conn)).MaxLines(2)
					}
				})
				Pill(c, ConnLabel(s.Conn), ConnColor(t, s.Conn))
				ui.Icon(c, icChevron).FontSize(12).TextColor(t.TextMuted)
			})
		})
	}).Grow(1)
}

// panelOne shows one instance's accounts, with a switch for each.
func (a *App) panelOne(c *ui.Context, s core.InstanceSnapshot, lists *panelList) {
	t := c.Theme()
	now := c.Now()
	ui.Row(c).Gap(8).Children(func() {
		Stat(c, ConnLabel(s.Conn), checkedLine(s, now), ConnColor(t, s.Conn))
		Stat(c, staleNumIf(s.Accounts.Available, s), T("可用账号", "Available"), t.Text)
		abn := t.Text
		if s.Accounts.Abnormal() > 0 {
			abn = t.Warning
		}
		Stat(c, staleNumIf(s.Accounts.Abnormal(), s), T("异常账号", "Abnormal"), abn)
	})
	a.usageLine(c, s.Profile.ID, now)
	ErrorBanner(c, s.Err)
	if s.Stale {
		Notice(c, fmt.Sprintf(T("以下是 %s 的数据，可能已过期。", "The data below is from %s and may be out of date."), Ago(s.LastSuccess, now)))
	}
	ui.Row(c).Gap(6).Children(func() {
		if PillButton(c, icUsers, T("管理账号", "Manage Accounts"), false).Clicked() {
			a.OpenInstance(s.Profile.ID, "accounts")
		}
		if PillButton(c, icCopy, T("复制地址", "Copy Address"), false).Clicked() {
			c.WriteClipboard(cpa.RootURL(s.Profile.BaseURL))
			c.Toast(T("已复制地址", "Address copied"))
		}
		if PillButton(c, icExternal, T("打开管理页", "Open Web UI"), false).Tooltip(T("在浏览器中打开 CPA 的管理页面", "Open CPA's management page in the browser")).Clicked() {
			c.OpenURL(cpa.RootURL(s.Profile.BaseURL) + "/management.html")
		}
	})
	if !s.HasData {
		if s.Err == nil {
			Busy(c, T("正在读取账号…", "Loading accounts…"))
		}
		return
	}
	creds := sortedForPanel(s.Credentials)
	if len(creds) == 0 {
		EmptyState(c, T("没有账号", "No accounts"), T("这个实例还没有认证文件。", "This instance has no credentials yet."))
		return
	}
	lists.creds.Key = func(i int) any { return credKeyOf(creds[i]) }
	ui.List(c, &lists.creds, len(creds), func(i int) {
		a.panelCred(c, s, creds[i], now)
	}).Grow(1).Radius(12).Background(pal(t).Card).Padding(4, 0)
}

func (a *App) panelCred(c *ui.Context, s core.InstanceSnapshot, cr cpa.Credential, now time.Time) {
	t := c.Theme()
	h := cr.Health()
	key := s.Profile.ID + "\x00" + credKeyOf(cr)
	task := a.panelCreds[key]
	if task == nil {
		task = &Task{}
		a.panelCreds[key] = task
	}
	ui.Column(c).Padding(7, 12).Gap(3).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			credTile(c, cr, 26, pal(t).Card)
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Text(c, PrivName(cr.DisplayName())).SingleLine().FontSize(13).FontWeight(500)
				sub := providerLabel(cr.ProviderName()) + " · " + HealthLabel(h)
				if h == cpa.HealthCooling {
					sub += " · " + Until(*cr.NextRetryAfter, now)
				}
				ui.Text(c, sub).FontSize(11).TextColor(HealthColor(t, h)).SingleLine()
			})
			if task.Running {
				ui.Spinner(c).Label(T("正在提交", "Saving"))
			}
			on := !cr.Disabled
			label := fmt.Sprintf(T("启用 %s", "Enable %s"), PrivName(cr.DisplayName()))
			if ui.Switch(c.Key(key), &on).Label(label).Disabled(task.Running || cr.RuntimeOnly || s.Stale).Changed() {
				id := s.Profile.ID
				Run(a.panel, task, 0, func(ctx context.Context) error {
					return a.Svc.SetCredentialDisabled(ctx, id, cr, !on)
				}, nil)
			}
		})
		if cr.StatusMessage != "" && h != cpa.HealthAvailable && h != cpa.HealthDisabled {
			ui.Text(c, PrivText(cr.StatusMessage)).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Selectable().Padding(0, 0, 0, 36)
		}
		if task.Err != nil {
			ui.Text(c, ErrorText(task.Err)).FontSize(11).TextColor(t.Danger).MaxLines(3).Selectable().Padding(0, 0, 0, 36)
		}
		ui.Column(c).Padding(0, 0, 0, 36).Children(func() { a.quotaView(c, s, cr, now, 2) })
	})
}

func (a *App) panelFooter(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(6).Padding(10, 14).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(pal(t).Line).Children(func() {
		if PillButton(c, icPlus, T("添加实例", "Add"), false).Clicked() {
			a.OpenManager("instances")
		}
		if PillButton(c, icGear, T("设置", "Settings"), false).Clicked() {
			a.OpenManager("settings")
		}
		if UpdatesEnabled() && PillButton(c, icRefresh, T("更新", "Update"), false).Tooltip(T("检查更新", "Check for updates")).Clicked() {
			a.ClosePanel()
			CheckForUpdates()
		}
		ui.Box(c).Grow(1)
		if ui.PrimaryButton(c, T("打开管理器", "Open Manager")).Radius(999).Clicked() {
			id := a.panelInstance
			if id != "" {
				a.OpenInstance(id, "overview")
			} else {
				a.OpenManager("")
			}
		}
	})
}

// instanceBusy reports whether an instance is being read: refreshing, or
// waiting for its first refresh.
func instanceBusy(s core.InstanceSnapshot) bool {
	return s.Refreshing || (!s.HasData && s.Err == nil && !s.Profile.Paused)
}

// sortedForPanel puts the accounts needing attention first.
func sortedForPanel(creds []cpa.Credential) []cpa.Credential {
	rank := func(h cpa.Health) int {
		switch h {
		case cpa.HealthError:
			return 0
		case cpa.HealthCooling:
			return 1
		case cpa.HealthPending:
			return 2
		case cpa.HealthAvailable:
			return 3
		}
		return 4
	}
	out := slices.Clone(creds)
	slices.SortStableFunc(out, func(x, y cpa.Credential) int { return rank(x.Health()) - rank(y.Health()) })
	return out
}

func credKeyOf(c cpa.Credential) string {
	if c.AuthIndex != "" {
		return c.AuthIndex
	}
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func accountsLine(s core.InstanceSnapshot) string {
	switch {
	case s.Profile.Paused:
		return T("已暂停自动刷新", "Automatic refresh paused")
	case !s.HasData:
		return T("暂无账号数据", "No account data")
	}
	line := fmt.Sprintf(T("可用 %d · 异常 %d · 共 %d", "%d available · %d abnormal · %d total"),
		s.Accounts.Available, s.Accounts.Abnormal(), s.Accounts.Total)
	if s.Stale {
		line += T("（过期）", " (stale)")
	}
	return line
}

func checkedLine(s core.InstanceSnapshot, now time.Time) string {
	if s.LastAttempt.IsZero() {
		return T("尚未检查", "Not checked yet")
	}
	return fmt.Sprintf(T("检查于 %s", "Checked %s"), Ago(s.LastAttempt, now))
}

// staleNum marks a number that includes stale data, so it is never read
// as current.
func staleNum(n int, stale bool) string {
	if stale {
		return strconv.Itoa(n) + "*"
	}
	return strconv.Itoa(n)
}

func staleNumIf(n int, s core.InstanceSnapshot) string {
	if !s.HasData {
		return "—"
	}
	return staleNum(n, s.Stale)
}
