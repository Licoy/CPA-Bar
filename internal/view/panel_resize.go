package view

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

// The panel's height follows the user: a grip on its edge away from the
// tray icon drags it taller or shorter, up to a share of the screen's
// height, and the panel opens at that height again.

const (
	// panelMinHeight is the least height the grip drags the panel to.
	panelMinHeight = 360
	// panelMaxShare is the most of its screen's height the panel takes.
	panelMaxShare = 0.8
	// panelGripHeight is the height of the grip along the edge.
	panelGripHeight = 8
)

// panelResize is the panel's height and the drag of its grip.
type panelResize struct {
	height int
	limit  int // the most the screen allows
	// up is set when the panel is above the tray icon, as over a taskbar
	// at the bottom of the screen: its top edge is the one to drag.
	up bool
	// centered is set when the panel is in the middle of the screen, as
	// on Linux, whose tray icons do not say where they are: it grows at
	// both edges.
	centered bool

	held   bool
	startY int // the pointer's y on the screen as the drag began
	startH int
}

// openPanelResize measures the screen of the tray icon and returns the
// height the panel opens at: the one the user chose, within the screen.
func (a *App) openPanelResize() int {
	r := panelResize{}
	var screen mygo.Rectangle
	if a.tray != nil {
		if b := a.tray.Bounds(); b.Width > 0 && b.Height > 0 {
			screen = mygo.Screen.DisplayMatching(b).Bounds
			r.up = b.Y+b.Height/2 > screen.Y+screen.Height/2
		}
	}
	if screen.Height == 0 {
		screen = mygo.Screen.PrimaryDisplay().Bounds
		r.centered = true
	}
	r.limit = panelHeightLimit(screen.Height)
	r.height = clampPanelHeight(a.Svc.Settings().PanelHeight, r.limit)
	a.panelResize = r
	return r.height
}

// panelHeightLimit is the most a screen of height screen allows the panel;
// 0 when the screen is not known.
func panelHeightLimit(screen int) int {
	if screen <= 0 {
		return 0
	}
	return int(float64(screen) * panelMaxShare)
}

// clampPanelHeight keeps a height between the least and the screen's
// limit (no limit when 0); 0 is the default height.
func clampPanelHeight(h, limit int) int {
	if h == 0 {
		h = panelHeight
	}
	h = max(h, panelMinHeight)
	if limit > 0 {
		h = min(h, limit)
	}
	return h
}

// panelGrip is the grip that drags the panel's height: along the bottom
// edge, or the top one when the panel is above the tray icon.
func (a *App) panelGrip(c *ui.Context) {
	t := c.Theme()
	r := &a.panelResize
	g := ui.Box(c.Key("panel-grip")).Absolute().Left(0).Right(0).Height(panelGripHeight).Cursor(ui.CursorResizeNS).
		Tooltip(T("拖动以调整面板高度", "Drag to resize the panel"))
	if r.up {
		g.Top(0)
	} else {
		g.Bottom(0)
	}
	_, _, held := g.Dragged()
	switch {
	case held && a.panel != nil:
		// The pointer's place on the screen, not in the panel, which
		// moves as it grows.
		y := mygo.Screen.CursorScreenPoint().Y
		if !r.held {
			r.held, r.startY, r.startH = true, y, r.height
			setPanelAnimates(a, false)
		}
		d := y - r.startY
		switch {
		case r.up:
			d = -d
		case r.centered:
			d *= 2
		}
		if h := clampPanelHeight(r.startH+d, r.limit); h != r.height {
			r.height = h
			a.panel.SetContentSize(panelWidth, h)
		}
	case r.held:
		r.held = false
		setPanelAnimates(a, true)
		h := r.height
		if h == r.startH {
			break
		}
		if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PanelHeight = h }); err != nil {
			log.Println("panel height:", err)
		}
	}
	hot := held || g.Hovered()
	g.Draw(func(p *ui.Painter, rc ui.Rect) {
		col := t.TextMuted.Alpha(0.35)
		if hot {
			col = t.TextMuted.Alpha(0.8)
		}
		const w, h = 36, 4
		p.Fill(ui.Rect{X: rc.X + (rc.W-w)/2, Y: rc.Y + (rc.H-h)/2, W: w, H: h}, col, h/2)
	})
}
