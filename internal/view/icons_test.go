package view

import (
	"bytes"
	"image"
	"image/png"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func decodeTray(t *testing.T, l trayLook) (*image.NRGBA, bool) {
	t.Helper()
	data, template := trayIcon(l)
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	n, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("the icon is a %T", img)
	}
	return n, template
}

// The color icon keeps the logo's colors; the mono one is a single color,
// a template on macOS; a problem puts a dot at the corner and no
// instances fade the logo.
func TestTrayIcon(t *testing.T) {
	size := 32
	if runtime.GOOS == "darwin" {
		size = 36
	}
	colored, template := decodeTray(t, trayLook{})
	if template || colored.Bounds().Dx() != size || colored.Bounds().Dy() != size {
		t.Errorf("color icon %v, template %v", colored.Bounds(), template)
	}
	primaryBlue := 0
	for i := 0; i < len(colored.Pix); i += 4 {
		if p := colored.Pix[i : i+4]; p[3] == 255 && p[0] == 0x0A && p[1] == 0x84 && p[2] == 0xFF {
			primaryBlue++
		}
	}
	if primaryBlue < 50 {
		t.Errorf("the color icon has %d #0A84FF pixels", primaryBlue)
	}

	mono, template := decodeTray(t, trayLook{mono: true})
	if template != (runtime.GOOS == "darwin") {
		t.Errorf("mono template = %v", template)
	}
	colors := map[[3]uint8]bool{}
	for i := 0; i < len(mono.Pix); i += 4 {
		if mono.Pix[i+3] > 0 {
			colors[[3]uint8(mono.Pix[i:i+3])] = true
		}
	}
	if len(colors) != 1 {
		t.Errorf("the mono icon has %d colors", len(colors))
	}

	corner := func(img *image.NRGBA) []uint8 {
		w := img.Bounds().Dx()
		x, y := w-w/6, w-w/6
		return img.Pix[img.PixOffset(x, y) : img.PixOffset(x, y)+4]
	}
	problem, _ := decodeTray(t, trayLook{health: trayProblem})
	if p := corner(problem); p[0] < 200 || p[1] > 80 || p[3] != 255 {
		t.Errorf("the problem dot is %v, want red", p)
	}
	if p, _ := decodeTray(t, trayLook{health: trayProblem, mono: true}); corner(p)[3] != 255 {
		t.Errorf("the mono problem dot is %v", corner(p))
	}

	idle, _ := decodeTray(t, trayLook{health: trayIdle})
	for i := 3; i < len(idle.Pix); i += 4 {
		if idle.Pix[i] > 120 {
			t.Fatalf("an idle pixel has alpha %d", idle.Pix[i])
		}
	}
}

// The tray icon is mono by default; the settings switch it to color and
// back.
func TestSettingsTrayIconStyle(t *testing.T) {
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if a.Svc.Settings().TrayIconColor || !a.trayLook(trayHealthy).mono {
		t.Fatal("the tray icon is not mono by default")
	}
	if err := tt.Click("彩色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.Svc.Settings().TrayIconColor || a.trayLook(trayHealthy).mono {
		t.Error("彩色 did not show the logo's colors")
	}
	if err := tt.Click("纯色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayIconColor || !a.trayLook(trayHealthy).mono {
		t.Error("纯色 did not make the tray icon mono again")
	}
}
