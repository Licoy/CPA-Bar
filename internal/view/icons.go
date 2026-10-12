package view

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"runtime"
)

// The tray icons and the app icon, rendered from the logo SVGs in
// resources/ by scripts/gen-icons.sh.
var (
	//go:embed assets/tray-color-36.png
	trayColor36 []byte
	//go:embed assets/tray-mono-36.png
	trayMono36 []byte
	//go:embed assets/tray-color-32.png
	trayColor32 []byte
	//go:embed assets/tray-mono-32.png
	trayMono32 []byte
	// appIconPNG is the app icon, for windows and the Dock of a build that
	// is not an app bundle.
	//go:embed assets/app-512.png
	appIconPNG []byte
)

type trayHealth int

const (
	trayHealthy trayHealth = iota
	trayProblem
	trayIdle
)

// trayLook is how the tray icon is drawn.
type trayLook struct {
	health trayHealth
	// mono draws the logo in one color: on macOS a template, which the
	// menu bar tints; elsewhere white on a dark taskbar or panel, else
	// black.
	mono, light bool
	// n, when not 0, draws the first n gauges as bars instead of the logo.
	n      int
	gauges [trayMaxBars]trayGauge
}

// trayIcon draws the tray icon: the logo, 18 points tall on macOS and
// 32 pixels elsewhere. Without instances it is faded; a problem adds a dot
// at the corner, red in color and in the logo's color when mono, with a
// gap around it so that it shows over the logo.
func trayIcon(l trayLook) (data []byte, template bool) {
	if l.n > 0 {
		return barsIcon(l.gauges[:l.n]), false
	}
	src := trayColor32
	switch {
	case runtime.GOOS == "darwin" && l.mono:
		src = trayMono36
	case runtime.GOOS == "darwin":
		src = trayColor36
	case l.mono:
		src = trayMono32
	}
	dec, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return src, false
	}
	b := dec.Bounds()
	img := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			img.Set(x, y, dec.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	// Mono is black; white where the bar under it is dark.
	ink := color.NRGBA{A: 255}
	if l.mono && runtime.GOOS != "darwin" && !l.light {
		ink = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	for i := 0; i < len(img.Pix); i += 4 {
		a := img.Pix[i+3]
		if l.mono {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = ink.R, ink.G, ink.B
		}
		if l.health == trayIdle {
			a = uint8(int(a) * 110 / 255)
		}
		img.Pix[i+3] = a
	}
	if l.health == trayProblem {
		dot := color.NRGBA{R: 220, G: 38, B: 38, A: 255}
		if l.mono {
			dot = ink
		}
		badge(img, dot)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return src, false
	}
	return out.Bytes(), runtime.GOOS == "darwin" && l.mono
}

// badge draws a dot at the bottom right of img, clearing a ring around it.
func badge(img *image.NRGBA, dot color.NRGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	r := float64(w) * 0.14
	cx, cy := float64(w)-r-0.5, float64(h)-r-0.5
	gap := r + float64(w)*0.06
	for y := range h {
		for x := range w {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := dx*dx + dy*dy
			switch {
			case d <= r*r:
				img.SetNRGBA(x, y, dot)
			case d <= gap*gap:
				img.SetNRGBA(x, y, color.NRGBA{})
			}
		}
	}
}
