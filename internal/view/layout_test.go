package view

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPanelCheckTimeCenteredInInstanceRow(t *testing.T) {
	a, id := newTestApp(t, true)
	snap, _ := a.Svc.Snapshot(id)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		var checked string
		for _, label := range tt.Texts() {
			if strings.HasPrefix(label, "检查于 ") {
				checked = label
				break
			}
		}
		stamp, foundStamp := tt.Find(checked)
		row, foundRow := tt.Find(snap.Profile.Name)
		if checked == "" || !foundStamp || !foundRow {
			t.Fatalf("missing check time or instance row: %q", tt.Texts())
		}
		// The status pill is a direct child of this centered row.
		if delta := stamp.Y + stamp.H/2 - (row.Y + row.H/2); delta < -1 || delta > 1 {
			t.Errorf("dark=%v: check time is %v DIPs from the instance row center", dark, delta)
		}
	}
}

// The live log takes the height the window leaves, as the window is
// resized: its card, and the message centered in it, follow the window.
func TestPageFillsResizedWindow(t *testing.T) {
	a, id := newTestApp(t, true)
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{} })
	st.paused, st.loaded = true, true
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)

	at := func(h int) float32 {
		t.Helper()
		tt.SetSize(managerWidth, h)
		tt.Frame()
		r, ok := tt.Find("暂无日志")
		if !ok {
			t.Fatalf("at height %d the live log shows no message; texts: %q", h, tt.Texts())
		}
		return r.Y + r.H/2
	}
	small, normal, large := at(600), at(managerHeight), at(1100)
	if normal-small < 60 || large-normal < 120 {
		t.Errorf("the message is at %v, %v and %v for heights 600, %d and 1100: the log does not follow the window", small, normal, large, managerHeight)
	}
	// Back to where it was, it is where it was.
	if again := at(managerHeight); again != normal {
		t.Errorf("at height %d again the message is at %v, was %v", managerHeight, again, normal)
	}
}

// Full screen hides the window controls (the title bar is zero): the
// sidebar then starts near the top instead of under their empty room.
func TestSidebarTopInFullScreen(t *testing.T) {
	a, _ := newTestApp(t, false)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 80})
	tt.Frame()
	windowed, ok := tt.Find("概览")
	if !ok {
		t.Fatal("no Overview item")
	}
	tt.SetTitleBar(ui.TitleBar{})
	tt.Frame()
	full, _ := tt.Find("概览")
	if windowed.Y < 52 || full.Y > 30 {
		t.Errorf("Overview at y=%v windowed, y=%v in full screen", windowed.Y, full.Y)
	}
}

func TestSidebarReservesSpaceOnlyForLeftControls(t *testing.T) {
	for _, tc := range []struct {
		name string
		bar  ui.TitleBar
	}{
		{"windows-right", ui.TitleBar{Height: 32, Right: 138}},
		{"linux-right", ui.TitleBar{Height: 46, Right: 120}},
		{"linux-left", ui.TitleBar{Height: 46, Left: 120}},
		{"linux-both", ui.TitleBar{Height: 60, Left: 40, Right: 80}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t, false)
			tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
			tt.SetTitleBar(tc.bar)
			tt.Frame()
			item, ok := tt.Find("概览")
			if !ok {
				t.Fatal("no Overview item")
			}
			if tc.bar.Left == 0 && item.Y > 30 {
				t.Errorf("sidebar leaves empty control space: Overview y=%v", item.Y)
			}
			if tc.bar.Left > 0 && item.Y < tc.bar.Height {
				t.Errorf("sidebar overlaps left controls: Overview y=%v, controls height=%v", item.Y, tc.bar.Height)
			}
		})
	}
}

func TestEmptyInstanceActionFollowsMessage(t *testing.T) {
	for _, panel := range []bool{false, true} {
		name := "overview"
		if panel {
			name = "panel"
		}
		t.Run(name, func(t *testing.T) {
			a, _ := newTestApp(t, false)
			a.page = "overview"
			view, width, height := a.managerView, managerWidth, managerHeight
			detail := "在“实例”页添加一个 CPA 服务后即可管理。"
			if panel {
				view, width, height = a.panelView, panelWidth, panelHeight
				detail = "添加一个 CPA 服务的地址和管理密钥，即可在这里查看状态。"
			}
			tt := ui.NewTester(view, width, height)
			for _, h := range []int{height, height + 300} {
				tt.SetSize(width, h)
				tt.Frame()
				message, foundMessage := tt.Find(detail)
				button, foundButton := tt.Find("添加实例")
				if !foundMessage || !foundButton {
					t.Fatalf("missing empty state message or action: %q", tt.Texts())
				}
				if gap := button.Y - (message.Y + message.H); gap < 8 || gap > 24 {
					t.Errorf("at height %d action gap=%v, want 8–24 DIPs", h, gap)
				}
			}
			if !panel {
				if err := tt.Click("添加实例"); err != nil {
					t.Fatal(err)
				}
				if a.page != "instances" {
					t.Errorf("Add Instance selected %q", a.page)
				}
			}
		})
	}
}
