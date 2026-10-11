package view

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSnapshots writes pictures of the windows to $CPABAR_SNAPSHOTS, to
// look at the design: go test ./internal/view -run Snapshots.
func TestSnapshots(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("CPABAR_SNAPSHOTS is not set")
	}
	a, id := newTestApp(t, true)
	// Some history for the charts.
	for range 40 {
		a.Svc.Refresh(id)
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			if s, _ := a.Svc.Snapshot(id); !s.Refreshing {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	save := func(name string, tt *ui.Tester) {
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		mode := map[bool]string{false: "light", true: "dark"}[dark]
		for _, p := range pages {
			a.page = p.ID
			tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
			tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 80})
			tt.SetDark(dark)
			tt.SetScale(2)
			tt.Frame()
			save("manager-"+p.ID+"-"+mode, tt)
		}
		tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
		tt.SetDark(dark)
		tt.SetScale(2)
		save("panel-all-"+mode, tt)
		a.panelInstance = a.selected
		tt = ui.NewTester(a.panelView, panelWidth, panelHeight)
		tt.SetDark(dark)
		tt.SetScale(2)
		save("panel-one-"+mode, tt)
		a.panelInstance = ""
	}
}

// saveSnapshot writes a picture of the tester's window to path.
func saveSnapshot(t *testing.T, path string, tt *ui.Tester) {
	t.Helper()
	tt.SetScale(2)
	tt.Frame()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
