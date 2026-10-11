package view

import (
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
)

// An instance's mark is a glyph on a tile of its color, which the user
// picks in the instance editor so that instances tell apart at a glance.
// The profile keeps the names below; an unknown or empty one is the
// default, the first of each list.

var instanceIcons = []struct {
	ID   string
	Icon *ui.SVG
}{
	{"server", icServer},
	{"cloud", icCloud},
	{"database", icDatabase},
	{"global", icGlobal},
	{"home", icHome},
	{"building", icBuilding},
	{"briefcase", icBriefcase},
	{"terminal", icTerminal},
	{"code", icCode},
	{"robot", icRobot},
	{"rocket", icRocket},
	{"planet", icPlanet},
	{"flask", icFlask},
	{"shield", cfgIcShield},
	{"flash", icFlash},
	{"fire", icFire},
	{"leaf", icLeaf},
	{"star", icStar},
	{"heart", icHeart},
	{"sun", icSun},
	{"moon", icMoon},
}

var instanceTints = []struct {
	ID   string
	Tint Tint
}{
	{"blue", TintBlue},
	{"indigo", TintIndigo},
	{"purple", TintPurple},
	{"pink", TintPink},
	{"red", TintRed},
	{"orange", TintOrange},
	{"yellow", TintYellow},
	{"green", TintGreen},
	{"teal", TintTeal},
	{"gray", TintGray},
}

func markIcon(id string) *ui.SVG {
	for _, m := range instanceIcons {
		if m.ID == id {
			return m.Icon
		}
	}
	return instanceIcons[0].Icon
}

func markTint(id string) Tint {
	for _, m := range instanceTints {
		if m.ID == id {
			return m.Tint
		}
	}
	return instanceTints[0].Tint
}

// instanceMark is the instance's glyph, white on a tile of its color.
func instanceMark(c *ui.Context, p store.InstanceProfile, size float32) ui.Element {
	return markTile(c, p.Icon, p.Color, size)
}

func markTile(c *ui.Context, icon, color string, size float32) ui.Element {
	return Tile(c, markIcon(icon), markTint(color), size, true)
}

// instanceTile is the mark with a dot at its corner for the connection;
// ring is the color behind it, which sets the dot apart from the tile.
func instanceTile(c *ui.Context, s core.InstanceSnapshot, size float32, ring ui.Color) ui.Element {
	dot := max(8, size*0.3)
	return ui.Column(c).Size(size, size).Children(func() {
		instanceMark(c, s.Profile, size)
		ui.Box(c).Absolute().Right(-2).Bottom(-2).Size(dot, dot).Radius(dot/2).
			Background(ConnColor(c.Theme(), s.Conn)).Border(2, ring).
			Label(ConnLabel(s.Conn))
	})
}

// markPicker chooses the glyph and the color of a mark.
func markPicker(c *ui.Context, icon, color *string) {
	t := c.Theme()
	k := markTint(*color)
	ui.Column(c).Gap(10).Children(func() {
		ui.Row(c).Gap(6).Wrap().Children(func() {
			for i, m := range instanceIcons {
				on := m.ID == *icon || (i == 0 && !knownIcon(*icon))
				b := ui.ButtonBase(c.Key("icon-"+m.ID)).Size(32, 32).Radius(9).Justify(ui.Center).AlignItems(ui.Center).
					Label(m.ID).Tooltip(m.ID).Selected(on)
				fg := t.TextMuted
				switch {
				case on:
					b.Background(k.C(t))
					fg = ui.RGB(255, 255, 255)
				case b.Hovered():
					b.Background(pal(t).Hover)
					fg = t.Text
				default:
					b.Background(pal(t).Inset)
				}
				b.Children(func() {
					ui.Icon(c, m.Icon).Size(18, 18).TextColor(fg)
				})
				if b.Clicked() {
					*icon = m.ID
				}
			}
		})
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			for i, m := range instanceTints {
				on := m.ID == *color || (i == 0 && !knownTint(*color))
				b := ui.ButtonBase(c.Key("color-"+m.ID)).Size(22, 22).Radius(11).
					Label(m.ID).Tooltip(m.ID).Selected(on)
				// After Selected, which would fill it with the accent.
				b.Background(m.Tint.C(t))
				if on {
					b.Border(2, t.Text)
				}
				if b.Clicked() {
					*color = m.ID
				}
			}
		})
	})
}

func knownIcon(id string) bool {
	for _, m := range instanceIcons {
		if m.ID == id {
			return true
		}
	}
	return false
}

func knownTint(id string) bool {
	for _, m := range instanceTints {
		if m.ID == id {
			return true
		}
	}
	return false
}

// instanceSelect chooses the selected instance from a popup listing every
// instance with its mark, and an item at the end that opens the Instances
// page. trigger draws the closed select for the selected snapshot; below
// opens the popup under it rather than above.
func (a *App) instanceSelect(c *ui.Context, key string, snaps []core.InstanceSnapshot, below bool, trigger func(sel ui.Element, cur core.InstanceSnapshot)) {
	t := c.Theme()
	choice := a.selected
	sel := ui.SelectBase(c.Key(key), &choice)
	var cur core.InstanceSnapshot
	for _, s := range snaps {
		if s.Profile.ID == a.selected {
			cur = s
		}
	}
	sel.Trigger.Label(T("当前实例", "Instance"))
	trigger(sel.Trigger, cur)
	sel.Popup(func(panel ui.Element) {
		if below {
			panel.Margin(6, 0, 0, 0)
		} else {
			panel.Margin(0, 0, 6, 0)
		}
		panel.Padding(5).Gap(1).Radius(12).MinWidth(sidebarWidth-20).Background(t.Background).
			Border(1, pal(t).Line).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18))
		for _, s := range snaps {
			item := sel.Item(s.Profile.ID).Padding(6, 8).Gap(8).Radius(7).AlignItems(ui.Center)
			fg, bg := t.Text, t.Background
			if item.Highlighted() {
				fg, bg = t.AccentText, t.Accent
				item.Background(bg)
			}
			item.Children(func() {
				instanceTile(c, s, 20, bg)
				ui.Text(c, s.Profile.Name).FontSize(13).TextColor(fg).SingleLine().Grow(1)
				ui.Text(c, ConnLabel(s.Conn)).FontSize(11).TextColor(fg.Alpha(0.7)).SingleLine()
			})
		}
		ui.Box(c).Height(1).Margin(4, 6).Background(pal(t).Line)
		item := sel.Item(manageInstances).Padding(6, 8).Gap(8).Radius(7).AlignItems(ui.Center)
		fg := t.Text
		if item.Highlighted() {
			item.Background(t.Accent)
			fg = t.AccentText
		}
		item.Children(func() {
			ui.Row(c).Size(20, 20).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icGear).FontSize(14).TextColor(fg)
			})
			ui.Text(c, T("管理实例…", "Manage Instances…")).FontSize(13).TextColor(fg)
		})
	})
	// Changed applies the choice to the local now; unasked, mygo would
	// apply it after the frame, when nothing reads it.
	if !sel.Trigger.Changed() {
		return
	}
	switch {
	case choice == manageInstances:
		a.page = "instances"
	case choice != a.selected:
		a.selected = choice
		a.syncActive()
	}
}
