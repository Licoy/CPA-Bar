package view

import (
	_ "embed"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// The look of CPA-Bar follows Pulse (pulse.egoist.dev): a quiet sidebar of
// colored tiles, and pages of soft, borderless cards with large numbers,
// small labels and dot-matrix charts, in Apple's system colors.

// Tint is one of the system colors that tell the parts of the app apart.
type Tint int

const (
	TintBlue Tint = iota
	TintGreen
	TintRed
	TintOrange
	TintTeal
	TintYellow
	TintPurple
	TintIndigo
	TintPink
	TintGray
)

// tintHex holds each tint for light and dark appearances.
var tintHex = [...][2]string{
	TintBlue:   {"#007AFF", "#0A84FF"},
	TintGreen:  {"#28B14C", "#30D158"},
	TintRed:    {"#FF3B30", "#FF453A"},
	TintOrange: {"#F59100", "#FF9F0A"},
	TintTeal:   {"#14A3BE", "#40C8E0"},
	TintYellow: {"#E6B400", "#FFD60A"},
	TintPurple: {"#AF52DE", "#BF5AF2"},
	TintIndigo: {"#5856D6", "#5E5CE6"},
	TintPink:   {"#FF2D55", "#FF375F"},
	TintGray:   {"#8E8E93", "#98989D"},
}

var tintColors [len(tintHex)][2]ui.Color

func init() {
	for i, h := range tintHex {
		tintColors[i] = [2]ui.Color{ui.Hex(h[0]), ui.Hex(h[1])}
	}
}

// C is the tint in the theme's appearance.
func (k Tint) C(t *ui.Theme) ui.Color {
	if t.Dark {
		return tintColors[k][1]
	}
	return tintColors[k][0]
}

// Text is the tint for text, darker on light backgrounds to stay legible.
func (k Tint) Text(t *ui.Theme) ui.Color {
	if t.Dark {
		return tintColors[k][1]
	}
	return tintColors[k][0].Mix(ui.RGB(0, 0, 0), 0.12)
}

// palette holds the surfaces the theme has no names for.
type palette struct {
	// Sidebar fills the sidebar, Card the cards on the window's background,
	// Inset what sits on a card, Line hairlines, Hover a hovered row.
	Sidebar, Card, Inset, Line, Hover ui.Color
}

var palettes = [2]palette{
	{
		Sidebar: ui.Hex("#F5F5F7"),
		Card:    ui.Hex("#F4F4F6"),
		Inset:   ui.Hex("#FFFFFF"),
		Line:    ui.RGBA(0, 0, 0, 0.07),
		Hover:   ui.RGBA(0, 0, 0, 0.05),
	},
	{
		Sidebar: ui.Hex("#202022"),
		Card:    ui.Hex("#262628"),
		Inset:   ui.Hex("#303033"),
		Line:    ui.RGBA(255, 255, 255, 0.08),
		Hover:   ui.RGBA(255, 255, 255, 0.06),
	},
}

// materialPalettes replace palettes where a view shows over the macOS
// popover's material: cards are translucent washes on it, as on the system's
// own popovers, rather than opaque greys that would not match it.
var materialPalettes = [2]palette{
	{
		Sidebar: palettes[0].Sidebar,
		Card:    ui.RGBA(255, 255, 255, 0.62),
		Inset:   ui.RGBA(255, 255, 255, 0.9),
		Line:    palettes[0].Line,
		Hover:   palettes[0].Hover,
	},
	{
		Sidebar: palettes[1].Sidebar,
		Card:    ui.RGBA(255, 255, 255, 0.07),
		Inset:   ui.RGBA(255, 255, 255, 0.12),
		Line:    palettes[1].Line,
		Hover:   palettes[1].Hover,
	},
}

func pal(t *ui.Theme) palette {
	i := 0
	if t.Dark {
		i = 1
	}
	if t == materialThemes[i] {
		return materialPalettes[i]
	}
	return palettes[i]
}

var themes, materialThemes [2]*ui.Theme

// applyTheme makes the frame use CPA-Bar's theme in the system's (or the
// chosen) appearance. Views call it first.
func applyTheme(c *ui.Context) *ui.Theme {
	i := 0
	if c.Theme().Dark {
		i = 1
	}
	if themes[i] == nil {
		themes[i] = buildTheme(i == 1)
	}
	if c.Vibrancy() {
		// The macOS panel is an NSPopover: the system draws its material
		// and its arrow, and the arrow can only match what the panel shows
		// when the panel shows that same material instead of an opaque
		// background of its own (see FlyoutOptions.Popover). The material
		// follows the appearance, light or dark, as the arrow does.
		if materialThemes[i] == nil {
			t := *themes[i]
			t.Background = ui.Transparent
			materialThemes[i] = &t
		}
		c.SetTheme(materialThemes[i])
		c.Root().Background(ui.Transparent)
		return materialThemes[i]
	}
	c.SetTheme(themes[i])
	return themes[i]
}

func buildTheme(dark bool) *ui.Theme {
	if dark {
		t := ui.DarkTheme()
		t.Background = ui.Hex("#18181A")
		t.Surface = ui.Hex("#333336")
		t.SurfaceHover = ui.Hex("#3C3C3F")
		t.SurfacePressed = ui.Hex("#47474A")
		t.Border = ui.Hex("#3A3A3D")
		t.Text = ui.Hex("#F5F5F7")
		t.TextMuted = ui.Hex("#98989D")
		t.Accent = ui.Hex("#0A84FF")
		t.AccentHover = ui.Hex("#3395FF")
		t.AccentPressed = ui.Hex("#0071E3")
		t.Danger = ui.Hex("#FF453A")
		t.Warning = ui.Hex("#FF9F0A")
		t.Success = ui.Hex("#30D158")
		t.Selection = ui.RGBA(10, 132, 255, 0.4)
		t.Focus = ui.RGBA(10, 132, 255, 0.6)
		// Toasts: by default their text is the background, which the
		// material theme makes transparent.
		t.Inverse = ui.Hex("#F5F5F7")
		t.InverseText = ui.Hex("#1D1D1F")
		t.Radius = 8
		return t
	}
	t := ui.LightTheme()
	t.Background = ui.Hex("#FFFFFF")
	t.Surface = ui.Hex("#FFFFFF")
	t.SurfaceHover = ui.Hex("#F2F2F4")
	t.SurfacePressed = ui.Hex("#E8E8EB")
	t.Border = ui.Hex("#DCDCE0")
	t.Text = ui.Hex("#1D1D1F")
	t.TextMuted = ui.Hex("#86868B")
	t.Accent = ui.Hex("#007AFF")
	t.AccentHover = ui.Hex("#0070EB")
	t.AccentPressed = ui.Hex("#0062CC")
	t.Danger = ui.Hex("#E5342A")
	t.Warning = ui.Hex("#D97E00")
	t.Success = ui.Hex("#24A148")
	t.Selection = ui.RGBA(0, 122, 255, 0.25)
	t.Focus = ui.RGBA(0, 122, 255, 0.5)
	t.Inverse = ui.Hex("#1D1D1F")
	t.InverseText = ui.Hex("#FFFFFF")
	t.Radius = 8
	return t
}

// Tile is a rounded square holding an icon: filled with the tint and the
// icon white when solid, as in the sidebar, or tinted lightly with the
// icon in the tint, as on cards.
func Tile(c *ui.Context, icon *ui.SVG, k Tint, size float32, solid bool) ui.Element {
	t := c.Theme()
	col := k.C(t)
	bg, fg := col.Alpha(0.15), k.Text(t)
	if solid {
		bg, fg = col, ui.RGB(255, 255, 255)
	}
	return ui.Row(c).Size(size, size).Radius(size * 0.28).Background(bg).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon).Size(size*0.58, size*0.58).TextColor(fg)
	})
}

//go:embed assets/logo.svg
var logoSVG []byte

// appLogo is the CPA-Bar logo in its colors.
var appLogo = ui.MustParseSVG(logoSVG)

// AppLogo is the logo on a pale blue tile, as the app icon shows it.
func AppLogo(c *ui.Context, size float32) ui.Element {
	return ui.Row(c).Size(size, size).Radius(size * 0.24).Background(ui.RGB(232, 243, 255)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Image(c, appLogo).Size(size*0.74, size*0.74)
	})
}

// Panel is a large card of a dashboard.
func Panel(c *ui.Context) ui.Element {
	return ui.Column(c).Padding(18).Gap(14).Radius(18).Background(pal(c.Theme()).Card)
}

// PanelTitle heads a panel: a tinted tile, the title in the tint, muted
// text at the right, and a chevron when it leads somewhere. It reports a
// click when it leads somewhere.
func PanelTitle(c *ui.Context, icon *ui.SVG, k Tint, title, trailing string, link bool) bool {
	t := c.Theme()
	row := ui.Row(c)
	if link {
		row = ui.ButtonBase(c).FocusRing(false).Label(title).Padding(0).Background(ui.Color{})
	}
	row.Gap(10).AlignItems(ui.Center).Children(func() {
		Tile(c, icon, k, 30, false)
		ui.Text(c, title).FontSize(17).FontWeight(600).TextColor(k.Text(t)).SingleLine().Grow(1)
		if trailing != "" {
			ui.Text(c, trailing).FontSize(13).TextColor(t.TextMuted).SingleLine()
		}
		if link {
			col := t.TextMuted
			if row.Hovered() {
				col = t.Text
			}
			ui.Icon(c, icChevron).FontSize(14).TextColor(col)
		}
	})
	return link && row.Clicked()
}

// BigValue is a caption over a large number and its unit.
func BigValue(c *ui.Context, caption, value, unit string, col ui.Color) ui.Element {
	t := c.Theme()
	return ui.Column(c).Gap(2).Children(func() {
		if caption != "" {
			ui.Text(c, caption).FontSize(13).TextColor(t.TextMuted).SingleLine()
		}
		ui.Row(c).Gap(4).AlignItems(ui.End).Children(func() {
			ui.Text(c, value).FontSize(38).FontWeight(700).TextColor(col).FontFeatures("tnum").SingleLine()
			if unit != "" {
				ui.Text(c, unit).FontSize(17).FontWeight(600).TextColor(t.TextMuted).Padding(0, 0, 6, 0).SingleLine()
			}
		})
	})
}

// Metric is a labeled value of a Metrics row.
type Metric struct {
	Label, Value string
	// Color colors the value; zero is the text color.
	Color ui.Color
}

// Metrics lays out labeled values in equal columns.
func Metrics(c *ui.Context, ms ...Metric) {
	t := c.Theme()
	ui.Row(c).Gap(12).Children(func() {
		for _, m := range ms {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				ui.Text(c, m.Label).FontSize(12).TextColor(t.TextMuted).SingleLine()
				col := m.Color
				if col == (ui.Color{}) {
					col = t.Text
				}
				ui.Text(c, m.Value).FontSize(15).FontWeight(600).TextColor(col).FontFeatures("tnum").SingleLine()
			})
		}
	})
}

// Pill is a status in a tinted capsule.
func Pill(c *ui.Context, text string, col ui.Color) ui.Element {
	return ui.Row(c).Padding(3, 10).Radius(999).Background(col.Alpha(0.15)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(col).SingleLine()
	})
}

// PillButton is a capsule button with an icon, for the toolbars of
// headers and cards. A disabled one reports no clicks.
func PillButton(c *ui.Context, icon *ui.SVG, label string, disabled bool) ui.Element {
	return pillButton(c, icon, label, disabled, false)
}

// BusyPillButton is a PillButton for work that takes a while, as a
// refresh: while busy it is disabled and its icon turns, the refresh icon
// itself and any other a spinner, instead of a spinner beside it.
func BusyPillButton(c *ui.Context, icon *ui.SVG, label string, busy, disabled bool) ui.Element {
	return pillButton(c, icon, label, disabled || busy, busy)
}

func pillButton(c *ui.Context, icon *ui.SVG, label string, disabled, busy bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Disabled(disabled).Padding(6, 12).Gap(6).Radius(999).AlignItems(ui.Center)
	if label == "" {
		b.Padding(6, 8) // an icon alone: name it with Label
	}
	// Translucent, to show on the window and on cards alike.
	bg := t.Text.Alpha(0.06)
	switch {
	case disabled:
	case b.Pressed():
		bg = t.Text.Alpha(0.14)
	case b.Hovered():
		bg = t.Text.Alpha(0.10)
	}
	b.Background(bg)
	return b.Children(func() {
		col := t.Text
		if disabled {
			col = t.TextMuted
		}
		switch {
		case busy && icon == icRefresh:
			spin := ui.Icon(c, icon).FontSize(13).TextColor(t.Text)
			spin.Rotate(spin.Loop("spin", time.Second, ui.Linear) * 360)
		case busy:
			ui.Spinner(c).FontSize(13)
		case icon != nil:
			ui.Icon(c, icon).FontSize(13).TextColor(col)
		}
		if label != "" {
			ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(col).SingleLine()
		}
	})
}

// PillTabs is a capsule segmented control, as the period picker of Pulse.
// It reports whether the choice changed.
func PillTabs(c *ui.Context, selected *int, labels ...string) bool {
	t := c.Theme()
	seg := ui.SegmentedBase(c, selected, len(labels))
	seg.Track.Padding(3).Gap(2).Radius(999).Background(t.Text.Alpha(0.06)).Children(func() {
		for i, l := range labels {
			s := seg.Segment(i).Padding(5, 14).Radius(999)
			col := t.TextMuted
			if i == *selected {
				col = t.Text
				s.Background(pal(t).Inset).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
			}
			s.Children(func() { ui.Text(c, l).FontSize(13).FontWeight(500).TextColor(col).SingleLine() })
		}
	})
	// Querying Changed applies the click now, while selected still points
	// at the caller's variable; otherwise mygo applies it after the frame.
	return seg.Track.Changed()
}

// Bar is a thin progress bar, full at frac 1.
func Bar(c *ui.Context, frac float64, col ui.Color) ui.Element {
	frac = math.Max(0, math.Min(1, frac))
	return ui.Box(c).Height(5).Radius(3).Background(col.Alpha(0.18)).Draw(func(p *ui.Painter, r ui.Rect) {
		if frac > 0 {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(r.H, r.W*float32(frac)), H: r.H}, col, r.H/2)
		}
	})
}

// DotSeg is a part of a column of a dot chart: Value units in Color.
type DotSeg struct {
	Value float64
	Color ui.Color
}

// DotChart draws columns of dots, the latest at the right, in a tinted
// well. Each column stacks its segments from the bottom, scaled so that
// top fills the height; columns with no data leave dim dots.
func DotChart(c *ui.Context, k Tint, cols [][]DotSeg, top float64) ui.Element {
	t := c.Theme()
	well := k.C(t).Alpha(0.10)
	dim := k.C(t).Alpha(0.22)
	return ui.Box(c).Height(66).Radius(12).Background(well).Draw(func(p *ui.Painter, r ui.Rect) {
		const step, rows = 7.0, 7
		inner := ui.Rect{X: r.X + 12, Y: r.Y + 10, W: r.W - 24, H: r.H - 20}
		n := int(inner.W / step)
		if n <= 0 {
			return
		}
		dy := inner.H / (rows - 1)
		for i := range n {
			x := inner.X + inner.W - float32(n-1-i)*step - 1.5
			var col []DotSeg
			if j := len(cols) - n + i; j >= 0 && j < len(cols) {
				col = cols[j]
			}
			// The colors of the dots from the bottom.
			lit := make([]ui.Color, 0, rows)
			if top > 0 {
				acc := 0.0
				for _, s := range col {
					from := int(math.Round(acc / top * rows))
					acc += s.Value
					to := min(rows, int(math.Round(acc/top*rows)))
					if s.Value > 0 && to <= from && from < rows {
						to = from + 1 // a value too small to round up still shows
					}
					for range to - from {
						lit = append(lit, s.Color)
					}
				}
			}
			for row := range rows {
				y := inner.Y + inner.H - float32(row)*dy
				col := dim
				if row < len(lit) {
					col = lit[row]
				}
				p.Fill(ui.Rect{X: x - 1.6, Y: y - 1.6, W: 3.2, H: 3.2}, col, 1.6)
			}
		}
	})
}

// ResponsiveColumns returns how many columns of cards at least minW wide
// fit across width.
func ResponsiveColumns(width, minW float32, most int) int {
	return max(1, min(most, int((width+14)/(minW+14))))
}
