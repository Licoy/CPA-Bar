package view

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
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

func trayTestAccounts(now time.Time) []trayAccount {
	short := &quota.Window{Label: quota.Text{ZH: "5 小时限额", EN: "5-hour limit"}, Remaining: 0.9, Known: true, ResetAt: now.Add(3 * time.Hour)}
	long := &quota.Window{Label: quota.Text{ZH: "周限额", EN: "Weekly limit"}, Remaining: 0.25, Known: true, ResetAt: now.Add(30 * time.Hour)}
	return []trayAccount{{provider: "codex", short: short, long: long}, {provider: "claude", long: long}}
}

func trayTestSettings(style string, fn func(*store.Settings)) store.Settings {
	s := store.Defaults()
	s.TrayStyle = style
	if fn != nil {
		fn(&s)
	}
	return s
}

// An account alone shows both main limits, named; stacked accounts show
// their short limit, else the long one, under one component.
func TestBuildTray(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	r := buildTray(trayTestSettings(store.TrayStyleMeters, nil), trayTestAccounts(now), now)
	if len(r.components) != 2 || r.components[0].provider != "codex" || len(r.components[0].cells) != 2 || len(r.components[1].cells) != 1 {
		t.Fatalf("components %+v", r.components)
	}
	c := r.components[0].cells[0]
	if c.label != "5小时" || c.percent != "90%" || c.meter != "90%" || c.countdown != "3小时" || c.ring != "90" || c.tier != 0 {
		t.Errorf("short cell %+v", c)
	}
	if c := r.components[0].cells[1]; c.label != "每周" || c.countdown != "1天6小时" || c.tier != 2 {
		t.Errorf("long cell %+v", c)
	}
	if c := r.components[1].cells[0]; c.label != "" || c.percent != "25%" {
		t.Errorf("long-only cell %+v", c)
	}

	stacked := buildTray(trayTestSettings(store.TrayStyleBattery, func(s *store.Settings) { s.TrayStack = true }), trayTestAccounts(now), now)
	if len(stacked.components) != 1 || stacked.components[0].provider != "" || len(stacked.components[0].cells) != 2 ||
		stacked.components[0].cells[0].percent != "90%" || stacked.components[0].cells[1].provider != "claude" {
		t.Errorf("stacked %+v", stacked.components)
	}
	// Rings show every account on its own.
	if r := buildTray(trayTestSettings(store.TrayStyleRings, func(s *store.Settings) { s.TrayStack = true }), trayTestAccounts(now), now); len(r.components) != 2 {
		t.Errorf("stacked rings %+v", r.components)
	}

	long := buildTray(trayTestSettings(store.TrayStyleMeters, func(s *store.Settings) {
		s.TrayWindow, s.TrayMetersDetail = store.TrayWindowLong, store.TrayDetailBoth
	}), trayTestAccounts(now), now)
	if c := long.components[0].cells; len(c) != 1 || c[0].meter != "25% · 10/13" {
		t.Errorf("long window %+v", c)
	}
	battery := buildTray(trayTestSettings(store.TrayStyleBattery, func(s *store.Settings) {
		s.TrayWindow, s.TrayBatteryDetail = store.TrayWindowShort, store.TrayBatteryPercent
	}), trayTestAccounts(now), now)
	if c := battery.components[1].cells; len(c) != 1 || c[0].battery != "25%" {
		t.Errorf("an account without the short limit shows %+v", c)
	}

	unknown := buildTray(trayTestSettings(store.TrayStyleMeters, nil), []trayAccount{{provider: "kimi"}, {provider: "xai", long: &quota.Window{Detail: "$12.50"}}}, now)
	if c := unknown.components[0].cells[0]; !c.empty || c.percent != "--" || c.ring != "--" {
		t.Errorf("unknown cell %+v", c)
	}
	if c := unknown.components[1].cells[0]; !c.empty || c.percent != "$12.50" || c.ring != "" {
		t.Errorf("balance cell %+v", c)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                    "0秒",
		45 * time.Second:                     "45秒",
		72 * time.Minute:                     "1小时12分",
		3 * time.Hour:                        "3小时",
		30*time.Hour + 20*time.Minute:        "1天6小时",
		16*24*time.Hour + 30*time.Second:     "16天",
		2*time.Minute + 500*time.Millisecond: "2分",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("%v is %q, want %q", d, got, want)
		}
	}
}

// Every style draws, wider with more components, and the colors land only
// in the color layer.
func TestRenderTray(t *testing.T) {
	now := time.Now()
	for _, style := range store.TrayStyles[1:] {
		one := buildTray(trayTestSettings(style, nil), trayTestAccounts(now)[:1], now)
		two := buildTray(trayTestSettings(style, nil), trayTestAccounts(now), now)
		if trayContentWidth(two) <= trayContentWidth(one) {
			t.Errorf("%s: two accounts are no wider than one", style)
		}
		ink := decodeTrayContent(t, one, trayImageOptions{light: true, foreground: true})
		colors := decodeTrayContent(t, one, trayImageOptions{colorsOnly: true})
		if ink.Bounds().Dy() != trayContentHeight*2 {
			t.Errorf("%s: %v tall", style, ink.Bounds())
		}
		if n := countColored(ink); n != 0 {
			t.Errorf("%s: the ink layer has %d colored pixels", style, n)
		}
		if style != store.TrayStyleText && style != store.TrayStyleCountdown && countColored(colors) == 0 {
			t.Errorf("%s: no quota colors", style)
		}
	}
	if _, err := renderTrayImage(trayRender{style: store.TrayStyleIcon}, trayImageOptions{}); err == nil {
		t.Error("the icon style renders as content")
	}
}

func decodeTrayContent(t *testing.T, r trayRender, o trayImageOptions) image.Image {
	t.Helper()
	data, err := renderTrayImage(r, o)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// countColored counts the pixels that are neither gray nor transparent.
func countColored(img image.Image) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A > 128 && (max(c.R, c.G, c.B)-min(c.R, c.G, c.B)) > 60 {
				n++
			}
		}
	}
	return n
}

// TestTrayPreview writes a sheet of every style to CPABAR_SNAPSHOTS, on a
// light and a dark menu bar.
func TestTrayPreview(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("set CPABAR_SNAPSHOTS to export the tray previews")
	}
	now := time.Now()
	type variant struct {
		style string
		fn    func(*store.Settings)
	}
	stack := func(s *store.Settings) { s.TrayStack = true }
	var variants []variant
	for _, style := range store.TrayStyles[1:] {
		variants = append(variants, variant{style, nil}, variant{style, func(s *store.Settings) { s.TrayWindow = store.TrayWindowShort }})
		if store.TrayStyleStacks(style) {
			variants = append(variants, variant{style, stack})
		}
	}
	variants = append(variants,
		variant{store.TrayStyleBattery, func(s *store.Settings) { s.TrayBatteryVertical = true }},
		variant{store.TrayStyleBattery, func(s *store.Settings) { s.TrayBatteryVertical = true; stack(s) }},
		variant{store.TrayStyleRings, func(s *store.Settings) { s.TrayRingValues = true }},
		variant{store.TrayStyleMeters, func(s *store.Settings) { s.TrayHideIcons = true }},
		variant{store.TrayStyleMeters, func(s *store.Settings) { s.TrayMetersDetail = store.TrayDetailBoth }})
	const rowH, width = 56, 900
	sheet := image.NewNRGBA(image.Rect(0, 0, width, rowH*len(variants)))
	for i, v := range variants {
		r := buildTray(trayTestSettings(v.style, v.fn), trayTestAccounts(now), now)
		for j, light := range []bool{true, false} {
			bg := color.NRGBA{236, 236, 236, 255}
			if !light {
				bg = color.NRGBA{30, 30, 30, 255}
			}
			at := image.Pt(j*width/2, i*rowH)
			draw.Draw(sheet, image.Rect(at.X, at.Y, at.X+width/2, at.Y+rowH), image.NewUniform(bg), image.Point{}, draw.Src)
			for _, o := range []trayImageOptions{{light: light, foreground: true}, {colorsOnly: true}} {
				img := decodeTrayContent(t, r, o)
				draw.Draw(sheet, img.Bounds().Add(at.Add(image.Pt(8, 6))), img, image.Point{}, draw.Over)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, sheet); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tray-styles.png"), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The menu bar shows the default instance's enabled accounts that have a
// quota, pinned ones first, unknown until read.
func TestTrayQuota(t *testing.T) {
	a, _, _, q := quotaApp(t)
	accts, tips := a.trayQuota(trayMaxAccounts, time.Now())
	if len(accts) != 1 || accts[0].short != nil || accts[0].long != nil {
		t.Fatalf("accounts before the read: %+v", accts)
	}
	if tips[0] != "pro@example.com  额度未知" {
		t.Errorf("tooltip line before the read %q", tips[0])
	}
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	waitFor(t, tt, q, "the tray quota", func() bool {
		accts, tips = a.trayQuota(trayMaxAccounts, time.Now())
		return len(accts) == 1 && accts[0].short != nil
	})
	if accts[0].provider != "claude" || percent(accts[0].short.Remaining) != 95 {
		t.Errorf("account %+v", accts[0])
	}
	if tips[0] != "pro@example.com  5 小时限额 95% · 7 天限额 67%" {
		t.Errorf("tooltip line %q", tips[0])
	}

	limit := trayAccountLimit(store.TrayStyleMeters)
	if runtime.GOOS == "darwin" && limit != trayMaxAccounts {
		t.Errorf("meters limit = %d", limit)
	}
	if runtime.GOOS != "darwin" && limit != 0 {
		t.Errorf("a quota style off macOS")
	}
	if trayAccountLimit(store.TrayStyleIcon) != 0 {
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
	for _, cr := range trayAccounts(s, trayMaxAccounts) {
		names = append(names, cr.Name)
	}
	if strings.Join(names, ",") != "e.json,a.json" {
		t.Errorf("accounts %q", names)
	}
}

// The settings offer the icon and the quota styles on macOS, each with its
// options.
func TestSettingsTrayStyle(t *testing.T) {
	if !mygoTrayTitleSupported() {
		t.Skip("the menu bar styles are for macOS")
	}
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("显示在线实例数") || tt.HasText("电池方向") || tt.HasText("显示的账号") {
		t.Fatalf("icon style texts: %q", tt.Texts())
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "settings-tray-icon.png"), tt)
	}
	for _, step := range []struct{ tile, style, option string }{
		{"额度条", store.TrayStyleMeters, "额度条右侧显示"},
		{"电池", store.TrayStyleBattery, "电池方向"},
		{"圆环进度", store.TrayStyleRings, "数值置于圆环内"},
		{"倒计时", store.TrayStyleCountdown, "合并显示两个账号"},
		{"进度文本", store.TrayStyleText, "显示服务商图标"},
	} {
		if err := tt.Click(step.tile); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.Svc.Settings().TrayStyle != step.style || tt.HasText("显示在线实例数") || !tt.HasText(step.option) || !tt.HasText("显示的账号") {
			t.Errorf("%s style %q, texts %q", step.tile, a.Svc.Settings().TrayStyle, tt.Texts())
		}
		if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
			saveSnapshot(t, filepath.Join(dir, "settings-tray-"+step.style+".png"), tt)
		}
	}
	if tt.HasText("合并显示两个账号") {
		t.Error("the text style offers to stack")
	}
	if err := tt.Click("图标"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().TrayStyle != store.TrayStyleIcon {
		t.Errorf("icon style %q", a.Svc.Settings().TrayStyle)
	}
}
