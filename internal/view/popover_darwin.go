//go:build darwin

package view

import (
	"structs"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type panelPopoverState struct {
	popover objc.ID
	view    objc.ID
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
		if a.panelPopover.popover != 0 {
			a.panelPopover.popover.Send(objc.RegisterName("release"))
		}
		a.panelPopover.popover = popover
		controller := popover.Send(objc.RegisterName("contentViewController"))
		a.panelPopover.view = controller.Send(objc.RegisterName("view"))
	})
	token := center.Send(objc.RegisterName("addObserverForName:object:queue:usingBlock:"), popoverWillShowNotification, objc.ID(0), objc.ID(0), block)
	return func() {
		center.Send(objc.RegisterName("removeObserver:"), token)
		block.Release()
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
	if a.panelPopover.popover != 0 {
		a.panelPopover.popover.Send(objc.RegisterName("release"))
	}
	a.panelPopover = panelPopoverState{}
	a.panelSafeArea = panelInsets{}
}
