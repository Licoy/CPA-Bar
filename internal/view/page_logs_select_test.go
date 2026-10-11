package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// logsSelectApp is the live log page with lines already in its buffer and no
// fetch due, so that the test decides what the log holds.
func logsSelectApp(t *testing.T, n int) (*App, *logsState, *ui.Tester) {
	t.Helper()
	a, id := newPageApp(t, logsFake(t))
	a.page = "logs"
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} })
	st.nextAt = time.Now().Add(time.Hour)
	st.loaded = true
	logsAppend(st, 0, n)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	tt.Frame()
	return a, st, tt
}

// logsAppend adds the lines from..to-1 as a fetch would.
func logsAppend(st *logsState, from, to int) {
	for i := from; i < to; i++ {
		l := fmt.Sprintf("[2026-10-11 10:00:%02d] line %03d", i%60, i)
		if i%7 == 3 {
			l += " [error] boom"
		}
		st.lines = append(st.lines, l)
	}
	st.gen++
}

func logsWant(from, to int) string {
	var out []string
	for i := from; i < to; i++ {
		l := fmt.Sprintf("[2026-10-11 10:00:%02d] line %03d", i%60, i)
		if i%7 == 3 {
			l += " [error] boom"
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// logsAreaRect is the box of the live log's text area.
func logsAreaRect(t *testing.T, tt *ui.Tester) ui.Rect {
	t.Helper()
	r, ok := tt.Find("日志文本")
	if !ok {
		t.Fatalf("no log text area; texts: %q", tt.Texts())
	}
	return r
}

// TestLogsDragSelectsAcrossLines is the report "dragging over the live log
// does not select properly": every line used to be its own selectable text,
// so a drag stayed within the line it began in.
func TestLogsDragSelectsAcrossLines(t *testing.T) {
	_, st, tt := logsSelectApp(t, 5)
	r := logsAreaRect(t, tt)
	// From the first line down to below the last one: everything.
	tt.Press(r.X+2, r.Y+6)
	tt.Move(r.X+r.W/2, r.Y+r.H-4)
	tt.Release(r.X+r.W/2, r.Y+r.H-4)
	tt.Key(ui.Cmd, ui.KeyC)
	if got, want := tt.Clipboard(), logsWant(0, 5); got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
	_ = st
}

func TestLogsFollowAndScrollUp(t *testing.T) {
	_, st, tt := logsSelectApp(t, 200)
	sc := &st.doc.scroll
	if sc.MaxY <= 0 || sc.Y < sc.MaxY-logsFollowSlack {
		t.Fatalf("the log does not start at its end: y=%v max=%v", sc.Y, sc.MaxY)
	}
	// New lines keep the view at the end.
	for k := 0; k < 3; k++ {
		before := sc.MaxY
		logsAppend(st, 200+10*k, 210+10*k)
		tt.Frame()
		tt.Frame()
		if sc.MaxY <= before || sc.Y < sc.MaxY-logsFollowSlack {
			t.Fatalf("round %d: not following: y=%v max=%v (was %v)", k, sc.Y, sc.MaxY, before)
		}
	}
	// The user scrolls up: the view stays where it is.
	r := logsAreaRect(t, tt)
	tt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, -200)
	tt.Frame()
	up := sc.Y
	if up >= sc.MaxY-logsFollowSlack {
		t.Fatalf("scrolling did not leave the end: y=%v max=%v", sc.Y, sc.MaxY)
	}
	logsAppend(st, 230, 260)
	tt.Frame()
	tt.Frame()
	if sc.Y != up {
		t.Errorf("the view moved from %v to %v while the user was reading above", up, sc.Y)
	}
	// Back at the end it follows again.
	sc.Y = sc.MaxY
	tt.Frame()
	logsAppend(st, 260, 270)
	tt.Frame()
	tt.Frame()
	if sc.Y < sc.MaxY-logsFollowSlack {
		t.Errorf("not following after returning to the end: y=%v max=%v", sc.Y, sc.MaxY)
	}
}

// TestLogsSelectionSurvivesTailing drags past the top of the viewport (the
// area scrolls under the pointer), then lets new lines arrive: the text, the
// selection and the view stay put until the selection is let go.
func TestLogsSelectionSurvivesTailing(t *testing.T) {
	_, st, tt := logsSelectApp(t, 200)
	r := logsAreaRect(t, tt)
	tt.Press(r.X+2, r.Y+r.H-6) // the start of the last visible line
	tt.Move(r.X+r.W/2, r.Y-20) // above the area: it scrolls up under the pointer
	for range 30 {
		tt.Frame()
	}
	tt.Release(r.X+r.W/2, r.Y-20)
	tt.Key(ui.Cmd, ui.KeyC)
	got := tt.Clipboard()
	lines := strings.Split(got, "\n")
	if len(lines) < 60 {
		t.Fatalf("the drag selected only %d lines, scrolling under the pointer did not extend it", len(lines))
	}
	// Both ends are at line edges, and the lines between are whole and in a row.
	var first int
	if _, err := fmt.Sscanf(lines[1][strings.Index(lines[1], "line"):], "line %d", &first); err != nil {
		t.Fatalf("cannot read the first full line %q: %v", lines[1], err)
	}
	if want := logsWant(first, first+len(lines)-2); strings.Join(lines[1:len(lines)-1], "\n") != want {
		t.Fatalf("the copy is not %d whole lines from %d:\n%q", len(lines)-2, first, got)
	}
	if !st.doc.holding {
		t.Fatal("a selection is made but the text is not held")
	}
	y := st.doc.scroll.Y

	// New lines arrive while the selection is there.
	logsAppend(st, 200, 230)
	tt.Frame()
	tt.Frame()
	if st.doc.ver == st.visVer {
		t.Error("the text was replaced under a selection")
	}
	if st.doc.scroll.Y != y {
		t.Errorf("the view moved from %v to %v under a selection", y, st.doc.scroll.Y)
	}
	tt.SetClipboard("")
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != got {
		t.Errorf("the copy changed after new lines arrived: %d lines", len(strings.Split(tt.Clipboard(), "\n")))
	}

	// A click in the area lets the selection go, and the log catches up.
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
	if st.doc.holding || st.doc.ver != st.visVer {
		t.Errorf("the log did not catch up: holding=%v ver=%d/%d", st.doc.holding, st.doc.ver, st.visVer)
	}
	if !strings.HasSuffix(st.doc.text, logsWant(229, 230)) {
		t.Error("the text lacks the newest lines")
	}
}
