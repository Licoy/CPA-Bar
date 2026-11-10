//go:build darwin

package view

import (
	"fmt"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// setTrayContent shows r instead of the logo. mygo's tray images are 18
// points tall; the content is drawn for a 22 point status item, which
// leaves room for two rows. The ink is the button's template image, which
// the menu bar tints and highlights itself; the quota colors lie over it
// in a view of their own.
func setTrayContent(tray *mygo.Tray, r trayRender) error {
	foreground, err := renderTrayImage(r, trayImageOptions{light: true, foreground: true})
	if err != nil {
		return err
	}
	colors, err := renderTrayImage(r, trayImageOptions{colorsOnly: true})
	if err != nil {
		return err
	}
	var applied error
	mygo.RunOnMain(func() {
		button := trayButton()
		if button == 0 {
			applied = fmt.Errorf("status item button unavailable")
			return
		}
		pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("new"))
		defer pool.Send(objc.RegisterName("drain"))
		image, err := trayNativeImage(foreground)
		if err != nil {
			applied = err
			return
		}
		defer image.Send(objc.RegisterName("release"))
		s := objc.Send[traySize](image, objc.RegisterName("size"))
		if s.Height <= 0 {
			applied = fmt.Errorf("status item image has no height")
			return
		}
		tray.SetTitle("")
		image.Send(objc.RegisterName("setSize:"), traySize{Width: s.Width * trayContentHeight / s.Height, Height: trayContentHeight})
		image.Send(objc.RegisterName("setTemplate:"), true)
		// The button sizes its highlight and hit area as its image is
		// assigned, so the image has its size first.
		button.Send(objc.RegisterName("setImage:"), image)
		applied = setTrayColorOverlay(button, colors)
	})
	return applied
}

// clearTrayContent takes the quota colors off the button, before the logo
// takes the content's place.
func clearTrayContent() {
	mygo.RunOnMain(func() {
		if button := trayButton(); button != 0 {
			if view := button.Send(objc.RegisterName("viewWithTag:"), trayOverlayTag); view != 0 {
				view.Send(objc.RegisterName("removeFromSuperview"))
			}
		}
	})
}

// trayButton is the button of the app's menu bar item: the content of a
// status bar window, which mygo has send mygoTrayClicked:. 0 when none.
func trayButton() objc.ID {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	windows := app.Send(objc.RegisterName("windows"))
	action := objc.RegisterName("mygoTrayClicked:")
	n := objc.Send[uint](windows, objc.RegisterName("count"))
	for i := range n {
		w := windows.Send(objc.RegisterName("objectAtIndex:"), i)
		if b := findTrayButton(w.Send(objc.RegisterName("contentView")), action); b != 0 {
			return b
		}
	}
	return 0
}

// findTrayButton looks for the button with action in view and its
// subviews.
func findTrayButton(view objc.ID, action objc.SEL) objc.ID {
	if view == 0 {
		return 0
	}
	if objc.Send[bool](view, objc.RegisterName("respondsToSelector:"), objc.RegisterName("action")) &&
		objc.Send[objc.SEL](view, objc.RegisterName("action")) == action {
		return view
	}
	subviews := view.Send(objc.RegisterName("subviews"))
	n := objc.Send[uint](subviews, objc.RegisterName("count"))
	for i := range n {
		if b := findTrayButton(subviews.Send(objc.RegisterName("objectAtIndex:"), i), action); b != 0 {
			return b
		}
	}
	return 0
}

func nsStr(s string) objc.ID {
	str := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("alloc")).
		Send(objc.RegisterName("initWithUTF8String:"), s)
	return str.Send(objc.RegisterName("autorelease"))
}
