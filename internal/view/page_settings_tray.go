package view

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/quota"
	"github.com/licoy/cpa-bar/internal/store"
)

// trayStyleSettings is the menu bar group: a preview of the current
// settings, the styles, then the options of the chosen one.
func trayStyleSettings(c *ui.Context, a *App, cur store.Settings, save func(func(*store.Settings)) bool) {
	t := c.Theme()
	quotaStyle := cur.TrayStyle != store.TrayStyleIcon
	settingsGroup(c, icGauge, TintTeal, T("菜单栏", "Menu bar"), func(row func(label, detail string, control func())) {
		ui.Column(c).Gap(6).Padding(4, 0, 8, 0).Children(func() {
			trayPreview(c, cur)
			if quotaStyle {
				ui.Text(c, T("示例额度，按当前设置显示；菜单栏显示实际额度", "Sample quotas in the current settings; the menu bar shows your own")).
					FontSize(11).TextColor(t.TextMuted)
			}
		})
		ui.Row(c).Gap(10).Padding(4, 0, 12, 0).Children(func() {
			for _, style := range store.TrayStyles {
				if trayStyleTile(c, cur, style).Clicked() && style != cur.TrayStyle {
					save(func(s *store.Settings) { s.TrayStyle = style })
					a.changed()
				}
			}
		})
		set := func(fn func(*store.Settings)) {
			if save(fn) {
				a.changed()
			}
		}
		switch cur.TrayStyle {
		case store.TrayStyleIcon:
			row(T("显示在线实例数", "Show instances online"), T("在图标旁显示“3/4”（在线实例/实例总数）", "Show “3/4” (online/all instances) next to the icon"), func() {
				on := cur.TrayText
				if ui.Switch(c.Key("traytext"), &on).Label(T("显示在线实例数", "Show instances online")).Changed() {
					set(func(s *store.Settings) { s.TrayText = on })
				}
			})
			return
		case store.TrayStyleMeters:
			row(T("额度条右侧显示", "Quota-bar detail"), "", func() {
				details := []string{store.TrayDetailPercent, store.TrayDetailReset, store.TrayDetailBoth}
				i := max(0, indexOf(details, cur.TrayMetersDetail))
				if PillTabs(c.Key("tray-meters"), &i, T("剩余百分比", "Remaining"), T("重置时间", "Reset time"), T("两者都显示", "Both")) {
					v := details[i]
					set(func(s *store.Settings) { s.TrayMetersDetail = v })
				}
			})
		case store.TrayStyleBattery:
			row(T("电池方向", "Battery orientation"), T("横向：百分比显示在电池内，详情在旁；竖向：电池竖立，文字显示在旁", "Horizontal puts the percentage inside the battery and the detail beside it; vertical stands the batteries up beside the text"), func() {
				i := pick(cur.TrayBatteryVertical, 1, 0)
				if PillTabs(c.Key("tray-battery"), &i, T("横向", "Horizontal"), T("竖向", "Vertical")) {
					v := i == 1
					set(func(s *store.Settings) { s.TrayBatteryVertical = v })
				}
			})
			row(T("电池旁显示", "Text beside battery"), "", func() {
				details := []string{store.TrayBatteryCountdown, store.TrayBatteryPercent}
				i := max(0, indexOf(details, cur.TrayBatteryDetail))
				if PillTabs(c.Key("tray-bdetail"), &i, T("刷新倒计时", "Countdown"), T("剩余百分比", "Remaining")) {
					v := details[i]
					set(func(s *store.Settings) { s.TrayBatteryDetail = v })
				}
			})
		case store.TrayStyleRings:
			row(T("数值置于圆环内", "Values inside rings"), T("每个周期一个圆环，剩余百分比显示在圆环中", "A ring for each window, with the percentage left inside"), func() {
				on := cur.TrayRingValues
				if ui.Switch(c.Key("tray-ring-values"), &on).Label(T("数值置于圆环内", "Values inside rings")).Changed() {
					set(func(s *store.Settings) { s.TrayRingValues = on })
				}
			})
		}
		if quotaStyle {
			row(T("额度周期", "Quota window"), T("自动：单独显示的账号显示两个主要周期，合并显示时各显示短周期；没有所选周期的账号显示其另一个周期。短周期多为 5 小时，长周期为每周或每月", "Automatic shows both main windows of an account on its own and the short one of stacked accounts; an account without the chosen window shows its other one. The short window is mostly 5 hours, the long one weekly or monthly"), func() {
				windows := []string{store.TrayWindowAuto, store.TrayWindowShort, store.TrayWindowLong}
				i := max(0, indexOf(windows, cur.TrayWindow))
				if PillTabs(c.Key("tray-window"), &i, T("自动", "Automatic"), T("短周期", "Short"), T("长周期", "Long")) {
					v := windows[i]
					set(func(s *store.Settings) { s.TrayWindow = v })
				}
			})
			if store.TrayStyleStacks(cur.TrayStyle) {
				row(T("合并显示两个账号", "Stack two accounts"), T("两个账号上下叠放在同一组件中", "Stacks the two accounts in one component, one over the other"), func() {
					on := cur.TrayStack
					if ui.Switch(c.Key("tray-stack"), &on).Label(T("合并显示两个账号", "Stack two accounts")).Changed() {
						set(func(s *store.Settings) { s.TrayStack = on })
					}
				})
			}
			row(T("显示服务商图标", "Show provider icons"), T("关闭后，叠放的行以周期名称或顺序区分", "Without icons, stacked rows are told apart by window name or order"), func() {
				on := !cur.TrayHideIcons
				if ui.Switch(c.Key("tray-icons"), &on).Label(T("显示服务商图标", "Show provider icons")).Changed() {
					set(func(s *store.Settings) { s.TrayHideIcons = !on })
				}
			})
			trayAccountsRow(c, a, row)
		}
	})
}

// trayAccountsRow says which instance and accounts the quota styles show.
func trayAccountsRow(c *ui.Context, a *App, row func(label, detail string, control func())) {
	s, ok := a.trayInstance()
	detail := ""
	var creds []cpa.Credential
	switch {
	case !ok:
		detail = T("还没有实例。", "No instances yet.")
	case s.Profile.Paused:
		detail = fmt.Sprintf(T("实例“%s”已暂停，菜单栏显示图标。", "Instance “%s” is paused; the menu bar shows the icon."), s.Profile.Name)
	default:
		creds = trayAccounts(s, trayMaxAccounts)
		detail = fmt.Sprintf(T("实例：%s。取默认实例（未设置时为第一个实例）的前 %d 个账号，置顶的优先，跳过已停用和不支持额度的账号", "Instance: %s. The first %d accounts of the default instance (else the first one), pinned ones first, skipping disabled ones and those without a quota"), s.Profile.Name, trayMaxAccounts)
		if len(creds) == 0 {
			detail += T("。没有可显示额度的账号，菜单栏显示图标。", ". No account has a quota to show; the menu bar shows the icon.")
		}
	}
	row(T("显示的账号", "Accounts shown"), detail, func() {
		if len(creds) == 0 {
			return
		}
		t := c.Theme()
		ui.Column(c).Gap(6).Children(func() {
			for _, cr := range creds {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					credTile(c.Key("tray-"+credKeyOf(cr)), cr, 22, pal(t).Card)
					ui.Text(c, PrivName(cr.DisplayName())).FontSize(12).SingleLine()
					if s.Profile.Pinned(credFileName(cr)) {
						ui.Icon(c, icPinned).FontSize(11).TextColor(t.Accent).Label(T("已置顶", "Pinned")).Tooltip(T("已置顶", "Pinned"))
					}
				})
			}
		})
	})
}

// trayPreview draws the menu bar of the settings, over sample quotas, on
// a strip of menu bar.
func trayPreview(c *ui.Context, s store.Settings) {
	t := c.Theme()
	ui.Box(c).Height(72).Draw(func(p *ui.Painter, rect ui.Rect) {
		if s.TrayStyle == store.TrayStyleIcon {
			scale := float32(2)
			bar := ui.Rect{X: rect.X, Y: rect.Y + (rect.H-(trayContentHeight+6)*scale)/2, W: rect.W, H: (trayContentHeight + 6) * scale}
			drawTrayIconStrip(p, t, bar, s, scale)
			return
		}
		r := traySample(s, 2)
		scale := min(2, (rect.W-24)/trayContentWidth(r))
		bar := ui.Rect{X: rect.X, Y: rect.Y + (rect.H-(trayContentHeight+6)*scale)/2, W: rect.W, H: (trayContentHeight + 6) * scale}
		drawTrayStrip(p, t, bar, r, scale)
	})
}

// trayStripColors are a strip of menu bar and its ink in the theme.
func trayStripColors(t *ui.Theme) (fill, ink ui.Color) {
	if t.Dark {
		return ui.RGBA(255, 255, 255, 0.07), ui.RGB(255, 255, 255)
	}
	return ui.RGBA(0, 0, 0, 0.05), ui.RGB(0, 0, 0)
}

// drawTrayStrip draws r centered on a strip of menu bar.
func drawTrayStrip(p *ui.Painter, t *ui.Theme, bar ui.Rect, r trayRender, scale float32) {
	fill, ink := trayStripColors(t)
	p.Fill(bar, fill, 7)
	w := trayContentWidth(r)
	x, y := bar.X+(bar.W-w*scale)/2, bar.Y+(bar.H-trayContentHeight*scale)/2
	p.Transform(ui.Scaling(scale, scale).Then(ui.Translation(x, y)), 0, 0, func() {
		// As the menu bar composes them: the colors over the ink.
		drawTrayContent(p, r, trayPaint{ink: ink})
		drawTrayContent(p, r, trayPaint{ink: ui.Transparent, colorOpacity: 1})
	})
}

// drawTrayIconStrip draws the tray icon of s, with "3/4" when it shows
// the instances online, centered on a strip of menu bar.
func drawTrayIconStrip(p *ui.Painter, t *ui.Theme, bar ui.Rect, s store.Settings, scale float32) {
	fill, ink := trayStripColors(t)
	p.Fill(bar, fill, 7)
	const logo, gap = 18, 4
	f := trayFont(13*scale, 500)
	w := float32(logo) * scale
	if s.TrayText {
		w += gap*scale + trayTextWidth("3/4", f)
	}
	x, cy := bar.X+(bar.W-w)/2, bar.Y+bar.H/2
	if bm := trayPreviewLogo(s.TrayIconColor, t.Dark); bm != nil {
		p.Image(bm, ui.Rect{X: x, Y: cy - logo*scale/2, W: logo * scale, H: logo * scale}, ui.Contain)
	}
	if s.TrayText {
		m := f.Metrics()
		p.Glyphs(ui.Shape("3/4", f), x+(logo+gap)*scale, cy-(m.Ascent+m.Descent)/2+m.Ascent, ink)
	}
}

// trayPreviewLogos are the tray icon as the menu bar shows it: in color,
// or mono on a light and on a dark bar.
var trayPreviewLogos = map[[2]bool]*ui.Bitmap{}

func trayPreviewLogo(color, dark bool) *ui.Bitmap {
	key := [2]bool{color, dark && !color}
	if bm, ok := trayPreviewLogos[key]; ok {
		return bm
	}
	src := trayMono36
	if color {
		src = trayColor36
	}
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	if key[1] {
		for i := 0; i < len(out.Pix); i += 4 {
			out.Pix[i], out.Pix[i+1], out.Pix[i+2] = 255, 255, 255
		}
	}
	bm := ui.NewBitmap(out)
	trayPreviewLogos[key] = bm
	return bm
}

const trayStyleTileHeight = 92

// trayStyleTile is a choice of style, previewing it with one account.
func trayStyleTile(c *ui.Context, s store.Settings, style string) ui.Element {
	t := c.Theme()
	name := trayStyleName(style)
	chosen := s.TrayStyle == style
	s.TrayStyle = style
	r := traySample(s, 1)
	b := ui.ButtonBase(c.Key("tray-style-" + style)).Label(name).Tooltip(name).
		Grow(1).Basis(0).MinWidth(0).Height(trayStyleTileHeight).Cursor(ui.CursorPointer)
	// The tile draws its own chosen look: a frame, not the selected fill.
	b.Role(ui.RoleRadio).Selected(chosen).Background(ui.Transparent)
	hovered := b.Hovered()
	return b.Draw(func(p *ui.Painter, rect ui.Rect) {
		border, width := pal(t).Line, float32(1)
		if chosen {
			border, width = t.Accent, 2
			p.Fill(rect, t.Accent.Alpha(0.08), 10)
		} else if hovered {
			p.Fill(rect, pal(t).Hover, 10)
		}
		p.Stroke(insetRect(rect, width/2), border, 10, width)
		const labelH = 26
		bar := ui.Rect{X: rect.X + 8, Y: rect.Y + 10, W: rect.W - 16, H: rect.H - 10 - labelH}
		if style == store.TrayStyleIcon {
			scale := float32(1.25)
			bar.H = min(bar.H, (trayContentHeight+6)*scale)
			bar.Y = rect.Y + 10 + (rect.H-10-labelH-bar.H)/2
			drawTrayIconStrip(p, t, bar, s, scale)
		} else {
			scale := min(1.25, (bar.W-8)/trayContentWidth(r))
			bar.H = min(bar.H, (trayContentHeight+6)*scale)
			bar.Y = rect.Y + 10 + (rect.H-10-labelH-bar.H)/2
			drawTrayStrip(p, t, bar, r, scale)
		}
		col := t.TextMuted
		if chosen {
			col = t.Text
		}
		f := ui.Font{Size: 12, Weight: 500}
		label := trayFit(name, f, rect.W-16)
		tw := trayTextWidth(label, f)
		m := f.Metrics()
		p.Glyphs(ui.Shape(label, f), rect.X+(rect.W-tw)/2, rect.Y+rect.H-labelH/2-2-(m.Ascent+m.Descent)/2+m.Ascent, col)
	})
}

// trayStyleName names a menu bar style.
func trayStyleName(style string) string {
	switch style {
	case store.TrayStyleMeters:
		return T("额度条", "Quota bars")
	case store.TrayStyleCountdown:
		return T("倒计时", "Countdown")
	case store.TrayStyleBattery:
		return T("电池", "Battery")
	case store.TrayStyleRings:
		return T("圆环进度", "Rings")
	case store.TrayStyleText:
		return T("进度文本", "Text")
	}
	return T("图标", "Icon")
}

// traySample is the menu bar of s over made-up quotas of n accounts.
func traySample(s store.Settings, n int) trayRender {
	now := time.Now()
	window := func(zh, en string, left float64, reset time.Duration) *quota.Window {
		return &quota.Window{Label: quota.Text{ZH: zh, EN: en}, Remaining: left, Known: true, ResetAt: now.Add(reset)}
	}
	accts := []trayAccount{
		{provider: "codex", short: window("5 小时限额", "5-hour limit", 0.73, 2*time.Hour+15*time.Minute), long: window("周限额", "Weekly limit", 0.41, 77*time.Hour)},
		{provider: "claude", short: window("5 小时限额", "5-hour limit", 0.18, 48*time.Minute), long: window("周限额", "Weekly limit", 0.64, 120*time.Hour)},
	}
	return buildTray(s, accts[:n], now)
}
