package view

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// Small pieces shared by the logs and plugins pages.

// logsDangerPill is a PillButton for a destructive action: the label is
// red. The action still asks before it does anything.
func logsDangerPill(c *ui.Context, icon *ui.SVG, label string, disabled bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Disabled(disabled).Padding(6, 12).Gap(6).Radius(999).AlignItems(ui.Center)
	bg := t.Danger.Alpha(0.10)
	switch {
	case disabled:
		bg = t.Text.Alpha(0.06)
	case b.Pressed():
		bg = t.Danger.Alpha(0.24)
	case b.Hovered():
		bg = t.Danger.Alpha(0.17)
	}
	b.Background(bg)
	return b.Children(func() {
		col := t.Danger
		if disabled {
			col = t.TextMuted
		}
		if icon != nil {
			ui.Icon(c, icon).FontSize(13).TextColor(col)
		}
		ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(col).SingleLine()
	})
}

// logsRule is the hairline between the rows of a list card.
func logsRule(c *ui.Context) {
	ui.Box(c).Height(1).Margin(0, 14).Background(pal(c.Theme()).Line)
}

// logsListCard is a card of rows: fn builds them, and calls sep between.
func logsListCard(c *ui.Context, n int, row func(i int)) {
	Card(c).Padding(4, 0).Gap(0).Children(func() {
		for i := range n {
			if i > 0 {
				logsRule(c)
			}
			row(i)
		}
	})
}

// Levels of a log line, for its color.
const (
	logsLevelInfo = iota
	logsLevelWarn
	logsLevelError
)

// logsMark colors the runes start..end of a logsDoc's text.
type logsMark struct {
	start, end int
	level      int
}

// logsDoc is the text of a log viewer. The viewer is ONE read-only text area
// holding every line, not a text element per line: a drag then selects
// across lines (and past the viewport, the area scrolls as the pointer
// nears its edge) and Cmd+C copies exactly what is selected. A per-line
// Selectable text only selects within its own line, and rows a list
// recycles would drop the selection's ends.
type logsDoc struct {
	text   string
	marks  []logsMark
	ver    int // the version of the lines the text is of; see set
	scroll ui.ScrollState
	// holding is set while the text area has the focus and a selection:
	// the text is not replaced then, so the selection stays where it is.
	holding bool
	// private records that the text was made in privacy mode.
	private bool
}

// set makes the text of lines, colored by their level. ver names the lines
// to the caller, who sets them again only when it changes.
func (d *logsDoc) set(lines []string, ver int) {
	var b strings.Builder
	d.marks = d.marks[:0]
	runes := 0
	d.private = Private()
	for i, l := range lines {
		l = PrivText(l)
		if i > 0 {
			b.WriteByte('\n')
			runes++
		}
		n := utf8.RuneCountInString(l)
		if lv := logsLineLevel(l); lv != logsLevelInfo && n > 0 {
			d.marks = append(d.marks, logsMark{runes, runes + n, lv})
		}
		b.WriteString(l)
		runes += n
	}
	d.text, d.ver = b.String(), ver
}

// newLogsDoc is a doc of lines that is not tailed.
func newLogsDoc(lines []string) *logsDoc {
	d := &logsDoc{}
	d.set(lines, 0)
	return d
}

// logsFollowSlack is how far from the end the view still counts as at it:
// the estimated heights of text not yet laid out are off by a little.
const logsFollowSlack = 8

// logsMono is a monospace block on a card: the lines of a log or a result,
// selectable across lines, in a scrolling read-only text area. With follow
// the view stays on the newest lines, unless the user scrolled up from the
// end, or while a selection is held (see logsDoc.holding). With h 0 it takes
// the height the page leaves.
func logsMono(c *ui.Context, key string, d *logsDoc, h float32, bg ui.Color, follow bool) ui.Element {
	t := c.Theme()
	if follow && d.scroll.Y >= d.scroll.MaxY-logsFollowSlack {
		d.scroll.Y = math.MaxFloat32 // the end, wherever the new lines put it
	}
	box := ui.Column(c)
	if h > 0 {
		box = box.Height(h)
	} else {
		box = fillHeight(box, logsListLeast)
	}
	return box.Padding(8, 4).Radius(14).Background(bg).Children(func() {
		var ranges []ui.TextRange
		for _, m := range d.marks {
			col := t.Warning
			if m.level == logsLevelError {
				col = t.Danger
			}
			ranges = append(ranges, ui.TextRange{Start: m.start, End: m.end, Color: col})
		}
		area := ui.TextArea(c.Key(key), &d.text).ReadOnly(true).Font("monospace").FontSize(11).TextColor(t.Text).
			Label(T("日志文本", "Log text")).
			Background(ui.Transparent).Border(0, ui.Transparent).Radius(0).Padding(1, 10).
			TrackScroll(&d.scroll).TextRanges(ranges...).Grow(1)
		start, end := area.TextSelection()
		d.holding = area.Focused() && start != end
	})
}
