//go:build darwin

package view

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

func TestPanelPopoverObserverConfiguresPublicProperties(t *testing.T) {
	if !panelOpacitySupported() {
		t.Skip("hasFullSizeContent requires macOS 14")
	}
	a := &App{}
	stop := observePanelPopover(a, true)
	popover := objc.ID(objc.GetClass("NSPopover")).Send(objc.RegisterName("new"))
	defer popover.Send(objc.RegisterName("release"))
	center := objc.ID(objc.GetClass("NSNotificationCenter")).Send(objc.RegisterName("defaultCenter"))
	center.Send(objc.RegisterName("postNotificationName:object:"), popoverWillShowNotification, popover)
	stop()
	defer releasePanelPopover(a)
	other := objc.ID(objc.GetClass("NSPopover")).Send(objc.RegisterName("new"))
	defer other.Send(objc.RegisterName("release"))
	center.Send(objc.RegisterName("postNotificationName:object:"), popoverWillShowNotification, other)

	if a.panelPopover.popover != popover {
		t.Fatal("observer did not retain only the scoped popover")
	}
	if !objc.Send[bool](popover, objc.RegisterName("hasFullSizeContent")) {
		t.Error("popover content does not extend into its arrow")
	}
	if objc.Send[bool](other, objc.RegisterName("hasFullSizeContent")) {
		t.Error("removed observer changed another popover")
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	if got, want := popover.Send(objc.RegisterName("appearance")), app.Send(objc.RegisterName("effectiveAppearance")); got != want {
		t.Errorf("popover appearance = %v, want app appearance %v", got, want)
	}
}

func TestPanelPopoverAppearanceFollowsApp(t *testing.T) {
	popover := objc.ID(objc.GetClass("NSPopover")).Send(objc.RegisterName("new"))
	defer popover.Send(objc.RegisterName("release"))
	popover.Send(objc.RegisterName("retain"))
	a := &App{panelPopover: panelPopoverState{popover: popover}}
	defer releasePanelPopover(a)

	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	original := app.Send(objc.RegisterName("appearance"))
	if original != 0 {
		original.Send(objc.RegisterName("retain"))
		defer original.Send(objc.RegisterName("release"))
	}
	defer app.Send(objc.RegisterName("setAppearance:"), original)

	for _, name := range []string{"NSAppearanceNameAqua", "NSAppearanceNameDarkAqua"} {
		nsName := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), name)
		appearance := objc.ID(objc.GetClass("NSAppearance")).Send(objc.RegisterName("appearanceNamed:"), nsName)
		if appearance == 0 {
			t.Fatalf("NSAppearance %s is unavailable", name)
		}
		app.Send(objc.RegisterName("setAppearance:"), appearance)
		a.matchPanelAppearance()
		if got, want := popover.Send(objc.RegisterName("appearance")), app.Send(objc.RegisterName("effectiveAppearance")); got != want {
			t.Errorf("%s: popover appearance = %v, want %v", name, got, want)
		}
	}
}

func TestPanelDismissalOnDeactivateAndCleanup(t *testing.T) {
	closed := 0
	a := &App{}
	a.panelPopover.stopDismissal = observePanelDismissal(nil, func() { closed++ })
	t.Cleanup(func() { releasePanelPopover(a) })
	center := objc.ID(objc.GetClass("NSNotificationCenter")).Send(objc.RegisterName("defaultCenter"))
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	post := func() {
		center.Send(objc.RegisterName("postNotificationName:object:"), nsStr("NSApplicationDidResignActiveNotification"), app)
	}
	post()
	if closed != 1 {
		t.Errorf("app deactivation requested %d closes, want 1", closed)
	}
	releasePanelPopover(a)
	post()
	if closed != 1 {
		t.Errorf("removed observer requested another close: %d", closed)
	}
}
