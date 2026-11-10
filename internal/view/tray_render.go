package view

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

// The quota styles are drawn as a row of components, an account each or
// two stacked, in a 22 point tall status item.
const (
	trayContentHeight             = 22
	trayHorizontalPadding float32 = 4
	// trayComponentGap sets the components apart.
	trayComponentGap float32 = 10
	// trayIconSize is the logo of a component's provider, trayRowIconSize
	// the logo of a stacked row.
	trayIconSize    float32 = 13
	trayRowIconSize float32 = 9
)

// trayRowCenters are the vertical centers of one row, or of two stacked.
var trayRowCenters = [...][]float32{{11}, {6, 16}}

func trayRowsAt(n int) []float32 { return trayRowCenters[min(2, max(1, n))-1] }

// trayImageOptions picks a layer: the ink only (foreground), the quota
// colors only (colorsOnly), or both, in the ink of a light menu bar
// (black) or of a dark one (white).
type trayImageOptions struct {
	light                  bool
	foreground, colorsOnly bool
}

// trayPaint is one layer's colors: the ink of glyphs, tracks and logos, and
// how opaque the quota colors are. macOS draws the ink as a template image
// and the colors over it, each with the other left out.
type trayPaint struct {
	ink          ui.Color
	colorOpacity float32
}

// renderTrayImage draws r at twice its size in points.
func renderTrayImage(r trayRender, options trayImageOptions) ([]byte, error) {
	if r.style == store.TrayStyleIcon || !slices.Contains(store.TrayStyles, r.style) {
		return nil, fmt.Errorf("unsupported tray style %q", r.style)
	}
	width := trayContentWidth(r)
	img := ui.Render(func(c *ui.Context) {
		theme := ui.LightTheme()
		theme.Background = ui.Transparent
		c.SetTheme(theme)
		ui.Box(c).Fill().Draw(func(p *ui.Painter, rect ui.Rect) {
			ink := ui.RGB(0, 0, 0)
			if !options.light {
				ink = ui.RGB(255, 255, 255)
			}
			paint := trayPaint{ink: ink, colorOpacity: 1}
			if options.foreground {
				paint.colorOpacity = 0
			}
			if options.colorsOnly {
				paint.ink = ui.Transparent
			}
			drawTrayContent(p, r, paint)
		})
	}, int(math.Ceil(float64(width))), trayContentHeight, 2)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("encode tray: %w", err)
	}
	return out.Bytes(), nil
}

// trayContentWidth is the width the content takes, padding included.
func trayContentWidth(r trayRender) float32 { return layoutTray(trayPen{}, r) }

// drawTrayContent draws the content from the origin.
func drawTrayContent(p *ui.Painter, r trayRender, paint trayPaint) {
	layoutTray(trayPen{p: p, paint: paint}, r)
}

// layoutTray places every component and returns the total width; the same
// pass measures and draws, so the two cannot drift apart.
func layoutTray(d trayPen, r trayRender) float32 {
	x := trayHorizontalPadding
	comps := r.components
	if len(comps) == 0 {
		comps = []trayComponent{{cells: []trayCell{{empty: true, percent: "--", countdown: "--", meter: "--", battery: "--", ring: "--"}}}}
	}
	for i, comp := range comps {
		if i > 0 {
			x += trayComponentGap
		}
		x += layoutTrayComponent(d, r, comp, x)
	}
	return x + trayHorizontalPadding
}

// layoutTrayComponent lays out one component from x and returns its width.
// An account alone leads with its provider's logo; stacked accounts lead
// each row with theirs.
func layoutTrayComponent(d trayPen, r trayRender, comp trayComponent, x float32) float32 {
	if len(comp.cells) == 0 {
		return 0
	}
	start := x
	icons := !r.hideIcons
	if r.style == store.TrayStyleRings {
		if r.ringValues && ringValuesFit(comp.cells) {
			return trayValueRings(d, comp.cells, x, icons)
		}
		return trayRings(d, comp, x, icons)
	}
	if icons && comp.provider != "" {
		d.icon(comp.provider, x, 11, trayIconSize)
		x += trayIconSize + 4
	}
	// Rows are told apart by the logo where two accounts stack, by the
	// limit's name where one account shows two limits.
	rowIcons := icons && comp.provider == ""
	switch r.style {
	case store.TrayStyleMeters:
		x += trayMeters(d, comp.cells, x, rowIcons)
	case store.TrayStyleCountdown:
		x += trayCountdown(d, comp.cells, x, rowIcons)
	case store.TrayStyleBattery:
		if r.vertical {
			x += trayVerticalBatteries(d, comp.cells, x, rowIcons)
		} else {
			x += trayHorizontalBatteries(d, comp.cells, x, rowIcons)
		}
	case store.TrayStyleText:
		x += trayText(d, comp.cells, x)
	}
	return x - start
}

// ---- Meters: a bar per limit, the detail after it.

func trayMeters(d trayPen, cells []trayCell, x float32, rowIcons bool) float32 {
	if len(cells) == 1 {
		c := cells[0]
		d.bar(x, 11, 34, 6, c)
		return 34 + 5 + d.text(c.meter, x+39, 11, 11, 600, 1, 96)
	}
	lead := d.rowLead(cells, x, rowIcons)
	w := float32(0)
	for i, c := range cells[:2] {
		cy := trayRowsAt(2)[i]
		rx := x + lead
		d.bar(rx, cy, 28, 4, c)
		rx += 28 + 4
		rx += d.text(c.meter, rx, cy, 8.5, 600, 1, 84)
		w = max(w, rx-x)
	}
	return w
}

// ---- Countdown: the time left until the refill.

func trayCountdown(d trayPen, cells []trayCell, x float32, rowIcons bool) float32 {
	if len(cells) == 1 {
		return d.text(cells[0].countdown, x, 11, 12.5, 600, 1, 120)
	}
	lead := d.rowLead(cells, x, rowIcons)
	w := float32(0)
	for i, c := range cells[:2] {
		w = max(w, lead+d.text(c.countdown, x+lead, trayRowsAt(2)[i], 9, 600, 1, 96))
	}
	return w
}

// ---- Battery lying flat: one limit is a battery with the percentage
// inside and the detail after it; stacked rows are too low for text
// inside, so the detail follows a small battery.

func trayHorizontalBatteries(d trayPen, cells []trayCell, x float32, rowIcons bool) float32 {
	if len(cells) == 1 {
		c := cells[0]
		w := max(30, d.measure(c.percent, 9, 700)+12)
		d.hBattery(ui.Rect{X: x, Y: 4.5, W: w, H: 13}, c, c.percent, 9)
		w += 2.5
		if c.battery != c.percent {
			w += 5 + d.text(c.battery, x+w+5, 11, 11, 600, 1, 96)
		}
		return w
	}
	lead := d.rowLead(cells, x, rowIcons)
	w := float32(0)
	for i, c := range cells[:2] {
		cy := trayRowsAt(2)[i]
		rx := x + lead
		d.hBattery(ui.Rect{X: rx, Y: cy - 4, W: 17, H: 8}, c, "", 0)
		rx += 17 + 2 + 4
		rx += d.text(c.battery, rx, cy, 8.5, 600, 1, 84)
		w = max(w, rx-x)
	}
	return w
}

// ---- Battery standing: a slim battery per limit, filling up, with the
// text beside them: the percentage over the countdown for one limit, a
// row of the chosen detail per limit otherwise.

func trayVerticalBatteries(d trayPen, cells []trayCell, x float32, rowIcons bool) float32 {
	start := x
	for i, c := range cells[:min(2, len(cells))] {
		if i > 0 {
			x += 2.5
		}
		d.vBattery(ui.Rect{X: x, Y: 4.5, W: 8.5, H: 15}, c)
		x += 8.5
	}
	x += 5
	if len(cells) == 1 {
		c := cells[0]
		if c.countdown == c.percent || c.empty {
			return x - start + d.text(c.percent, x, 11, 11, 650, 1, 96)
		}
		w := d.text(c.percent, x, 6.5, 9, 700, 1, 96)
		w = max(w, d.text(c.countdown, x, 16, 8, 500, 0.62, 96))
		return x - start + w
	}
	lead := d.rowLead(cells, x, rowIcons)
	w := float32(0)
	for i, c := range cells[:2] {
		w = max(w, lead+d.text(c.battery, x+lead, trayRowsAt(2)[i], 8.5, 600, 1, 84))
	}
	return x - start + w
}

// ---- Rings: a ring around the logo, or concentric rings after it, the
// first limit outside; the percentages beside them.

func trayRings(d trayPen, comp trayComponent, x float32, icons bool) float32 {
	cells := comp.cells
	start := x
	if len(cells) > 1 && icons {
		// Two rings leave no room inside for the logo.
		d.icon(cells[0].provider, x, 11, trayIconSize)
		x += trayIconSize + 4
	}
	cx, cy := x+10.25, float32(11)
	if len(cells) == 1 {
		d.ring(cx, cy, 8.4, 2.3, cells[0])
		if icons {
			d.icon(cells[0].provider, cx-4.75, cy, 9.5)
		}
	} else {
		d.ring(cx, cy, 9.1, 2, cells[0])
		d.ring(cx, cy, 5.4, 2, cells[1])
	}
	x += 20.5 + 4
	if len(cells) == 1 {
		return x - start + d.text(cells[0].percent, x, 11, 11, 650, 1, 96)
	}
	w := float32(0)
	for i, c := range cells[:2] {
		w = max(w, d.text(c.percent, x, trayRowsAt(2)[i], 8.5, 600, 1, 84))
	}
	return x - start + w
}

// ---- Rings with values: the logo, then a ring per limit with its value
// inside, the first limit first.

// A ring with its value inside is thinner than a plain ring, to leave the
// value room.
const (
	trayValueRingRadius float32 = 9.2
	trayValueRingWidth  float32 = 1.7
)

func trayValueRings(d trayPen, cells []trayCell, x float32, icons bool) float32 {
	start := x
	if icons {
		d.icon(cells[0].provider, x, 11, trayIconSize)
		x += trayIconSize + 4
	}
	for i, c := range cells[:min(2, len(cells))] {
		if i > 0 {
			x += 3
		}
		cx := x + 10.25
		d.ring(cx, 11, trayValueRingRadius, trayValueRingWidth, c)
		d.ringText(c.ring, cx, 11, trayValueRingRadius-trayValueRingWidth/2)
		x += 20.5
	}
	return x - start
}

// ringValuesFit reports whether every limit has a value for inside a
// ring; a balance keeps its amount beside the ring.
func ringValuesFit(cells []trayCell) bool {
	for _, c := range cells {
		if c.ring == "" {
			return false
		}
	}
	return true
}

// ---- Text: the first limit large, the second small after it.

func trayText(d trayPen, cells []trayCell, x float32) float32 {
	w := d.text(cells[0].percent, x, 11, 12.5, 650, 1, 96)
	if len(cells) > 1 {
		w += 3 + d.text(cells[1].percent, x+w+3, 12, 9.5, 500, 0.6, 72)
	}
	return w
}

// trayPen draws in a layer, or only measures without a painter.
type trayPen struct {
	p     *ui.Painter
	paint trayPaint
}

func (d trayPen) draws() bool { return d.p != nil }

func (d trayPen) inks() bool { return d.p != nil && d.paint.ink.A > 0 }

func (d trayPen) measure(s string, size float32, weight int) float32 {
	return trayTextWidth(s, trayFont(size, weight))
}

// text draws s centered on cy from x, at most maxW wide, and returns its
// width. alpha dims the ink of secondary text.
func (d trayPen) text(s string, x, cy, size float32, weight int, alpha, maxW float32) float32 {
	f := trayFont(size, weight)
	s = trayFit(s, f, maxW)
	w := trayTextWidth(s, f)
	if d.inks() {
		m := f.Metrics()
		d.p.Glyphs(ui.Shape(s, f), x, cy-(m.Ascent+m.Descent)/2+m.Ascent, d.paint.ink.Alpha(alpha))
	}
	return w
}

// ringText draws s centered inside a ring of that inner radius.
func (d trayPen) ringText(s string, cx, cy, radius float32) {
	size := ringTextSize(s, radius)
	d.text(s, cx-d.measure(s, size, 600)/2, cy, size, 600, 1, 0)
}

// ringTextSize is the size of s inside a ring of that inner radius, small
// enough to keep clear of the ring and smaller still for three digits.
func ringTextSize(s string, radius float32) float32 {
	size, room := float32(8), 2*radius-5
	for size > 5 && trayTextWidth(s, trayFont(size, 600)) > room {
		size -= 0.25
	}
	return size
}

// rowLead draws what tells two stacked rows apart, the providers' logos or
// the limits' names, and returns the width it takes.
func (d trayPen) rowLead(cells []trayCell, x float32, icons bool) float32 {
	if icons {
		for i, c := range cells[:min(2, len(cells))] {
			d.icon(c.provider, x, trayRowsAt(2)[i], trayRowIconSize)
		}
		return trayRowIconSize + 3.5
	}
	w := float32(0)
	for _, c := range cells {
		if c.label != "" {
			w = max(w, d.measure(c.label, 8, 500))
		}
	}
	if w == 0 {
		return 0
	}
	for i, c := range cells[:min(2, len(cells))] {
		d.text(c.label, x, trayRowsAt(2)[i], 8, 500, 0.62, 40)
	}
	return min(w, 40) + 4
}

// icon draws a provider's monochrome logo in the ink; a provider LobeHub
// has no logo of shows the plug.
func (d trayPen) icon(provider string, x, cy, size float32) {
	if !d.inks() {
		return
	}
	svg := providerMonoLogo(provider)
	if svg == nil {
		svg = icPlug
	}
	d.p.Icon(svg, ui.Rect{X: x, Y: cy - size/2, W: size, H: size}, d.paint.ink)
}

// level is the cell's quota color, as this layer draws it, and the
// fraction left.
func (d trayPen) level(c trayCell) (ui.Color, float32) {
	if c.empty {
		return ui.Transparent, 0
	}
	return trayLevelColor(c.tier).Alpha(d.paint.colorOpacity), float32(min(100, max(0, c.left))) / 100
}

func (d trayPen) bar(x, cy, w, h float32, c trayCell) {
	if !d.draws() {
		return
	}
	r := ui.Rect{X: x, Y: cy - h/2, W: w, H: h}
	if d.inks() {
		d.p.Fill(r, d.paint.ink.Alpha(0.22), h/2)
	}
	if fill, level := d.level(c); level > 0 && fill.A > 0 {
		r.W = max(h, w*level)
		d.p.Fill(r, fill, h/2)
	}
}

func (d trayPen) ring(cx, cy, radius, width float32, c trayCell) {
	if !d.draws() {
		return
	}
	if d.inks() {
		d.p.StrokePath((&ui.Path{}).Circle(cx, cy, radius), width, d.paint.ink.Alpha(0.22))
	}
	if fill, level := d.level(c); level > 0 && fill.A > 0 {
		ringArc(d.p, cx, cy, radius, width, float64(level), fill)
	}
}

// ringArc strokes the share level of a ring, clockwise from the top.
func ringArc(p *ui.Painter, cx, cy, radius, width float32, level float64, c ui.Color) {
	if level <= 0 {
		return
	}
	path := &ui.Path{}
	steps := max(2, int(math.Ceil(level*72)))
	for i := 0; i <= steps; i++ {
		a := -math.Pi/2 + level*2*math.Pi*float64(i)/float64(steps)
		x, y := cx+radius*float32(math.Cos(a)), cy+radius*float32(math.Sin(a))
		if i == 0 {
			path.MoveTo(x, y)
		} else {
			path.LineTo(x, y)
		}
	}
	p.StrokePath(path, width, c)
}

// hBattery is a battery lying in body, its cap at the right, with text
// inside when size is set. The text is ink over the empty part and white
// over the fill, which the color layer covers the ink with.
func (d trayPen) hBattery(body ui.Rect, c trayCell, text string, size float32) {
	if !d.draws() {
		return
	}
	stroke, radius, inset := float32(1.1), body.H*0.3, float32(1.9)
	if body.H < 10 {
		stroke, radius, inset = 1, 2.2, 1.6
	}
	if d.inks() {
		d.p.Stroke(body, d.paint.ink.Alpha(0.5), radius, stroke)
		d.p.Fill(ui.Rect{X: body.X + body.W + 0.9, Y: body.Y + body.H*0.32, W: 1.6, H: body.H * 0.36}, d.paint.ink.Alpha(0.5), 0.8)
	}
	track := insetRect(body, inset)
	fill, level := d.level(c)
	filled := track
	filled.W = min(track.W, max(1.5, track.W*level))
	if level > 0 && fill.A > 0 {
		d.p.Fill(filled, fill, max(1, radius-inset))
	}
	if text == "" {
		return
	}
	f := trayFont(size, 700)
	text = trayFit(text, f, track.W-2)
	w := trayTextWidth(text, f)
	m := f.Metrics()
	tx, base := track.X+(track.W-w)/2, body.Y+(body.H-m.Ascent-m.Descent)/2+m.Ascent
	if d.inks() {
		d.p.Glyphs(ui.Shape(text, f), tx, base, d.paint.ink)
	}
	if level > 0 && fill.A > 0 {
		d.p.Clip(filled, max(1, radius-inset), func() { d.p.Glyphs(ui.Shape(text, f), tx, base, ui.RGB(255, 255, 255)) })
	}
}

// vBattery is a battery standing in body, its cap on top, filling up.
func (d trayPen) vBattery(body ui.Rect, c trayCell) {
	if !d.draws() {
		return
	}
	if d.inks() {
		d.p.Stroke(body, d.paint.ink.Alpha(0.5), 2.4, 1.1)
		d.p.Fill(ui.Rect{X: body.X + body.W*0.3, Y: body.Y - 2.2, W: body.W * 0.4, H: 1.5}, d.paint.ink.Alpha(0.5), 0.7)
	}
	if fill, level := d.level(c); level > 0 && fill.A > 0 {
		track := insetRect(body, 1.8)
		h := min(track.H, max(1.5, track.H*level))
		d.p.Fill(ui.Rect{X: track.X, Y: track.Y + track.H - h, W: track.W, H: h}, fill, 1)
	}
}

func insetRect(r ui.Rect, d float32) ui.Rect {
	return ui.Rect{X: r.X + d, Y: r.Y + d, W: max(0, r.W-2*d), H: max(0, r.H-2*d)}
}

func trayTextWidth(s string, font ui.Font) float32 {
	var width float32
	for _, g := range ui.Shape(s, font) {
		width = max(width, g.X+g.Advance)
	}
	return width
}

// trayFont is the menu bar font: tabular digits keep a ticking countdown
// from shifting what follows it.
func trayFont(size float32, weight int) ui.Font {
	return ui.Font{Size: size, Weight: weight, Features: "tnum"}
}

// trayFit shortens s with an ellipsis to fit maxW. A subpixel float32
// difference must not replace a fitting last glyph with the ellipsis.
func trayFit(s string, f ui.Font, maxW float32) string {
	if maxW <= 0 || trayTextWidth(s, f) <= maxW+0.01 {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if t := strings.TrimSpace(string(runes)) + "…"; trayTextWidth(t, f) <= maxW+0.01 {
			return t
		}
	}
	return "…"
}

// trayLevelColor is the color of a quota tier: green, amber, red, bright
// enough for a light or a dark menu bar.
func trayLevelColor(tier int) ui.Color {
	return [...]ui.Color{ui.RGB(48, 209, 88), ui.RGB(255, 159, 10), ui.RGB(255, 69, 58)}[tier]
}
