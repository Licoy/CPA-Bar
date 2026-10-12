package view

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
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

// The bars are 18 points tall, one an account, filled in a color by how
// much is left; an unknown quota is an empty track.
func TestBarsIcon(t *testing.T) {
	img := decodeGauge(t, barsIcon([]trayGauge{{known: true, left: 84}, {known: true, left: 15}}))
	if img.Bounds().Dy() != 36 || img.Bounds().Dx() != 72 {
		t.Errorf("bars icon %v", img.Bounds())
	}
	green, red := count(img, 0x30, 0xD1, 0x58), count(img, 0xFF, 0x45, 0x3A)
	if green == 0 || red == 0 || green < red*4 {
		t.Errorf("green %d, red %d", green, red)
	}
	one := decodeGauge(t, barsIcon([]trayGauge{{known: true, left: 40}, {}}))
	if count(one, 0xFF, 0x9F, 0x0A) == 0 || count(one, 0x30, 0xD1, 0x58) != 0 || count(one, 0xFF, 0x45, 0x3A) != 0 {
		t.Error("an amber bar and an empty track have other colors")
	}
	// The panel's tiers: green from 70%, amber from 30%, red below.
	for p, want := range map[int]color.NRGBA{100: gaugeGreen, 70: gaugeGreen, 69: gaugeAmber, 30: gaugeAmber, 29: gaugeRed, 0: gaugeRed} {
		if got := levelColor(p); got != want {
			t.Errorf("%d%% is %v, want %v", p, got, want)
		}
	}
}

// A row shows the short limit, else the long one, without the word for
// limit, and when it refills: the time within a day, else the date.
func TestTrayRow(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	cr := cpa.Credential{Name: "a.json", Provider: "codex", Email: "a@example.com"}
	short := &quota.Window{Label: quota.Text{ZH: "5 小时限额", EN: "5-hour limit"}, Remaining: 0.9, Known: true, ResetAt: now.Add(3 * time.Hour)}
	long := &quota.Window{Label: quota.Text{ZH: "周限额", EN: "Weekly limit"}, Remaining: 0.84, Known: true, ResetAt: time.Date(2026, 10, 18, 8, 0, 0, 0, time.Local)}
	if r := newTrayRow(cr, short, long, now); r.text != "Codex · 5 小时 90% · 12:00" || r.gauge != (trayGauge{known: true, left: 90}) || r.tip != "a@example.com  5 小时限额 90% · 周限额 84%" {
		t.Errorf("row %+v", r)
	}
	if r := newTrayRow(cr, nil, long, now); r.text != "Codex · 每周 84% · 10/18" || r.gauge != (trayGauge{known: true, left: 84}) {
		t.Errorf("weekly-only row %+v", r)
	}
	if r := newTrayRow(cpa.Credential{Provider: "xai"}, nil, nil, now); r.text != "Grok · 额度未知" || r.gauge.known {
		t.Errorf("unknown row %+v", r)
	}
}

// The menu bar shows the default instance's enabled accounts that have a
// quota, pinned ones first, unknown until read.
func TestTrayRows(t *testing.T) {
	a, _, _, q := quotaApp(t)
	rows := a.trayRows(trayMaxBars, time.Now())
	if len(rows) != 1 || rows[0].gauge.known {
		t.Fatalf("rows before the read: %+v", rows)
	}
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	waitFor(t, tt, q, "the tray quota", func() bool {
		rows = a.trayRows(trayMaxBars, time.Now())
		return len(rows) == 1 && rows[0].gauge.known
	})
	if rows[0].gauge.left != 95 || !strings.HasPrefix(rows[0].text, "Claude · 5 小时 95% · ") {
		t.Errorf("row %+v", rows[0])
	}
	if rows[0].tip != "pro@example.com  5 小时限额 95% · 7 天限额 67%" {
		t.Errorf("tooltip line %q", rows[0].tip)
	}

	limit := trayGaugeLimit(store.TrayStyleBars)
	if runtime.GOOS == "darwin" && limit != trayMaxBars {
		t.Errorf("bars limit = %d", limit)
	}
	if runtime.GOOS != "darwin" && limit != 0 {
		t.Errorf("a quota style off macOS")
	}
	if trayGaugeLimit(store.TrayStyleIcon) != 0 {
		t.Error("the icon style shows accounts")
	}
}

// Pinned accounts come first; disabled ones and those without a quota are
// skipped.
func TestTrayAccounts(t *testing.T) {
	s := core.InstanceSnapshot{Credentials: []cpa.Credential{
		{Name: "a.json", AuthIndex: "a", Provider: "codex"},
		{Name: "b.json", AuthIndex: "b", Provider: "codex", Disabled: true},
		{Name: "c.json", AuthIndex: "c", Provider: "openai-compatibility"},
		{Name: "d.json", AuthIndex: "d", Provider: "claude"},
		{Name: "e.json", AuthIndex: "e", Provider: "codex"},
	}}
	s.Profile.PinnedAccounts = []string{"e.json"}
	var names []string
	for _, cr := range trayAccounts(s, trayMaxBars) {
		names = append(names, cr.Name)
	}
	if strings.Join(names, ",") != "e.json,a.json" {
		t.Errorf("accounts %q", names)
	}
}

// The settings offer the icon and the bars on macOS: the instances switch
// with the icon, the accounts shown with the bars.
func TestSettingsTrayStyle(t *testing.T) {
	if !mygoTrayTitleSupported() {
		t.Skip("the menu bar styles are for macOS")
	}
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("显示在线实例数") || tt.HasText("电池") || tt.HasText("显示的账号") {
		t.Fatalf("icon style texts: %q", tt.Texts())
	}
	if err := tt.Click("进度条"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleBars || tt.HasText("显示在线实例数") || !tt.HasText("显示的账号") {
		t.Errorf("bars style %q, texts %q", a.Svc.Settings().TrayStyle, tt.Texts())
	}
	if err := tt.Click("图标"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleIcon {
		t.Errorf("icon style %q", a.Svc.Settings().TrayStyle)
	}
}
