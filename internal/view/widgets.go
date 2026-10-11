package view

import (
	"github.com/egoist/mygo/ui"
)

// Shared building blocks, so that every page looks and behaves alike.

// PageHeader describes a page under the window's title, which names it,
// with actions at its right. The title is the window header's: it is
// shown here only when the page is not the one the header names (a page
// may head a part of itself).
func PageHeader(c *ui.Context, title, subtitle string, actions func()) {
	t := c.Theme()
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(12).TextColor(t.TextMuted).MaxLines(3)
			}
		})
		if actions != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(actions)
		}
	})
}

// Section is a titled group on a page.
func Section(c *ui.Context, title string, fn func()) ui.Element {
	t := c.Theme()
	return ui.Column(c).Gap(8).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(t.TextMuted).Padding(0, 4)
		}
		fn()
	})
}

// Card is a soft, borderless surface.
func Card(c *ui.Context) ui.Element {
	return ui.Column(c).Padding(14, 16).Gap(10).Radius(14).Background(pal(c.Theme()).Card)
}

// ErrorBanner shows an error, with the server's message, selectable to be
// copied. It shows nothing for a nil error.
func ErrorBanner(c *ui.Context, err error) {
	if err == nil {
		return
	}
	t := c.Theme()
	col := t.Danger
	if IsIndeterminate(err) {
		col = t.Warning
	}
	ui.Row(c).Gap(10).Padding(10, 12).Radius(12).Background(col.Alpha(0.12)).AlignItems(ui.Start).Children(func() {
		ui.Icon(c, icAlert).FontSize(15).TextColor(col)
		ui.Text(c, ErrorText(err)).Grow(1).FontSize(12).Selectable()
	})
}

// Notice shows an informative message.
func Notice(c *ui.Context, text string) {
	t := c.Theme()
	ui.Row(c).Gap(10).Padding(10, 12).Radius(12).Background(t.Accent.Alpha(0.10)).AlignItems(ui.Start).Children(func() {
		ui.Icon(c, icInfo).FontSize(15).TextColor(t.Accent)
		ui.Text(c, text).Grow(1).FontSize(12)
	})
}

// EmptyState fills a page or list with a message; children appended to the
// returned column stay centered with it, for example an action button.
func EmptyState(c *ui.Context, title, detail string) ui.Element {
	t := c.Theme()
	return ui.Column(c).Grow(1).Center().Gap(8).Padding(32).Children(func() {
		Tile(c, icServer, TintGray, 44, false)
		ui.Text(c, title).FontSize(16).FontWeight(600)
		if detail != "" {
			ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(420)
		}
	})
}

// Busy shows a spinner with a label while a task runs.
func Busy(c *ui.Context, label string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Spinner(c).Label(label)
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

// StatusText is a dot and a text naming a status, so that state never
// depends on color alone.
func StatusText(c *ui.Context, label string, col ui.Color) ui.Element {
	return ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		StatusDot(c, col)
		ui.Text(c, label).FontSize(12).TextColor(col)
	})
}

// TaskStatus shows the outcome of a task: a spinner while it runs, its
// error, or done text for a moment after it succeeded.
func TaskStatus(c *ui.Context, t *Task, running, done string) {
	switch {
	case t.Running:
		Busy(c, running)
	case t.Err != nil:
		ErrorBanner(c, t.Err)
	case t.Done && done != "" && c.Now().Sub(t.At) < 4e9:
		ui.Text(c, done).FontSize(12).TextColor(c.Theme().Success)
		c.After(4e9)
	}
}

// Stat is a number with a caption, for summaries.
func Stat(c *ui.Context, value, caption string, col ui.Color) ui.Element {
	t := c.Theme()
	return ui.Column(c).Grow(1).Basis(0).Gap(4).Padding(12, 14).Radius(14).Background(pal(t).Card).Children(func() {
		ui.Text(c, caption).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).FontSize(24).FontWeight(700).TextColor(col).FontFeatures("tnum").SingleLine()
	})
}
