//go:build darwin

package view

import (
	"fmt"
	"structs"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

type trayPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type traySize struct {
	_             structs.HostLayout
	Width, Height float64
}

type trayRect struct {
	_      structs.HostLayout
	Origin trayPoint
	Size   traySize
}

// trayOverlayTag finds the view of the quota colors among the button's.
const trayOverlayTag int64 = 0x43504142 // "CPAB"

// setTrayColorOverlay lays the quota colors, and the battery's clipped
// white labels, over the button's template image, in a view that lets
// clicks through to the button.
func setTrayColorOverlay(button objc.ID, png []byte) error {
	class, err := trayColorOverlayClass()
	if err != nil {
		return err
	}
	image, err := trayNativeImage(png)
	if err != nil {
		return err
	}
	defer image.Send(objc.RegisterName("release"))
	image.Send(objc.RegisterName("setSize:"), objc.Send[traySize](button.Send(objc.RegisterName("image")), objc.RegisterName("size")))
	view := button.Send(objc.RegisterName("viewWithTag:"), trayOverlayTag)
	if view == 0 {
		view = objc.ID(class).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithFrame:"), trayRect{})
		defer view.Send(objc.RegisterName("release"))
		view.Send(objc.RegisterName("setTag:"), trayOverlayTag)
		view.Send(objc.RegisterName("setAutoresizingMask:"), uint64(2|16)) // width and height sizable
		view.Send(objc.RegisterName("setImageScaling:"), uint64(2))        // NSImageScaleNone
		view.Send(objc.RegisterName("setAccessibilityElement:"), false)
		button.Send(objc.RegisterName("addSubview:"), view)
	}
	view.Send(objc.RegisterName("setImage:"), image)
	resizeTrayColorOverlay(view)
	view.Send(objc.RegisterName("setNeedsDisplay:"), true)
	return nil
}

// trayColorOverlayClass is an image view that is never hit and follows
// the image of the button it is in.
func trayColorOverlayClass() (objc.Class, error) {
	const name = "CPABarTrayColorOverlay"
	if class := objc.GetClass(name); class != 0 {
		return class, nil
	}
	return objc.RegisterClass(name, objc.GetClass("NSImageView"), nil, nil, []objc.MethodDef{
		{Cmd: objc.RegisterName("hitTest:"), Fn: func(objc.ID, objc.SEL, trayPoint) objc.ID { return 0 }},
		{Cmd: objc.RegisterName("resizeWithOldSuperviewSize:"), Fn: func(view objc.ID, _ objc.SEL, _ traySize) { resizeTrayColorOverlay(view) }},
	})
}

func resizeTrayColorOverlay(view objc.ID) {
	button := view.Send(objc.RegisterName("superview"))
	bounds := objc.Send[trayRect](button, objc.RegisterName("bounds"))
	cell := button.Send(objc.RegisterName("cell"))
	rect := objc.Send[trayRect](cell, objc.RegisterName("imageRectForBounds:"), bounds)
	view.Send(objc.RegisterName("setFrame:"), rect)
}

// trayNativeImage is a retained NSImage of png.
func trayNativeImage(png []byte) (objc.ID, error) {
	if len(png) == 0 {
		return 0, fmt.Errorf("tray image is empty")
	}
	data := objc.ID(objc.GetClass("NSData")).Send(objc.RegisterName("dataWithBytes:length:"), unsafe.Pointer(&png[0]), uint64(len(png)))
	image := objc.ID(objc.GetClass("NSImage")).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithData:"), data)
	if image == 0 {
		return 0, fmt.Errorf("tray image cannot be decoded")
	}
	return image, nil
}
