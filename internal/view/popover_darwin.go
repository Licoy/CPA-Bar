//go:build darwin

package view

import (
	"structs"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

type panelPopoverState struct {
	popover       objc.ID
	view          objc.ID
	stopDismissal func()
}

type nativeEdgeInsets struct {
	_                        structs.HostLayout
	Top, Left, Bottom, Right float64
}

var popoverWillShowNotification = func() objc.ID {
	lib, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	sym, err := purego.Dlsym(lib, "NSPopoverWillShowNotification")
	if err != nil {
		panic(err)
	}
	return *(*objc.ID)(unsafe.Pointer(sym))
}()

func panelOpacitySupported() bool {
	class := objc.ID(objc.GetClass("NSPopover"))
	return class != 0 && objc.Send[bool](class, objc.RegisterName("instancesRespondToSelector:"), objc.RegisterName("setHasFullSizeContent:"))
}

// observePanelPopover scopes the public notification to NewFlyout's
// synchronous creation, and keeps that popover until its window closes.
func observePanelPopover(a *App, opaque bool) func() {
	center := objc.ID(objc.GetClass("NSNotificationCenter")).Send(objc.RegisterName("defaultCenter"))
	block := objc.NewBlock(func(_ objc.Block, notification objc.ID) {
		popover := notification.Send(objc.RegisterName("object"))
		configurePanelPopover(popover, opaque)
		popover.Send(objc.RegisterName("retain"))
		releasePanelPopover(a)
		a.panelPopover.popover = popover
		controller := popover.Send(objc.RegisterName("contentViewController"))
		a.panelPopover.view = controller.Send(objc.RegisterName("view"))
		a.panelPopover.stopDismissal = observePanelDismissal(a.tray, a.ClosePanel)
	})
	token := center.Send(objc.RegisterName("addObserverForName:object:queue:usingBlock:"), popoverWillShowNotification, objc.ID(0), objc.ID(0), block)
	return func() {
		center.Send(objc.RegisterName("removeObserver:"), token)
		block.Release()
	}
}

// AppKit's transient popovers do not dismiss for every outside interaction,
// including some menu-bar panels. Own these events for the panel's lifetime.
func observePanelDismissal(tray *mygo.Tray, closePanel func()) func() {
	events := objc.ID(objc.GetClass("NSEvent"))
	dismiss := func() {
		// The tray's click toggles the panel itself; closing on mouse-down
		// here would let its mouse-up open a new panel immediately. Do not
		// check pressedMouseButtons: a quick click may already be released
		// when AppKit delivers these events.
		if tray != nil {
			p, b := mygo.Screen.CursorScreenPoint(), tray.Bounds()
			if p.X >= b.X && p.Y >= b.Y && p.X < b.X+b.Width && p.Y < b.Y+b.Height {
				return
			}
		}
		closePanel()
	}
	click := objc.NewBlock(func(_ objc.Block, _ objc.ID) { dismiss() })
	const mouseDownMask = 1<<1 | 1<<3 | 1<<25 // left, right and other buttons
	monitor := events.Send(objc.RegisterName("addGlobalMonitorForEventsMatchingMask:handler:"), uint64(mouseDownMask), click)
	center := objc.ID(objc.GetClass("NSNotificationCenter")).Send(objc.RegisterName("defaultCenter"))
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	deactivate := objc.NewBlock(func(_ objc.Block, _ objc.ID) { dismiss() })
	token := center.Send(objc.RegisterName("addObserverForName:object:queue:usingBlock:"),
		nsStr("NSApplicationDidResignActiveNotification"), app, objc.ID(0), deactivate)
	return func() {
		events.Send(objc.RegisterName("removeMonitor:"), monitor)
		center.Send(objc.RegisterName("removeObserver:"), token)
		click.Release()
		deactivate.Release()
	}
}

func configurePanelPopover(popover objc.ID, opaque bool) {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	popover.Send(objc.RegisterName("setAppearance:"), app.Send(objc.RegisterName("effectiveAppearance")))
	if opaque && panelOpacitySupported() {
		popover.Send(objc.RegisterName("setHasFullSizeContent:"), true)
	}
}

func (a *App) matchPanelAppearance() {
	if a.panelPopover.popover == 0 {
		return
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	a.panelPopover.popover.Send(objc.RegisterName("setAppearance:"), app.Send(objc.RegisterName("effectiveAppearance")))
}

// setPanelAnimates turns the popover's animations on or off: off while
// the user drags its height, which an animated resize would trail.
func setPanelAnimates(a *App, on bool) {
	if a.panelPopover.popover != 0 {
		a.panelPopover.popover.Send(objc.RegisterName("setAnimates:"), on)
	}
}

func panelContentInsets(a *App) panelInsets {
	if !a.Svc.Settings().PanelOpaque || !panelOpacitySupported() {
		return panelInsets{}
	}
	if a.panelPopover.view == 0 {
		return a.panelSafeArea
	}
	in := objc.Send[nativeEdgeInsets](a.panelPopover.view, objc.RegisterName("safeAreaInsets"))
	a.panelSafeArea = panelInsets{top: float32(in.Top), right: float32(in.Right), bottom: float32(in.Bottom), left: float32(in.Left)}
	return a.panelSafeArea
}

func releasePanelPopover(a *App) {
	if a.panelPopover.stopDismissal != nil {
		a.panelPopover.stopDismissal()
	}
	if a.panelPopover.popover != 0 {
		a.panelPopover.popover.Send(objc.RegisterName("release"))
	}
	a.panelPopover = panelPopoverState{}
	a.panelSafeArea = panelInsets{}
}
