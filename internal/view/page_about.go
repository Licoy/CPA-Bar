package view

import (
	"runtime"

	"github.com/egoist/mygo/ui"
)

// RepoURL is the home page of the project, which the About page and the
// sidebar's bug report link open.
const RepoURL = "https://github.com/Licoy/CPA-Bar"

func init() {
	RegisterPage(Page{
		ID:    "about",
		Order: 10,
		Group: GroupAbout,
		Title: func() string { return T("关于", "About") },
		View:  aboutPage,
	})
	// Not a page: the sidebar opens the project's home page.
	RegisterPage(Page{
		ID:    "bug",
		Order: 20,
		Group: GroupAbout,
		Title: func() string { return T("BUG 反馈", "Report a Bug") },
		Link:  RepoURL,
	})
}

func aboutPage(c *ui.Context, env *PageEnv) {
	t := c.Theme()
	Panel(c).Gap(16).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			AppLogo(c, 64)
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "CPA-Bar").FontSize(22).FontWeight(700).SingleLine()
					Pill(c, versionLabel(), TintGray.C(t))
				})
				ui.Text(c, T("常驻菜单栏 / 系统托盘的 CLIProxyAPI 实例管理工具。", "A menu-bar / system-tray manager for CLIProxyAPI instances.")).
					FontSize(13).TextColor(t.TextMuted)
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			if PillButton(c, icExternal, T("打开仓库", "Open Repository"), false).Clicked() {
				c.OpenURL(RepoURL)
			}
			if UpdatesEnabled() && PillButton(c, icRefresh, T("检查更新", "Check for Updates"), false).Clicked() {
				CheckForUpdates()
			}
		})
	})
	Panel(c).Children(func() {
		PanelTitle(c, icInfo, TintBlue, T("应用信息", "About This App"), "", false)
		ui.Column(c).Children(func() {
			aboutRow(c, icTag, T("版本", "Version"), true, func() {
				ui.Text(c, "CPA-Bar "+versionLabel()).FontSize(13).Selectable()
			})
			aboutRow(c, icCpu, T("系统架构", "Platform"), true, func() {
				ui.Text(c, runtime.GOOS+"/"+runtime.GOARCH).FontSize(13).Selectable()
			})
			aboutRow(c, icGithub, T("仓库", "Repository"), false, func() {
				ui.Link(c, RepoURL, RepoURL).FontSize(13)
			})
		})
	})
}

// aboutRow is a line of the app's information: an icon, the label, and
// the value at the right, over a hairline unless it is the last.
func aboutRow(c *ui.Context, icon *ui.SVG, label string, line bool, value func()) {
	t := c.Theme()
	row := ui.Row(c).Gap(10).Padding(10, 0).AlignItems(ui.Center)
	if line {
		row.BorderWidth(0, 0, 1, 0).BorderColor(pal(t).Line)
	}
	row.Children(func() {
		ui.Icon(c, icon).FontSize(15).TextColor(t.TextMuted)
		ui.Text(c, label).FontSize(13).TextColor(t.TextMuted).Grow(1).Basis(0).SingleLine()
		value()
	})
}
