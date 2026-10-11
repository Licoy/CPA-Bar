// Package view is CPA-Bar's native interface: the tray icon, its panel and
// the management window, drawn with mygo's ui package. Views run on the
// main thread and read the core service's snapshots every frame; the
// service's changes only ask the windows for a new frame.
package view

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
	"github.com/licoy/cpa-bar/internal/usage"
)

// Panel and window sizes in DIPs; the flyout shrinks to the work area.
const (
	panelWidth    = 420
	panelHeight   = 560
	managerWidth  = 1100
	managerHeight = 760
)

// App owns the tray, the panel and the management window. Its fields are
// used on the main thread only, except where noted.
type App struct {
	Svc *core.Service
	// Usage collects the usage records; nil when there is none (tests).
	Usage *usage.Manager

	tray    *mygo.Tray
	panel   *mygo.Window
	manager *mygo.Window

	// panelInstance is the instance the panel shows, "" for all.
	panelInstance string
	panelTask     Task
	panelCreds    map[string]*Task // per instance+credential toggles
	panelLists    map[string]*panelList
	panelPopover  panelPopoverState
	panelSafeArea panelInsets
	// quotas caches the quota of the accounts, per instance.
	quotas map[string]*quotaInstance

	// selected is the instance of the management window, page its page.
	selected string
	page     string

	pageState map[string]any

	// lastTray avoids touching the tray when nothing changed.
	lastTray trayState

	quitting     bool
	pending      atomic.Bool
	usagePending atomic.Bool

	evMu sync.Mutex // guards nothing but the order of notifications
}

// NewApp builds the interface around a service. Call Ready once the app is
// ready.
func NewApp(svc *core.Service) *App {
	a := &App{
		Svc:        svc,
		pageState:  map[string]any{},
		panelCreds: map[string]*Task{},
		page:       "overview",
	}
	st := svc.Settings()
	SetPrivacy(st.Privacy)
	setPrivacyHosts(st.Instances)
	a.panelInstance = st.DefaultInstanceID
	if len(st.Instances) > 0 {
		a.selected = st.Instances[0].ID
		if st.DefaultInstanceID != "" {
			a.selected = st.DefaultInstanceID
		}
	}
	return a
}

// ApplyAppearance follows the appearance setting. It may run before
// App.Run.
func ApplyAppearance(appearance string) {
	switch appearance {
	case store.AppearanceDark:
		system.SetThemeSource(mygo.ThemeDark)
	case store.AppearanceLight:
		system.SetThemeSource(mygo.ThemeLight)
	default:
		system.SetThemeSource(mygo.ThemeSystem)
	}
}

// Ready creates the tray and starts following the service.
func (a *App) Ready() {
	var err error
	look := a.trayLook(trayHealthy)
	icon, template := trayIcon(look)
	a.tray, err = mygo.NewTray(mygo.TrayOptions{
		Icon:           icon,
		IconIsTemplate: template,
		ToolTip:        "CPA-Bar",
	})
	if err != nil {
		// Linux without AppIndicator: say what is missing, and still
		// offer the window.
		log.Println("tray:", err)
		mygo.Dialog.Error(T("无法创建托盘图标", "Cannot create the tray icon"),
			T("Linux 需要安装 libayatana-appindicator3（例如 sudo apt install libayatana-appindicator3-1）。管理窗口仍可使用。",
				"Linux needs libayatana-appindicator3 (e.g. sudo apt install libayatana-appindicator3-1). The management window still works.")+"\n\n"+err.Error())
		a.OpenManager("")
	} else {
		a.lastTray.look = look
		a.wireTray()
		// A mono icon follows the taskbar, which may turn light or dark.
		mygo.Theme.OnUpdated(a.updateTray)
		// The panel's popover follows the app's appearance, not the menu
		// bar's.
		mygo.Theme.OnUpdated(a.matchPanelAppearance)
	}

	a.Svc.Subscribe(func() {
		// Often from a goroutine of the service: hop to the main thread
		// without waiting, and fold bursts of changes into one.
		if a.pending.CompareAndSwap(false, true) {
			go mygo.RunOnMain(func() {
				a.pending.Store(false)
				a.changed()
			})
		}
	})
	if a.Usage != nil {
		a.Usage.Subscribe(func() {
			if a.usagePending.CompareAndSwap(false, true) {
				go mygo.RunOnMain(func() {
					a.usagePending.Store(false)
					a.invalidate()
				})
			}
		})
	}
	mygo.Power.OnSuspend(a.Svc.Suspend)
	mygo.Power.OnResume(a.Svc.Resume)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.tray != nil {
			a.tray.Destroy()
		}
	})
	a.Svc.Start()
	a.changed()
	if len(a.Svc.Settings().Instances) == 0 {
		// First launch: there is nothing to watch until an instance is
		// added.
		a.OpenManager("instances")
	}
}

func (a *App) wireTray() {
	if runtime.GOOS == "linux" {
		// AppIndicator shows menus only and reports no clicks.
		a.tray.SetMenu(a.trayMenu())
		return
	}
	a.tray.OnClick(a.TogglePanel)
	a.tray.OnRightClick(func() {
		a.ClosePanel()
		a.tray.SetMenu(a.trayMenu())
		defer a.tray.SetMenu(nil)
		a.tray.PopUpMenu()
	})
}

func (a *App) trayMenu() *mygo.Menu {
	o := a.Svc.Overview()
	items := []*mygo.MenuItem{
		{Label: overviewLine(o), Disabled: true},
		mygo.Separator(),
		{Label: T("打开面板", "Open Panel"), Click: func(*mygo.MenuItem, *mygo.Window) { a.OpenPanel() }},
		{Label: T("打开管理窗口", "Open Manager"), Click: func(*mygo.MenuItem, *mygo.Window) { a.OpenManager("") }},
		{Label: T("全部刷新", "Refresh All"), Click: func(*mygo.MenuItem, *mygo.Window) { a.Svc.RefreshAll() }},
		mygo.Separator(),
		{Label: T("检查更新…", "Check for Updates…"), Click: func(*mygo.MenuItem, *mygo.Window) { CheckForUpdates() }},
		{Label: T("退出 CPA-Bar", "Quit CPA-Bar"), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	}
	return mygo.NewMenu(items)
}

// overviewLine is the one-line health summary of the tray.
func overviewLine(o core.Overview) string {
	if o.Instances == 0 {
		return T("尚未添加实例", "No instances yet")
	}
	s := fmt.Sprintf(T("实例 %d/%d 在线 · 可用账号 %d · 异常 %d", "%d/%d online · %d accounts available · %d abnormal"),
		o.Online, o.Instances, o.Accounts.Available, o.Accounts.Abnormal())
	if o.Stale {
		s += T("（部分数据已过期）", " (some data stale)")
	}
	return s
}

// changed runs on the main thread after every service change.
func (a *App) changed() {
	setPrivacyHosts(a.Svc.Settings().Instances)
	a.updateTray()
	a.invalidate()
	a.syncActive()
	a.syncUsage()
}

// invalidate redraws the open windows.
func (a *App) invalidate() {
	if a.panel != nil {
		a.panel.Invalidate()
	}
	if a.manager != nil {
		a.manager.Invalidate()
	}
}

// syncUsage collects the usage of the instances that are not paused and
// have a key, unless collection is off.
func (a *App) syncUsage() {
	if a.Usage == nil {
		return
	}
	a.Usage.SetEnabled(!a.Svc.Settings().UsageOff)
	var targets []usage.Target
	for _, s := range a.Svc.Snapshots() {
		t := usage.Target{ID: s.Profile.ID}
		if !s.Profile.Paused {
			t.Client, _ = a.Svc.Client(s.Profile.ID)
			t.Plus, _ = a.Svc.ManagerPlus(s.Profile.ID)
			if s.Profile.ManagerPlus() && t.Plus == nil {
				// Without its admin key, nothing: the stream would take
				// the records Manager-Plus collects.
				t.Client = nil
			}
		}
		targets = append(targets, t)
	}
	a.Usage.Sync(targets)
}

// syncActive tells the service which instance the user is looking at.
func (a *App) syncActive() {
	id := ""
	switch {
	case a.manager != nil && a.manager.IsVisible():
		id = a.selected
	case a.panel != nil:
		id = a.panelInstance
	}
	a.Svc.SetActive(id)
}

type trayState struct {
	look  trayLook
	title string
	tip   string
}

// trayLook is how the tray icon is drawn in a state of health, by the
// settings.
func (a *App) trayLook(h trayHealth) trayLook {
	mono := !a.Svc.Settings().TrayIconColor
	return trayLook{health: h, mono: mono, light: mono && taskbarLight()}
}

func (a *App) updateTray() {
	if a.tray == nil {
		return
	}
	o := a.Svc.Overview()
	health := trayHealthy
	switch {
	case o.Instances == 0:
		health = trayIdle
	case o.Problem > 0 || o.Accounts.Abnormal() > 0:
		health = trayProblem
	}
	settings := a.Svc.Settings()
	st := trayState{look: a.trayLook(health), tip: "CPA-Bar\n" + overviewLine(o)}
	if style, limit := trayGaugeStyle(settings.TrayStyle); limit > 0 {
		gauges, lines := a.trayGauges(limit, time.Now())
		if len(gauges) > 0 {
			st.look.style, st.look.n = style, copy(st.look.gauges[:], gauges)
			st.tip += "\n" + strings.Join(lines, "\n")
		}
	}
	if st.look.n == 0 && settings.TrayText && o.Instances > 0 && runtime.GOOS == "darwin" {
		st.title = fmt.Sprintf("%d/%d", o.Online, o.Instances)
		if n := o.Accounts.Abnormal(); n > 0 {
			st.title += fmt.Sprintf(" !%d", n)
		}
	}
	if st == a.lastTray {
		return
	}
	if st.look != a.lastTray.look {
		_ = a.tray.SetIcon(trayIcon(st.look))
	}
	if st.title != a.lastTray.title {
		a.tray.SetTitle(st.title)
	}
	if st.tip != a.lastTray.tip {
		a.tray.SetToolTip(st.tip)
		if runtime.GOOS == "linux" {
			a.tray.SetMenu(a.trayMenu()) // the summary line lives in the menu
		}
	}
	a.lastTray = st
}

// ---- Panel ----

// TogglePanel opens the panel, or closes it when open.
func (a *App) TogglePanel() {
	if a.panel != nil {
		a.ClosePanel()
		return
	}
	a.OpenPanel()
}

// OpenPanel shows the light panel next to the tray icon.
func (a *App) OpenPanel() {
	if a.panel != nil {
		a.panel.Focus()
		return
	}
	a.panelLists = nil
	stopObserving := observePanelPopover(a, a.Svc.Settings().PanelOpaque && panelOpacitySupported())
	opts := mygo.FlyoutOptions{
		Tray:      a.tray,
		Placement: mygo.PlacementBottom,
		Gap:       4,
		Width:     panelWidth,
		Height:    panelHeight,
		Focusable: true,
		Shadow:    true,
		Popover:   runtime.GOOS == "darwin",
		Content:   ui.View(a.panelView),
	}
	func() {
		defer stopObserving()
		a.panel = mygo.NewFlyout(opts)
	}()
	panel := a.panel
	panel.OnShow(a.matchPanelAppearance)
	panel.OnFocus(a.matchPanelAppearance)
	a.matchPanelAppearance()
	panel.Invalidate()
	a.panel.OnClosed(func() {
		releasePanelPopover(a)
		a.panel = nil
		a.syncActive()
	})
	a.syncActive()
}

// ClosePanel closes the panel.
func (a *App) ClosePanel() {
	if a.panel != nil {
		a.panel.Close()
	}
}

// ---- Management window ----

// OpenManager shows the management window, on page when not empty.
func (a *App) OpenManager(page string) {
	if _, ok := pageByID(page); ok {
		// Links of the sidebar and unknown ids are not pages to show.
		a.page = page
	}
	a.ClosePanel()
	if a.manager != nil {
		a.manager.Show()
		a.manager.Focus()
		a.syncActive()
		return
	}
	// Page state holds list and editor state tied to the window it was
	// shown in: a new window starts afresh.
	a.pageState = map[string]any{}
	a.manager = mygo.NewWindow(mygo.WindowOptions{
		StateKey:  "manager",
		Title:     "CPA-Bar",
		Width:     managerWidth,
		Height:    managerHeight,
		MinWidth:  820,
		MinHeight: 560,
		// The sidebar runs up under the window controls, as in Pulse.
		TitleBarStyle:        mygo.TitleBarHidden,
		TrafficLightPosition: &mygo.Point{X: 18, Y: 20},
		Content:              ui.View(a.managerView),
	})
	a.manager.OnClosed(func() {
		a.manager = nil
		a.syncActive()
	})
	if runtime.GOOS != "darwin" {
		_ = a.manager.SetIcon(appIconPNG)
	}
	if runtime.GOOS == "darwin" {
		// A menu bar app shows a Dock icon only while its window is open,
		// so the window can be found with Cmd+Tab. Outside an app bundle,
		// as in development, the Dock has no icon of the app's own: it is
		// set once the Dock shows the app, which gives it the executable's
		// icon on every show.
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyRegular)
		if exe, err := os.Executable(); err == nil && !strings.Contains(exe, ".app/Contents/MacOS/") {
			_ = mygo.App.Dock.SetIcon(appIconPNG)
		}
		a.manager.OnClosed(func() { mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory) })
		mygo.App.Focus()
	}
	a.manager.Focus()
	a.syncActive()
}

// OpenInstance opens the management window on an instance.
func (a *App) OpenInstance(id, page string) {
	if id != "" {
		a.selected = id
	}
	a.OpenManager(page)
}

// Notify shows a desktop notification for a service event, when the user
// allows them. It may run on any goroutine.
func (a *App) Notify(e core.Event) {
	if !a.Svc.Settings().Notifications || !system.NotificationsSupported() {
		return
	}
	a.evMu.Lock()
	defer a.evMu.Unlock()
	var title, body string
	switch e.Kind {
	case core.EventInstanceDown:
		title = fmt.Sprintf(T("%s：%s", "%s: %s"), e.InstanceName, ConnLabel(e.Conn))
		body = ErrorText(e.Err)
	case core.EventRecovered:
		title = fmt.Sprintf(T("%s 已恢复在线", "%s is back online"), e.InstanceName)
	case core.EventAccountsAbnormal:
		title = fmt.Sprintf(T("%s：%d 个账号异常", "%s: %d accounts need attention"), e.InstanceName, len(e.Accounts))
		var names []string
		for i, n := range e.Accounts {
			if i == 5 {
				names = append(names, "…")
				break
			}
			names = append(names, PrivName(n))
		}
		body = strings.Join(names, "、")
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "cpabar/" + e.InstanceID,
		Group: e.InstanceID,
		Title: title,
		Body:  body,
	})
	id := e.InstanceID
	n.OnClick(func() {
		a.OpenInstance(id, "overview")
		n.Close()
	})
	if err := n.Show(); err != nil {
		log.Println("notification:", err)
	}
}

// StateFor returns the state of a page for an instance, made by init the
// first time. Keeping page state per instance keeps one instance's data
// and pending results off another's page.
func StateFor[T any](a *App, page, instanceID string, init func() *T) *T {
	key := page + "\x00" + instanceID
	if v, ok := a.pageState[key].(*T); ok {
		return v
	}
	v := init()
	a.pageState[key] = v
	return v
}

// dropInstanceState forgets the page state of a removed instance.
func (a *App) dropInstanceState(id string) {
	delete(a.quotas, id)
	if a.Usage != nil {
		_ = a.Usage.Clear(id)
	}
	for k := range a.pageState {
		if strings.HasSuffix(k, "\x00"+id) {
			delete(a.pageState, k)
		}
	}
	for k := range a.panelCreds {
		if strings.HasPrefix(k, id+"\x00") {
			delete(a.panelCreds, k)
		}
	}
}
