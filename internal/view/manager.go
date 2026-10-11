package view

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// Page is a section of the management window. Pages register themselves
// with RegisterPage from an init function of their file.
type Page struct {
	ID    string
	Order int
	// Group places the page in a sidebar section.
	Group PageGroup
	Title func() string
	// Instance pages act on the selected instance and are shown only when
	// one is selected.
	Instance bool
	View     func(c *ui.Context, env *PageEnv)
	// Link makes the item an external link rather than a page: the sidebar
	// opens it in the browser and never selects it (it has no View).
	Link string
}

// PageGroup is a section of the sidebar.
type PageGroup int

const (
	GroupMonitor PageGroup = iota
	GroupManage
	GroupApp
	GroupAbout
)

func (g PageGroup) title() string {
	switch g {
	case GroupMonitor:
		return T("监控", "Monitor")
	case GroupManage:
		return T("管理", "Manage")
	case GroupAbout:
		return T("关于", "About")
	}
	return T("应用", "App")
}

var pages []Page

// RegisterPage adds a page to the management window.
func RegisterPage(p Page) {
	pages = append(pages, p)
	slices.SortStableFunc(pages, func(a, b Page) int {
		if a.Group != b.Group {
			return int(a.Group) - int(b.Group)
		}
		return a.Order - b.Order
	})
}

// pageByID finds a page that can be shown; links are not pages.
func pageByID(id string) (Page, bool) {
	for _, p := range pages {
		if p.ID == id && p.Link == "" {
			return p, true
		}
	}
	return Page{}, false
}

// PageEnv is what a page view gets: the app, its window, and the selected
// instance with its latest snapshot and client.
type PageEnv struct {
	App *App
	Win *mygo.Window
	// InstanceID is the selected instance, "" when there is none.
	InstanceID string
	Snap       core.InstanceSnapshot
	// Client is nil when the instance has no usable key; ClientErr says
	// why.
	Client    *cpa.Client
	ClientErr error
	Now       time.Time
}

// InstanceName is the name every write operation shows as its target.
func (e *PageEnv) InstanceName() string { return e.Snap.Profile.Name }

// Run runs a task of the page against the selected instance; see Run.
func (e *PageEnv) Run(t *Task, fn func(ctx context.Context) error, then func(err error)) {
	Run(e.Win, t, 0, fn, then)
}

// State returns the page's state for the selected instance.
func State[T any](e *PageEnv, page string, init func() *T) *T {
	return StateFor(e.App, page, e.InstanceID, init)
}

// pageLook is how the sidebar shows a page.
type pageLook struct {
	icon *ui.SVG
	tint Tint
}

var pageLooks = map[string]pageLook{
	"overview":  {icGrid, TintGray},
	"logs":      {icLogs, TintTeal},
	"accounts":  {icUsers, TintGreen},
	"oauth":     {icLogin, TintPurple},
	"providers": {icCloud, TintIndigo},
	"keys":      {icKey, TintYellow},
	"routing":   {icRoute, TintOrange},
	"config":    {icFileCog, TintPink},
	"plugins":   {icPuzzle, TintBlue},
	"instances": {icServer, TintGray},
	"settings":  {icGear, TintGray},
	"about":     {icInfo, TintBlue},
	"bug":       {icBug, TintRed},
}

func lookOf(id string) pageLook {
	if l, ok := pageLooks[id]; ok {
		return l
	}
	return pageLook{icGrid, TintGray}
}

const sidebarWidth = 240

func (a *App) managerView(c *ui.Context) {
	t := applyTheme(c)
	snaps := a.Svc.Snapshots()
	if a.selected != "" && !slices.ContainsFunc(snaps, func(s core.InstanceSnapshot) bool { return s.Profile.ID == a.selected }) {
		a.selected = ""
	}
	if a.selected == "" && len(snaps) > 0 {
		a.selected = snaps[0].Profile.ID
		a.syncActive()
	}
	if _, ok := pageByID(a.page); !ok {
		a.page = "overview"
		if i := slices.IndexFunc(pages, func(p Page) bool { return p.Link == "" }); i >= 0 {
			a.page = pages[i].ID
		}
	}

	ui.Row(c).Fill().AlignItems(ui.Stretch).Background(t.Background).Children(func() {
		ui.Column(c).Width(sidebarWidth).Background(pal(t).Sidebar).BorderWidth(0, 1, 0, 0).BorderColor(pal(t).Line).Children(func() {
			a.sidebar(c, snaps)
		})
		ui.Column(c).Grow(1).Basis(0).Children(func() {
			a.managerContent(c, snaps)
		})
	})
}

// sidebar lists the pages under the window controls, and the instance the
// instance pages act on at its foot.
func (a *App) sidebar(c *ui.Context, snaps []core.InstanceSnapshot) {
	t := c.Theme()
	// Room for the window controls, which full screen hides (the title
	// bar is zero there): then only a margin.
	bar := c.TitleBar()
	top := float32(14)
	if bar.Height > 0 {
		top = max(bar.Height, 52)
	}
	ui.Row(c).Height(top).DragWindow()
	snap, haveSnap := a.Svc.Snapshot(a.selected)
	ui.Scroll(c.Key("sidebar")).Grow(1).Padding(0, 10, 10, 10).Gap(2).Children(func() {
		for gi, g := range []PageGroup{GroupMonitor, GroupManage, GroupApp, GroupAbout} {
			if gi > 0 {
				ui.Text(c, g.title()).FontSize(12).FontWeight(600).TextColor(t.TextMuted).Padding(14, 8, 4, 8)
			}
			for _, p := range pages {
				if p.Group != g {
					continue
				}
				if a.navItem(c, p, a.sidebarValue(p.ID, snap, haveSnap, len(snaps))) {
					if p.Link != "" {
						c.OpenURL(p.Link)
					} else {
						a.page = p.ID
					}
				}
			}
		}
	})
	a.instanceSwitcher(c, snaps)
}

// sidebarValue is the muted figure at the right of a page in the sidebar.
func (a *App) sidebarValue(id string, s core.InstanceSnapshot, ok bool, instances int) string {
	switch id {
	case "overview":
		if ok {
			return ConnLabel(s.Conn)
		}
	case "accounts":
		if ok && s.HasData {
			return staleNumIf(s.Accounts.Available, s) + "/" + staleNumIf(s.Accounts.Total, s)
		}
	case "instances":
		return strconv.Itoa(instances)
	case "settings":
		return Version
	}
	return ""
}

// navItem is a page in the sidebar; it reports a click.
func (a *App) navItem(c *ui.Context, p Page, value string) bool {
	t := c.Theme()
	look := lookOf(p.ID)
	sel := p.Link == "" && a.page == p.ID
	b := ui.ButtonBase(c.Key("nav-"+p.ID)).Label(p.Title()).Padding(5, 8).Gap(10).Radius(8).AlignItems(ui.Center)
	b.Role(ui.RoleTab).Selected(sel)
	text, muted := t.Text, t.TextMuted
	switch {
	case sel:
		b.Background(t.Accent)
		text, muted = t.AccentText, t.AccentText.Alpha(0.8)
	case b.Hovered():
		b.Background(pal(t).Hover)
	}
	b.Children(func() {
		if sel {
			ui.Row(c).Size(24, 24).Radius(7).Background(ui.RGBA(255, 255, 255, 0.22)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, look.icon).Size(14, 14).TextColor(t.AccentText)
			})
		} else {
			Tile(c, look.icon, look.tint, 24, true)
		}
		ui.Text(c, p.Title()).FontSize(14).TextColor(text).SingleLine().Grow(1)
		if value != "" {
			ui.Text(c, value).FontSize(12).TextColor(muted).FontFeatures("tnum").SingleLine()
		}
		if p.Link != "" {
			// Leaves the app: it opens in the browser.
			ui.Icon(c, icExternal).Size(13, 13).TextColor(muted)
		}
	})
	return b.Clicked()
}

// manageInstances is the switcher's item that opens the Instances page.
const manageInstances = "\x00manage"

// instanceSwitcher shows the selected instance at the foot of the sidebar,
// as Pulse shows the Mac, and chooses another.
func (a *App) instanceSwitcher(c *ui.Context, snaps []core.InstanceSnapshot) {
	t := c.Theme()
	ui.Column(c).Padding(8, 10, 12, 10).BorderWidth(1, 0, 0, 0).BorderColor(pal(t).Line).Children(func() {
		if len(snaps) == 0 {
			ui.Column(c).Padding(6, 8).Gap(2).Children(func() {
				ui.Text(c, T("尚未添加实例", "No instances")).FontSize(13).FontWeight(600)
				ui.Text(c, "CPA-Bar "+Version).FontSize(11).TextColor(t.TextMuted)
			})
			return
		}
		a.instanceSelect(c, "instance", snaps, false, func(sel ui.Element, s core.InstanceSnapshot) {
			sel.Padding(6, 8).Gap(8).Radius(8).AlignItems(ui.Center)
			if sel.Hovered() {
				sel.Background(pal(t).Hover)
			}
			sel.Children(func() {
				instanceTile(c, s, 28, pal(t).Sidebar)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Text(c, s.Profile.Name).FontSize(13).FontWeight(600).SingleLine()
					sub := ConnLabel(s.Conn)
					if s.Server.Version != "" {
						sub = "CPA " + s.Server.Version + " · " + sub
					}
					ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).SingleLine()
				})
				ui.Icon(c, icChevron).FontSize(12).TextColor(t.TextMuted).Rotate(-90)
			})
		})
	})
}

func (a *App) managerContent(c *ui.Context, snaps []core.InstanceSnapshot) {
	t := c.Theme()
	p, ok := pageByID(a.page)
	if !ok {
		return
	}
	env := &PageEnv{App: a, Win: a.manager, Now: c.Now()}
	snap, found := a.Svc.Snapshot(a.selected)
	if p.Instance && found {
		env.InstanceID = a.selected
		env.Snap = snap
		env.Client, env.ClientErr = a.Svc.Client(a.selected)
	}
	bar := c.TitleBar()
	// The header names the page and, on instance pages, the target of
	// every operation.
	ui.Row(c).Height(max(bar.Height, 52)).Padding(0, bar.Right+16, 0, 24).Gap(10).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Text(c, p.Title()).FontSize(15).FontWeight(700).SingleLine()
		if p.Instance && found {
			a.instanceSelect(c, "header-instance", snaps, true, func(sel ui.Element, s core.InstanceSnapshot) {
				sel.Padding(3, 8, 3, 4).Gap(6).Radius(999).AlignItems(ui.Center).Border(1, pal(t).Line)
				bg := pal(t).Card
				if sel.Hovered() {
					bg = pal(t).Hover
				}
				sel.Background(bg).Children(func() {
					instanceTile(c, s, 20, bg)
					ui.Text(c, s.Profile.Name).FontSize(12).FontWeight(600).SingleLine()
					ui.Icon(c, icChevronDown).FontSize(11).TextColor(t.TextMuted)
				})
			})
		}
		ui.Box(c).Grow(1)
		if p.Instance && found {
			Pill(c, ConnLabel(snap.Conn), ConnColor(t, snap.Conn))
			if BusyPillButton(c, icRefresh, T("刷新", "Refresh"), instanceBusy(snap), snap.Profile.Paused).Clicked() {
				a.Svc.Refresh(a.selected)
			}
		}
	})
	if p.Instance && !found {
		ui.Column(c).Grow(1).Children(func() {
			EmptyState(c, T("请先添加实例", "Add an instance first"),
				T("在“实例”页添加一个 CPA 服务后即可管理。", "Add a CPA server on the Instances page to manage it."))
			ui.Row(c).Justify(ui.Center).Padding(0, 0, 48, 0).Children(func() {
				if ui.PrimaryButton(c, T("添加实例", "Add Instance")).Clicked() {
					a.page = "instances"
				}
			})
		})
		return
	}
	// The page is at least as tall as the space under the header, so that
	// an element growing in it (a list, a log) takes the height the window
	// leaves, and follows it as the window is resized. With more above it
	// than fits, it keeps its least height and the page scrolls.
	_, h := c.Size()
	ui.Scroll(c.Key("page-" + p.ID + "-" + env.InstanceID)).Grow(1).Children(func() {
		ui.Column(c).MinHeight(max(h-max(bar.Height, 52), 0)).Padding(6, 24, 24, 24).Gap(16).Children(func() {
			if p.Instance && env.Client == nil {
				ErrorBanner(c, env.ClientErr)
			}
			p.View(c, env)
		})
	})
}

// fillHeight makes e take the height left on the page, and at least least.
func fillHeight(e ui.Element, least float32) ui.Element {
	return e.Grow(1).Basis(0).MinHeight(least)
}

// dialogHeight is the height of the scrolling part of a dialog: want, or
// less in a window that leaves only room for it besides chrome, the rest
// of the dialog and its margins. It is never under 160.
func dialogHeight(c *ui.Context, want, chrome float32) float32 {
	_, h := c.Size()
	return max(160, min(want, h-chrome))
}
