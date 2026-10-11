package view

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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
