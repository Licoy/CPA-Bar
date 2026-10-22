package view

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

func init() {
	RegisterPage(Page{
		ID:    "settings",
		Order: 10,
		Group: GroupApp,
		Title: func() string { return T("设置", "Settings") },
		View:  settingsPage,
	})
}

// Version is the app's version, set by main.
var Version = "dev"

var panelOpaqueAvailable = panelOpacitySupported

// versionLabel is Version as shown, "v1.0.0".
func versionLabel() string {
	if Version == "" || Version == "dev" || strings.HasPrefix(Version, "v") {
		return Version
	}
	return "v" + Version
}

type settingsState struct {
	background string
	active     string
	quota      string
	loaded     bool
	// login is read from the system once: the user may change it there.
	login    bool
	err      error
	loginErr error
}

func settingsPage(c *ui.Context, env *PageEnv) {
	a := env.App
	st := StateFor(a, "settings", "", func() *settingsState { return &settingsState{} })
	cur := a.Svc.Settings()
	if !st.loaded {
		st.background = strconv.Itoa(cur.BackgroundRefresh)
		st.active = strconv.Itoa(cur.ActiveRefresh)
		st.quota = strconv.Itoa(cur.QuotaRefreshMinutes)
		st.login = system.OpenAtLogin()
		st.loaded = true
	}
	save := func(fn func(*store.Settings)) bool {
		st.err = a.Svc.UpdateSettings(fn)
		return st.err == nil
	}

	ErrorBanner(c, st.err)

	settingsGroup(c, icPanel, TintBlue, T("外观", "Appearance"), func(row func(label, detail string, control func())) {
		if runtime.GOOS == "darwin" {
			supported := panelOpaqueAvailable()
			detail := ""
			if !supported {
				detail = T("当前使用半透明；不透明模式需要 macOS 14 或更新版本", "Currently translucent; opaque mode requires macOS 14 or later")
			}
			row(T("面板背景", "Panel background"), detail, func() {
				i := 0
				if supported && cur.PanelOpaque {
					i = 1
				}
				disabled := -1
				if !supported {
					disabled = 1
				}
				if PillTabsDisabled(c.Key("panel-background"), &i, disabled, T("半透明", "Translucent"), T("不透明", "Opaque")) {
					opaque := i == 1
					if save(func(s *store.Settings) { s.PanelOpaque = opaque }) {
						a.ClosePanel()
					}
				}
				if !supported && cur.PanelOpaque && ui.Button(c, T("保存半透明", "Save Translucent")).Radius(999).Clicked() {
					if save(func(s *store.Settings) { s.PanelOpaque = false }) {
						a.ClosePanel()
					}
				}
			})
		}
		row(T("主题", "Theme"), "", func() {
			modes := []string{store.AppearanceSystem, store.AppearanceLight, store.AppearanceDark}
			i := max(0, indexOf(modes, cur.Appearance))
			if PillTabs(c.Key("theme"), &i, T("跟随系统", "System"), T("浅色", "Light"), T("深色", "Dark")) {
				mode := modes[i]
				save(func(s *store.Settings) { s.Appearance = mode })
				ApplyAppearance(mode)
			}
		})
		row(T("语言", "Language"), "", func() {
			langs := []string{store.LanguageAuto, store.LanguageZH, store.LanguageEN}
			i := max(0, indexOf(langs, cur.Language))
			if PillTabs(c.Key("lang"), &i, T("自动", "Auto"), "简体中文", "English") {
				setting := langs[i]
				resolved, err := resolveLanguage(setting)
				if err != nil {
					st.err = err
				} else if save(func(s *store.Settings) { s.Language = setting }) {
					setResolvedLanguage(resolved)
					a.changed()
				}
			}
		})
		row(T("隐私模式", "Privacy mode"), T("将 CPA 地址、账号邮箱和名称的中间部分显示为 ***，便于截图或共享屏幕。“复制地址”仍复制完整地址。", "Show the middle of CPA addresses, account emails and names as ***, for screenshots and screen sharing. Copy Address still copies the full address."), func() {
			on := cur.Privacy
			if ui.Switch(c.Key("privacy"), &on).Label(T("隐私模式", "Privacy mode")).Changed() {
				save(func(s *store.Settings) { s.Privacy = on })
				SetPrivacy(on)
				a.changed()
			}
		})
		row(trayIconName(), trayIconDetail(), func() {
			i := 0
			if cur.TrayIconColor {
				i = 1
			}
			if PillTabs(c.Key("trayicon"), &i, T("纯色", "Mono"), T("彩色", "Color")) {
				color := i == 1
				save(func(s *store.Settings) { s.TrayIconColor = color })
				a.changed()
			}
		})
		if mygoTrayTitleSupported() {
			row(T("菜单栏样式", "Menu bar style"), trayStyleDetail(cur.TrayStyle), func() {
				styles := []string{store.TrayStyleIcon, store.TrayStyleBars}
				i := max(0, indexOf(styles, cur.TrayStyle))
				if PillTabs(c.Key("traystyle"), &i, T("图标", "Icon"), T("进度条", "Bars")) {
					style := styles[i]
					save(func(s *store.Settings) { s.TrayStyle = style })
					a.changed()
				}
			})
			switch cur.TrayStyle {
			case store.TrayStyleIcon:
				row(T("显示在线实例数", "Show instances online"), T("在图标旁显示“3/4”（在线实例/实例总数）", "Show “3/4” (online/all instances) next to the icon"), func() {
					on := cur.TrayText
					if ui.Switch(c.Key("traytext"), &on).Label(T("显示在线实例数", "Show instances online")).Changed() {
						save(func(s *store.Settings) { s.TrayText = on })
						a.changed()
					}
				})
			case store.TrayStyleBars:
				trayAccountsRow(c, a, row)
			}
		}
	})

	settingsGroup(c, icRefresh, TintGreen, T("刷新", "Refresh"), func(row func(label, detail string, control func())) {
		row(T("被动额度用量更新", "Passive quota updates"), T("跟随 CPA 已记录的额度数据，不再自动查询上游；手动刷新仍可查询，实例数据刷新间隔继续生效", "Follow quota data recorded by CPA without automatic upstream queries; manual quota refresh and instance polling remain available"), func() {
			on := cur.PassiveQuota
			if ui.Switch(c.Key("passive-quota"), &on).Label(T("被动额度用量更新", "Passive quota updates")).Changed() {
				if save(func(s *store.Settings) { s.PassiveQuota = on }) {
					a.quotas = nil
					a.changed()
				}
			}
		})
		row(T("后台刷新间隔（秒）", "Background interval (s)"), T("不少于 10 秒。最多同时刷新 4 个实例，系统睡眠时暂停。", "At least 10 s. At most 4 instances refresh at once; refresh pauses while the system sleeps."), func() {
			ui.TextInput(c.Key("bg"), &st.background).Width(90).Label(T("后台刷新间隔（秒）", "Background interval (s)"))
		})
		row(T("当前实例刷新间隔（秒）", "Active instance interval (s)"), T("管理窗口或面板正在查看的实例，不少于 3 秒。", "The instance the window or panel shows; at least 3 s."), func() {
			ui.TextInput(c.Key("act"), &st.active).Width(90).Label(T("当前实例刷新间隔（秒）", "Active instance interval (s)"))
		})
		row(T("额度刷新间隔（分钟）", "Quota refresh interval (min)"), T("默认 5 分钟，至少 1 分钟，账号显示时按此间隔查询额度；开启被动额度用量更新后不生效", "Defaults to 5 minutes, at least 1 minute; queries quota at this interval while accounts are displayed, and has no effect with passive quota updates enabled"), func() {
			ui.TextInput(c.Key("quota-interval"), &st.quota).Width(90).Label(T("额度刷新间隔（分钟）", "Quota refresh interval (min)"))
		})
		row("", "", func() {
			if ui.Button(c, T("应用", "Apply")).Radius(999).Clicked() {
				bg, err1 := strconv.Atoi(st.background)
				ac, err2 := strconv.Atoi(st.active)
				qm, quotaErr := strconv.Atoi(st.quota)
				switch {
				case err1 != nil || err2 != nil:
					st.err = fmt.Errorf("%s", T("请输入整数秒数", "Enter whole seconds"))
				case quotaErr != nil || qm < 1 || qm > store.MaxQuotaRefreshMinutes:
					st.err = fmt.Errorf("%s", T("请输入有效的整数分钟数，且不少于 1 分钟", "Enter a valid whole number of minutes, at least 1"))
				case bg < 10 || ac < 3:
					st.err = fmt.Errorf("%s", T("后台间隔至少 10 秒，当前实例间隔至少 3 秒", "The background interval is at least 10 s and the active one at least 3 s"))
				default:
					save(func(s *store.Settings) { s.BackgroundRefresh, s.ActiveRefresh, s.QuotaRefreshMinutes = bg, ac, qm })
					if st.err == nil {
						a.changed()
						c.Toast(T("已应用", "Applied"))
					}
				}
			}
		})
	})

	settingsGroup(c, icGear, TintGray, T("系统", "System"), func(row func(label, detail string, control func())) {
		row(T("登录时启动", "Open at login"), T("登录系统后在菜单栏或托盘中运行。", "Run in the menu bar or tray after you log in."), func() {
			on := st.login
			if ui.Switch(c.Key("login"), &on).Label(T("登录时启动", "Open at login")).Changed() {
				st.loginErr = system.SetOpenAtLogin(on)
				if st.loginErr == nil {
					st.login = on
					save(func(s *store.Settings) { s.LaunchAtLogin = on })
				}
			}
		})
		detail := T("实例离线、恢复或账号异常时通知。", "Notify when an instance goes down, recovers, or accounts fail.")
		if !system.NotificationsSupported() {
			detail = T("当前系统不支持通知。", "Notifications are not supported here.")
		}
		row(T("通知", "Notifications"), detail, func() {
			on := cur.Notifications
			if ui.Switch(c.Key("notify"), &on).Label(T("通知", "Notifications")).Disabled(!system.NotificationsSupported()).Changed() {
				save(func(s *store.Settings) { s.Notifications = on })
			}
		})
	})
	ErrorBanner(c, st.loginErr)

	settingsGroup(c, icActivity, TintOrange, T("用量", "Usage"), func(row func(label, detail string, control func())) {
		row(T("采集用量", "Collect usage"), T("订阅每个实例的用量流，把请求、Token 和耗时保存在本机 30 天，供“用量”页查看。订阅期间 CPA 直接推送记录，不再写入其用量队列（/observability/usage/queue）。API 密钥只保存末 4 位，不保存客户端 IP 和请求头。", "Subscribes to the usage stream of each instance and keeps requests, tokens and latency on this computer for 30 days, for the Usage page. While subscribed, CPA pushes the records instead of adding them to its usage queue (/observability/usage/queue). Only the last 4 characters of API keys are kept; client addresses and headers are not."), func() {
			on := !cur.UsageOff
			if ui.Switch(c.Key("usage"), &on).Label(T("采集用量", "Collect usage")).Changed() {
				save(func(s *store.Settings) { s.UsageOff = !on })
				a.changed()
			}
		})
	})

	settingsGroup(c, icActivity, TintPurple, T("更新", "Updates"), func(row func(label, detail string, control func())) {
		if !UpdatesEnabled() {
			row(T("自动更新不可用", "Updates unavailable"), T("此版本不能自动更新（开发版本、软件包管理器安装或只读位置）。", "This build cannot update itself (a development build, a package manager install, or a read-only location)."), nil)
			return
		}
		row(T("自动检查", "Check automatically"), T("每 12 小时检查一次 GitHub Licoy/CPA-Bar 的正式版本，安装前校验签名；不收集任何使用数据。启动约 10 秒后进行首次检查。", "Checks the stable releases of Licoy/CPA-Bar on GitHub every 12 hours and verifies them before installing; no usage data is collected. The first check runs about 10 s after launch."), func() {
			on := AutomaticUpdateChecks()
			if ui.Switch(c.Key("autoupd"), &on).Label(T("自动检查", "Check automatically")).Changed() {
				save(func(s *store.Settings) { s.AutoCheckUpdates = on })
				SetAutomaticUpdateChecks(on)
			}
		})
		row(T("当前版本", "Current version"), "CPA-Bar "+versionLabel(), func() {
			if PillButton(c, icRefresh, T("立即检查更新", "Check Now"), false).Clicked() {
				CheckForUpdates()
			}
		})
	})
}

// settingsGroup is a panel of settings, one per row: a label with a muted
// description at the left, its control at the right.
func settingsGroup(c *ui.Context, icon *ui.SVG, k Tint, title string, rows func(row func(label, detail string, control func()))) {
	t := c.Theme()
	Panel(c).Gap(0).Children(func() {
		ui.Row(c).Padding(0, 0, 10, 0).Children(func() {
			PanelTitle(c, icon, k, title, "", false)
		})
		first := true
		rows(func(label, detail string, control func()) {
			if !first {
				ui.Box(c).Height(1).Background(pal(t).Line)
			}
			first = false
			ui.Row(c).Gap(16).Padding(10, 0).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					if label != "" {
						ui.Text(c, label).FontSize(13).FontWeight(500)
					}
					if detail != "" {
						ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted)
					}
				})
				if control != nil {
					control()
				}
			})
		})
	})
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

// trayIconName is what the platform calls where the tray icon sits.
func trayIconName() string {
	if runtime.GOOS == "darwin" {
		return T("菜单栏图标", "Menu bar icon")
	}
	return T("托盘图标", "Tray icon")
}

// trayStyleDetail says what a menu bar style shows.
func trayStyleDetail(style string) string {
	if style == store.TrayStyleBars {
		return T("以进度条显示最多 2 个账号的剩余额度（有 5 小时限额时显示 5 小时限额，没有则显示周限额或其他限额），旁边标出服务商、限额、剩余比例和刷新时间。账号取自默认实例（未设置时为第一个实例），置顶的账号优先，其余按账号列表顺序，跳过已停用和不支持额度的账号；悬停可查看各账号的额度。",
			"Shows the quota left of up to 2 accounts as bars (the 5-hour limit, else the weekly or another one), each with its provider, limit, percent left and refill time. Accounts are from the default instance (else the first), pinned ones first, then in the order of the account list, skipping disabled ones and those without a quota; hover for each account's quota.")
	}
	return T("显示 CPA-Bar 图标，可在旁边显示在线实例数。", "Shows the CPA-Bar icon, optionally with the number of instances online.")
}

// trayAccountsRow says which instance and accounts the bars show.
func trayAccountsRow(c *ui.Context, a *App, row func(label, detail string, control func())) {
	s, ok := a.trayInstance()
	detail := ""
	var creds []cpa.Credential
	switch {
	case !ok:
		detail = T("还没有实例。", "No instances yet.")
	case s.Profile.Paused:
		detail = fmt.Sprintf(T("实例“%s”已暂停，菜单栏显示图标。", "Instance “%s” is paused; the menu bar shows the icon."), s.Profile.Name)
	default:
		creds = trayAccounts(s, trayMaxBars)
		detail = fmt.Sprintf(T("实例：%s", "Instance: %s"), s.Profile.Name)
		if len(creds) == 0 {
			detail += T("。没有可显示额度的账号，菜单栏显示图标。", ". No account has a quota to show; the menu bar shows the icon.")
		}
	}
	row(T("显示的账号", "Accounts shown"), detail, func() {
		if len(creds) == 0 {
			return
		}
		t := c.Theme()
		ui.Column(c).Gap(6).Children(func() {
			for _, cr := range creds {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					credTile(c.Key("tray-"+credKeyOf(cr)), cr, 22, pal(t).Card)
					ui.Text(c, PrivName(cr.DisplayName())).FontSize(12).SingleLine()
					if s.Profile.Pinned(credFileName(cr)) {
						ui.Icon(c, icPinned).FontSize(11).TextColor(t.Accent).Label(T("已置顶", "Pinned")).Tooltip(T("已置顶", "Pinned"))
					}
				})
			}
		})
	})
}

// trayIconDetail says how the mono icon looks on the platform.
func trayIconDetail() string {
	switch runtime.GOOS {
	case "darwin":
		return T("纯色随菜单栏自动显示为黑色或白色；彩色显示 CPA-Bar 标志的颜色。不影响进度条样式。", "Mono turns black or white with the menu bar; color shows the logo in its colors. The bars style is not affected.")
	case "windows":
		return T("纯色随任务栏的深浅显示为白色或黑色；彩色显示 CPA-Bar 标志的颜色。", "Mono is white or black to suit the taskbar; color shows the logo in its colors.")
	}
	return T("纯色为白色，适合深色面板；彩色显示 CPA-Bar 标志的颜色。", "Mono is white, for dark panels; color shows the logo in its colors.")
}
