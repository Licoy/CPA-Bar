package view

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

func decodeGauge(t *testing.T, data []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	n, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("the icon is a %T", img)
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, t.Name()+".png"), data, 0o644)
	}
	return n
}

// count counts the opaque pixels of a color.
func count(img *image.NRGBA, r, g, b uint8) int {
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if p := img.Pix[i : i+4]; p[3] == 255 && p[0] == r && p[1] == g && p[2] == b {
			n++
		}
	}
	return n
}

// Batteries are 18 points tall, one an account, filled to the lower limit
// in a color by how much is left; mono ones are a single color.
func TestBatteryIcon(t *testing.T) {
	gs := []trayGauge{
		{known: true, short: 90, long: 80},
		{known: true, short: -1, long: 35},
		{known: true, short: 5, long: 70},
		{known: false, short: -1, long: -1},
	}
	img := decodeGauge(t, batteryIcon(gs, false))
	if img.Bounds().Dy() != 36 || img.Bounds().Dx() != int((4*7.5+3*3)*2) {
		t.Errorf("battery icon %v", img.Bounds())
	}
	if count(img, 52, 199, 89) < 20 || count(img, 255, 159, 10) < 20 || count(img, 255, 59, 48) < 2 {
		t.Error("the batteries lack green, orange or red charge")
	}
	// The battery at 80% holds more than the one at 35%.
	column := func(x float64) int {
		n := 0
		for y := range 36 {
			if p := img.Pix[img.PixOffset(int(x*2), y):]; p[3] == 255 && p[0] != 142 {
				n++
			}
		}
		return n
	}
	if full, mid, unknown := column(3.75), column(3.75+10.5), column(3.75+3*10.5); full <= mid || unknown != 0 {
		t.Errorf("charge columns: %d, %d, unknown %d", full, mid, unknown)
	}

	mono := decodeGauge(t, batteryIcon(gs, true))
	for i := 0; i < len(mono.Pix); i += 4 {
		if mono.Pix[i] != 0 || mono.Pix[i+1] != 0 || mono.Pix[i+2] != 0 {
			t.Fatal("the mono batteries are not black")
		}
	}
}

// Bars show the short limit in blue over the long one in purple; an
// account without a short limit has the long bar alone.
func TestBarsIcon(t *testing.T) {
	both := decodeGauge(t, barsIcon([]trayGauge{{known: true, short: 60, long: 30}, {known: true, short: -1, long: 90}}, false))
	if both.Bounds().Dy() != 36 || both.Bounds().Dx() != 60 {
		t.Errorf("bars icon %v", both.Bounds())
	}
	blue, purple := count(both, 10, 132, 255), count(both, 175, 82, 222)
	if blue == 0 || purple == 0 {
		t.Fatalf("blue %d, purple %d", blue, purple)
	}
	// 60% of one bar against 30% of one and 90% of another.
	if purple < blue {
		t.Errorf("blue %d, purple %d", blue, purple)
	}
	weekOnly := decodeGauge(t, barsIcon([]trayGauge{{known: true, short: -1, long: 50}}, false))
	if count(weekOnly, 10, 132, 255) != 0 || count(weekOnly, 175, 82, 222) == 0 {
		t.Error("a weekly-only account is not a purple bar alone")
	}
}

// The batteries show the default instance's enabled accounts that have a
// quota, unknown until read; the menu bar text drops "CPA".
func TestTrayGauges(t *testing.T) {
	a, _, _, q := quotaApp(t)
	gs, lines := a.trayGauges(trayMaxBatteries, time.Now())
	if len(gs) != 1 || gs[0].known || len(lines) != 1 {
		t.Fatalf("gauges before the read: %+v %q", gs, lines)
	}
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	waitFor(t, tt, q, "the tray quota", func() bool {
		gs, _ = a.trayGauges(trayMaxBatteries, time.Now())
		return len(gs) == 1 && gs[0].known
	})
	if gs[0].short != 95 || gs[0].long != 67 || gs[0].level() != 67 {
		t.Errorf("gauge %+v", gs[0])
	}
	if _, lines = a.trayGauges(trayMaxBatteries, time.Now()); lines[0] != "pro@example.com  5 小时 95% · 周 67%" {
		t.Errorf("tooltip line %q", lines[0])
	}

	style, limit := trayGaugeStyle(store.TrayStyleBars)
	if runtime.GOOS == "darwin" && (style != store.TrayStyleBars || limit != trayMaxBars) {
		t.Errorf("bars style = %q, %d", style, limit)
	}
	if runtime.GOOS != "darwin" && limit != 0 {
		t.Errorf("a quota style off macOS")
	}
}

// The settings offer the three styles on macOS, and the text switch only
// with the icon.
func TestSettingsTrayStyle(t *testing.T) {
	if !mygoTrayTitleSupported() {
		t.Skip("the menu bar styles are for macOS")
	}
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("菜单栏文字") {
		t.Fatal("no text switch with the icon style")
	}
	if err := tt.Click("电池"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleBattery || tt.HasText("菜单栏文字") {
		t.Errorf("battery style %q, text switch shown %v", a.Svc.Settings().TrayStyle, tt.HasText("菜单栏文字"))
	}
	if err := tt.Click("进度条"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleBars {
		t.Errorf("bars style %q", a.Svc.Settings().TrayStyle)
	}
	if err := tt.Click("图标"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleIcon {
		t.Errorf("icon style %q", a.Svc.Settings().TrayStyle)
	}
}
