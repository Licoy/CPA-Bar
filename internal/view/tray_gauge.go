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

// The menu bar can show the quota of the tray instance's first enabled
// accounts instead of the logo: a bar an account, filled to what is left
// of its short limit, else of its long one, with a line of text beside it that names the provider, the
// limit, what is left and when it refills. The tray instance is the
// panel's default one, else the first; its pinned accounts come first.
// Its quota is read as for the accounts page, only while the instance is
// online, so a refused key is not tried again and again.

const trayMaxBars = 2

// trayGauge is the quota of one account, in whole percents so that small
// changes do not redraw the icon.
type trayGauge struct {
	// known is false until the quota is read.
	known bool
	// left is the percent left of the limit the bar shows.
	left int
}

// trayRow is an account the menu bar shows.
type trayRow struct {
	gauge trayGauge
	// text is its line in the menu bar, "Codex · 每周 84% · 10/18".
	text string
	// tip is its line in the tooltip.
	tip string
}

// trayAccounts are the accounts of s the menu bar shows: the first
// enabled ones that have a quota, pinned first, in the order of the
// account list.
func trayAccounts(s core.InstanceSnapshot, limit int) []cpa.Credential {
	var out []cpa.Credential
	for _, cr := range pinnedFirst(sortedForPanel(s.Credentials), s.Profile.PinnedAccounts) {
		if len(out) == limit {
			break
		}
		if cr.Health() == cpa.HealthDisabled || !quota.Supported(cr) {
			continue
		}
		out = append(out, cr)
	}
	return out
}

// trayRows is the quota of the tray instance's accounts. It starts the
// reads that are due.
func (a *App) trayRows(limit int, now time.Time) []trayRow {
	s, ok := a.trayInstance()
	if !ok || s.Profile.Paused {
		return nil
	}
	var rows []trayRow
	for _, cr := range trayAccounts(s, limit) {
		var e *quotaEntry
		if s.Conn == core.ConnOnline {
			e, _, _ = a.quotaEntryFor(s, cr, now)
		} else if q := a.quotas[s.Profile.ID]; q != nil {
			e = q.entries[cr.AuthIndex]
		}
		var short, long *quota.Window
		if e != nil && e.res != nil {
			short, long = e.res.Summary()
		}
		rows = append(rows, newTrayRow(cr, short, long, now))
	}
	return rows
}

// newTrayRow shows an account's short limit (mostly of five hours), else
// its long one (weekly or monthly), else the first it has: Summary gives
// that as the long one.
func newTrayRow(cr cpa.Credential, short, long *quota.Window, now time.Time) trayRow {
	w := short
	if w == nil {
		w = long
	}
	name := trayProviderName(cr.ProviderName())
	r := trayRow{text: name + " · " + T("额度未知", "quota unknown"), tip: PrivName(cr.DisplayName()) + "  " + T("额度未知", "quota unknown")}
	if w == nil {
		return r
	}
	r.gauge = trayGauge{known: true, left: percent(w.Remaining)}
	r.text = fmt.Sprintf("%s · %s %d%%", name, trayLimitName(w.Label), r.gauge.left)
	if !w.ResetAt.IsZero() {
		layout := "01/02"
		if w.ResetAt.Sub(now) < 24*time.Hour {
			layout = "15:04"
		}
		r.text += " · " + w.ResetAt.Local().Format(layout)
	}
	var parts []string
	for _, w := range []*quota.Window{short, long} {
		if w != nil {
			parts = append(parts, fmt.Sprintf("%s %d%%", T(w.Label.ZH, w.Label.EN), percent(w.Remaining)))
		}
	}
	r.tip = PrivName(cr.DisplayName()) + "  " + strings.Join(parts, " · ")
	return r
}

// trayProviderName is a provider's name short enough for the menu bar:
// "Codex (OpenAI)" → "Codex".
func trayProviderName(id string) string {
	if k := providerKey(id); k == "xai" || k == "grok" {
		return "Grok"
	}
	name, _, _ := strings.Cut(providerLabel(id), " (")
	return name
}

// trayLimitName is a limit's label without the word for limit: "周限额" →
// "每周", "5-hour limit" → "5-hour".
func trayLimitName(label quota.Text) string {
	s := T(label.ZH, label.EN)
	for _, suffix := range []string{"剩余额度", "限额", "额度", " remaining", " limit", " quota"} {
		if cut, ok := strings.CutSuffix(strings.ToLower(s), suffix); ok && cut != "" {
			s = s[:len(cut)]
			break
		}
	}
	switch s {
	case "周":
		return "每周"
	case "月度":
		return "每月"
	}
	return s
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

// percent is the share left, rounded down as the panel shows it.
func percent(f float64) int { return int(math.Floor(max(0, min(1, f))*100 + 1e-9)) }

// trayGaugeLimit is how many accounts the menu bar shows in a style: none
// but with the bars, and only where the bar has room for text (macOS).
func trayGaugeLimit(style string) int {
	if !mygoTrayTitleSupported() || style != store.TrayStyleBars {
		return 0
	}
	return trayMaxBars
}

// ---- Drawing ----

// The bars are drawn at twice the size in points, 18 points tall like
// every menu bar image, in color whatever the icon's setting: the track in
// gray, the level in green, amber or red by what is left, as in the panel
// (in its dark theme's colors, which show on any bar). Each bar sits
// beside its line of the title.

const (
	gaugeScale  = 2
	gaugeHeight = 18
)

var (
	gaugeOutline = color.NRGBA{R: 142, G: 142, B: 147, A: 255}
	gaugeGreen   = color.NRGBA{R: 0x30, G: 0xD1, B: 0x58, A: 255}
	gaugeAmber   = color.NRGBA{R: 0xFF, G: 0x9F, B: 0x0A, A: 255}
	gaugeRed     = color.NRGBA{R: 0xFF, G: 0x45, B: 0x3A, A: 255}
)

func fade(c color.NRGBA, f float64) color.NRGBA {
	c.A = uint8(float64(c.A)*f + 0.5)
	return c
}

// levelColor is the color of a bar at p percent left.
func levelColor(p int) color.NRGBA {
	return [...]color.NRGBA{gaugeGreen, gaugeAmber, gaugeRed}[quotaTier(float64(p)/100)]
}

// barsIcon draws a bar an account, one over the other; an unknown quota is
// an empty track.
func barsIcon(gs []trayGauge) []byte {
	const (
		width = 36.0
		barH  = 4.0
		// pitch is the distance between the lines of the title.
		pitch = 10.0
	)
	cv := newGaugeCanvas(width)
	y := (gaugeHeight-barH)/2 - pitch*float64(len(gs)-1)/2
	track := fade(gaugeOutline, 0.45)
	for _, g := range gs {
		cv.fill(0, y, width, barH, barH/2, track)
		if g.known && g.left > 0 {
			w := max(width*float64(g.left)/100, barH)
			cv.fill(0, y, w, barH, barH/2, levelColor(g.left))
		}
		y += pitch
	}
	return cv.png()
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
