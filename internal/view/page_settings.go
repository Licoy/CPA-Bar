package view

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
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
				styles := []string{store.TrayStyleIcon, store.TrayStyleBattery, store.TrayStyleBars}
				i := max(0, indexOf(styles, cur.TrayStyle))
				if PillTabs(c.Key("traystyle"), &i, T("图标", "Icon"), T("电池", "Batteries"), T("进度条", "Bars")) {
					style := styles[i]
					save(func(s *store.Settings) { s.TrayStyle = style })
					a.changed()
				}
			})
			if cur.TrayStyle == store.TrayStyleIcon {
				row(T("菜单栏文字", "Menu bar text"), T("在图标旁显示“3/4”（在线实例/实例总数）", "Show “3/4” (online/all instances) next to the icon"), func() {
					on := cur.TrayText
					if ui.Switch(c.Key("traytext"), &on).Label(T("菜单栏文字", "Menu bar text")).Changed() {
						save(func(s *store.Settings) { s.TrayText = on })
						a.changed()
					}
				})
			}
		}
	})

	settingsGroup(c, icRefresh, TintGreen, T("刷新", "Refresh"), func(row func(label, detail string, control func())) {
		row(T("后台刷新间隔（秒）", "Background interval (s)"), T("不少于 10 秒。最多同时刷新 4 个实例，系统睡眠时暂停。", "At least 10 s. At most 4 instances refresh at once; refresh pauses while the system sleeps."), func() {
			ui.TextInput(c.Key("bg"), &st.background).Width(90).Label(T("后台刷新间隔（秒）", "Background interval (s)"))
		})
		row(T("当前实例刷新间隔（秒）", "Active instance interval (s)"), T("管理窗口或面板正在查看的实例，不少于 3 秒。", "The instance the window or panel shows; at least 3 s."), func() {
			ui.TextInput(c.Key("act"), &st.active).Width(90).Label(T("当前实例刷新间隔（秒）", "Active instance interval (s)"))
		})
		row("", "", func() {
			if ui.Button(c, T("应用", "Apply")).Radius(999).Clicked() {
				bg, err1 := strconv.Atoi(st.background)
				ac, err2 := strconv.Atoi(st.active)
				switch {
				case err1 != nil || err2 != nil:
					st.err = fmt.Errorf("%s", T("请输入整数秒数", "Enter whole seconds"))
				case bg < 10 || ac < 3:
					st.err = fmt.Errorf("%s", T("后台间隔至少 10 秒，当前实例间隔至少 3 秒", "The background interval is at least 10 s and the active one at least 3 s"))
				default:
					save(func(s *store.Settings) { s.BackgroundRefresh, s.ActiveRefresh = bg, ac })
					if st.err == nil {
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
	from := T("账号取自默认实例（未设置时为第一个实例），按账号列表顺序，跳过已停用和不支持额度的账号；悬停可查看各账号的额度。",
		"Accounts are from the default instance (else the first), in the order of the account list, skipping disabled ones and those without a quota; hover for each account's quota.")
	switch style {
	case store.TrayStyleBattery:
		return T("以竖向电池显示最多 6 个账号的剩余额度（5 小时与周限额中较低的一个）。", "Shows the quota left of up to 6 accounts as upright batteries (the lower of the 5-hour and weekly limits). ") + from
	case store.TrayStyleBars:
		return T("以进度条显示最多 2 个账号：上方为 5 小时限额，下方为周限额（彩色时分别为蓝色与紫色）；没有 5 小时限额的账号只显示周限额。", "Shows up to 2 accounts as bars: the 5-hour limit over the weekly one (blue and purple in color); accounts without a 5-hour limit show the weekly one alone. ") + from
	}
	return T("显示 CPA-Bar 图标，可在旁边显示在线实例数。", "Shows the CPA-Bar icon, optionally with the number of instances online.")
}

// trayIconDetail says how the mono icon looks on the platform.
func trayIconDetail() string {
	switch runtime.GOOS {
	case "darwin":
		return T("纯色随菜单栏自动显示为黑色或白色；彩色显示 CPA-Bar 标志的颜色。电池和进度条样式同样区分纯色与彩色。", "Mono turns black or white with the menu bar; color shows the logo in its colors. The battery and bar styles follow this too.")
	case "windows":
		return T("纯色随任务栏的深浅显示为白色或黑色；彩色显示 CPA-Bar 标志的颜色。", "Mono is white or black to suit the taskbar; color shows the logo in its colors.")
	}
	return T("纯色为白色，适合深色面板；彩色显示 CPA-Bar 标志的颜色。", "Mono is white, for dark panels; color shows the logo in its colors.")
}
