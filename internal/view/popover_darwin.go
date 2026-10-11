package view

import (
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// matchAppAppearance gives the panel's popover the app's appearance. AppKit
// draws a popover from the menu bar in the menu bar's appearance, which
// follows the wallpaper, not the app's light or dark setting: the material
// and the arrow would not match the content. Main thread only.
func matchAppAppearance(w *mygo.Window) {
	if w == nil {
		return
	}
	win := objc.ID(w.NativeHandle())
	if win == 0 {
		return
	}
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	appearance := app.Send(objc.RegisterName("effectiveAppearance"))
	win.Send(objc.RegisterName("setAppearance:"), appearance)
}
