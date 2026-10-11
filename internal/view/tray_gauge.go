package view

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

// The menu bar can show the quota of the tray instance's enabled accounts
// instead of the logo: as upright batteries, one an account, or as bars,
// the short limit over the long one. The tray instance is the panel's
// default one, else the first. Its quota is read as for the accounts
// page, only while the instance is online, so a refused key is not tried
// again and again.

const (
	trayMaxBatteries = 6
	trayMaxBars      = 2
)

// trayGauge is the quota of one account, in whole percents so that small
// changes do not redraw the icon.
type trayGauge struct {
	// known is false until the quota is read.
	known bool
	// short and long are the percents left of the short (5-hour) and the
	// long (weekly) limit; -1 when the account has no such limit.
	short, long int
}

// level is what a battery shows: the lower of the two limits.
func (g trayGauge) level() int {
	switch {
	case g.short < 0:
		return g.long
	case g.long < 0:
		return g.short
	}
	return min(g.short, g.long)
}

// trayGauges is the quota of the tray instance's first enabled accounts
// that have one, and a line of the tooltip for each. It starts the reads
// that are due.
func (a *App) trayGauges(limit int, now time.Time) (gauges []trayGauge, lines []string) {
	s, ok := a.trayInstance()
	if !ok || s.Profile.Paused {
		return nil, nil
	}
	for _, cr := range s.Credentials {
		if len(gauges) == limit {
			break
		}
		if cr.Health() == cpa.HealthDisabled || !quota.Supported(cr) {
			continue
		}
		var e *quotaEntry
		if s.Conn == core.ConnOnline {
			e, _, _ = a.quotaEntryFor(s, cr, now)
		} else if q := a.quotas[s.Profile.ID]; q != nil {
			e = q.entries[cr.AuthIndex]
		}
		g := trayGauge{short: -1, long: -1}
		if e != nil && e.res != nil {
			short, long := e.res.Summary()
			if short != nil {
				g.short = percent(short.Remaining)
			}
			if long != nil {
				g.long = percent(long.Remaining)
			}
			g.known = short != nil || long != nil
		}
		gauges = append(gauges, g)
		lines = append(lines, PrivName(cr.DisplayName())+"  "+g.text())
	}
	return gauges, lines
}

// trayInstance is the instance whose accounts the menu bar shows.
func (a *App) trayInstance() (core.InstanceSnapshot, bool) {
	snaps := a.Svc.Snapshots()
	id := a.Svc.Settings().DefaultInstanceID
	for _, s := range snaps {
		if s.Profile.ID == id {
			return s, true
		}
	}
	if len(snaps) > 0 {
		return snaps[0], true
	}
	return core.InstanceSnapshot{}, false
}

func percent(f float64) int { return int(math.Round(max(0, min(1, f)) * 100)) }

// text says the gauge in the tooltip.
func (g trayGauge) text() string {
	if !g.known {
		return T("额度未知", "quota unknown")
	}
	var parts []string
	if g.short >= 0 {
		parts = append(parts, fmt.Sprintf(T("5 小时 %d%%", "5h %d%%"), g.short))
	}
	if g.long >= 0 {
		parts = append(parts, fmt.Sprintf(T("周 %d%%", "week %d%%"), g.long))
	}
	return strings.Join(parts, " · ")
}

// trayGaugeStyle is the style the menu bar takes: a quota style only where
// the bar is wide (macOS) and when there are accounts to show.
func trayGaugeStyle(style string) (string, int) {
	if !mygoTrayTitleSupported() {
		return store.TrayStyleIcon, 0
	}
	switch style {
	case store.TrayStyleBattery:
		return style, trayMaxBatteries
	case store.TrayStyleBars:
		return style, trayMaxBars
	}
	return store.TrayStyleIcon, 0
}

// ---- Drawing ----

// The gauges are drawn at twice the size in points, 18 points tall like
// every menu bar image. Mono draws a template, which macOS tints to suit
// the bar; it tells the limits apart by opacity. Color draws on any bar:
// the outlines in gray, the levels in color.

const (
	gaugeScale  = 2
	gaugeHeight = 18
)

var (
	gaugeOutline = color.NRGBA{R: 142, G: 142, B: 147, A: 255}
	gaugeGreen   = color.NRGBA{R: 52, G: 199, B: 89, A: 255}
	gaugeOrange  = color.NRGBA{R: 255, G: 159, B: 10, A: 255}
	gaugeRed     = color.NRGBA{R: 255, G: 59, B: 48, A: 255}
	// gaugeShort and gaugeLong are the colors of the two limits of a bar:
	// the logo's blue and purple.
	gaugeShort = color.NRGBA{R: 10, G: 132, B: 255, A: 255}
	gaugeLong  = color.NRGBA{R: 175, G: 82, B: 222, A: 255}
	gaugeInk   = color.NRGBA{A: 255}
)

func fade(c color.NRGBA, f float64) color.NRGBA {
	c.A = uint8(float64(c.A)*f + 0.5)
	return c
}

// levelColor is the color of a battery's charge.
func levelColor(p int) color.NRGBA {
	switch {
	case p > 50:
		return gaugeGreen
	case p > 20:
		return gaugeOrange
	}
	return gaugeRed
}

// batteryIcon draws one upright battery an account, filled to the lower of
// its limits; an unknown quota is a faded, empty battery.
func batteryIcon(gs []trayGauge, mono bool) []byte {
	const (
		bodyW, bodyH = 7.5, 14.0
		capW, capH   = 3.5, 1.5
		gap          = 3.0
		stroke       = 1.0
		inset        = stroke + 0.75
	)
	width := float64(len(gs))*bodyW + float64(len(gs)-1)*gap
	cv := newGaugeCanvas(width)
	top := (gaugeHeight - bodyH - capH) / 2
	for i, g := range gs {
		x := float64(i) * (bodyW + gap)
		y := top + capH
		line, fill := gaugeOutline, levelColor(g.level())
		if mono {
			line, fill = gaugeInk, gaugeInk
		}
		if !g.known {
			line = fade(line, 0.4)
		}
		cv.fill(x+(bodyW-capW)/2, top, capW, capH+0.5, 0.6, line)
		cv.stroke(x, y, bodyW, bodyH, 2, stroke, line)
		if !g.known {
			continue
		}
		innerH := bodyH - 2*inset
		h := innerH * float64(g.level()) / 100
		if g.level() > 0 {
			h = max(h, 1)
		}
		cv.fill(x+inset, y+inset+innerH-h, bodyW-2*inset, h, 0.75, fill)
	}
	return cv.png()
}

// barsIcon draws a pair of bars an account: the short limit over the long
// one, or the long one alone when there is no short limit.
func barsIcon(gs []trayGauge, mono bool) []byte {
	const width = 30.0
	barH, inner, outer := 3.0, 1.0, 2.5
	if len(gs) == 1 {
		barH, inner = 5.0, 1.5
	}
	rows := 0
	for _, g := range gs {
		rows += g.bars()
	}
	total := float64(rows)*barH + float64(rows-len(gs))*inner + float64(len(gs)-1)*outer
	cv := newGaugeCanvas(width)
	y := (gaugeHeight - total) / 2
	short, long, track := gaugeShort, gaugeLong, fade(gaugeOutline, 0.45)
	if mono {
		short, long, track = gaugeInk, fade(gaugeInk, 0.55), fade(gaugeInk, 0.2)
	}
	bar := func(p int, col color.NRGBA) {
		cv.fill(0, y, width, barH, barH/2, track)
		if p > 0 {
			w := max(width*float64(p)/100, barH)
			cv.fill(0, y, w, barH, barH/2, col)
		}
		y += barH + inner
	}
	for _, g := range gs {
		switch {
		case !g.known:
			bar(0, track)
		case g.short >= 0 && g.long >= 0:
			bar(g.short, short)
			bar(g.long, long)
		case g.short >= 0:
			bar(g.short, short)
		default:
			bar(g.long, long)
		}
		y += outer - inner
	}
	return cv.png()
}

// bars is how many bars an account takes.
func (g trayGauge) bars() int {
	if g.known && g.short >= 0 && g.long >= 0 {
		return 2
	}
	return 1
}

// gaugeCanvas is an image measured in points, drawn with antialiased
// rounded rectangles.
type gaugeCanvas struct{ img *image.NRGBA }

func newGaugeCanvas(width float64) *gaugeCanvas {
	w := int(math.Ceil(width * gaugeScale))
	return &gaugeCanvas{image.NewNRGBA(image.Rect(0, 0, w, gaugeHeight*gaugeScale))}
}

func (cv *gaugeCanvas) png() []byte {
	var out bytes.Buffer
	_ = png.Encode(&out, cv.img)
	return out.Bytes()
}

// fill fills a rounded rectangle.
func (cv *gaugeCanvas) fill(x, y, w, h, r float64, col color.NRGBA) {
	cv.paint(x, y, w, h, col, func(px, py float64) bool {
		return inRRect(px, py, x, y, w, h, r)
	})
}

// stroke draws the outline of a rounded rectangle, inside its bounds.
func (cv *gaugeCanvas) stroke(x, y, w, h, r, width float64, col color.NRGBA) {
	cv.paint(x, y, w, h, col, func(px, py float64) bool {
		return inRRect(px, py, x, y, w, h, r) &&
			!inRRect(px, py, x+width, y+width, w-2*width, h-2*width, max(0, r-width))
	})
}

// paint blends col over the pixels of the box, by how much of each the
// shape covers (4×4 samples a pixel).
func (cv *gaugeCanvas) paint(x, y, w, h float64, col color.NRGBA, in func(px, py float64) bool) {
	const n = 4
	b := cv.img.Bounds()
	x0, y0 := max(0, int(x*gaugeScale)), max(0, int(y*gaugeScale))
	x1, y1 := min(b.Dx(), int(math.Ceil((x+w)*gaugeScale))), min(b.Dy(), int(math.Ceil((y+h)*gaugeScale)))
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			hit := 0
			for sy := range n {
				for sx := range n {
					if in((float64(px)+(float64(sx)+0.5)/n)/gaugeScale, (float64(py)+(float64(sy)+0.5)/n)/gaugeScale) {
						hit++
					}
				}
			}
			if hit > 0 {
				i := cv.img.PixOffset(px, py)
				blendOver(cv.img.Pix[i:i+4:i+4], col, float64(hit)/(n*n))
			}
		}
	}
}

// inRRect reports whether a point is inside a rounded rectangle.
func inRRect(px, py, x, y, w, h, r float64) bool {
	if w <= 0 || h <= 0 || px < x || py < y || px > x+w || py > y+h {
		return false
	}
	r = min(r, w/2, h/2)
	cx := min(max(px, x+r), x+w-r)
	cy := min(max(py, y+r), y+h-r)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

// blendOver draws col, with coverage cov, over the non-premultiplied pixel
// p.
func blendOver(p []uint8, col color.NRGBA, cov float64) {
	sa := float64(col.A) / 255 * cov
	da := float64(p[3]) / 255
	oa := sa + da*(1-sa)
	if oa <= 0 {
		return
	}
	mix := func(s, d uint8) uint8 {
		return uint8((float64(s)*sa+float64(d)*da*(1-sa))/oa + 0.5)
	}
	p[0], p[1], p[2] = mix(col.R, p[0]), mix(col.G, p[1]), mix(col.B, p[2])
	p[3] = uint8(oa*255 + 0.5)
}
