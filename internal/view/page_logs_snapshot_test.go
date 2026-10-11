package view

import (
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestLogsPluginsSnapshots draws the logs and plugins pages with data, to
// $CPABAR_SNAPSHOTS: go test ./internal/view -run LogsPluginsSnapshots.
func TestLogsPluginsSnapshots(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("CPABAR_SNAPSHOTS is not set")
	}
	lf, pf := logsFake(t), pluginsFake(t)
	f := &pageFake{plugin: "1"}
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		path := strings.TrimPrefix(r.URL.Path, "/v8/management")
		if path == "/observability/logs" && r.Method == http.MethodGet && r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"lines":["[2026-10-11 10:00:01] [info] server started on :8317","[2026-10-11 10:00:05] [warn] upstream slow: claude 2.4s","[2026-10-11 10:00:09] [error] request failed: 502 bad gateway","[2026-10-11 10:00:12] [info] token refreshed for ok@example.com"],"line-count":4,"next-cursor":"c1"}`))
			return true
		}
		return lf.extra(w, r, body) || pf.extra(w, r, body)
	}
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	save := func(name string, tt *ui.Tester) {
		out, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		mode := map[bool]string{false: "light", true: "dark"}[dark]
		StateFor(a, "plugins", id, func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} }).quota.open = false
		shots := []struct {
			name string
			page string
			prep func()
			wait string
		}{
			{"logs-live", "logs", func() {
				StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} }).tab = logsTabLive
			}, "server started"},
			{"logs-files", "logs", func() {
				StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} }).tab = logsTabFiles
			}, "error-v1-2026.log"},
			{"logs-diag", "logs", func() {
				StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} }).tab = logsTabDiag
			}, "API"},
			{"plugins", "plugins", func() {}, "Sample Plugin"},
			{"plugins-store", "plugins", func() {
				StateFor(a, "plugins", id, func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} }).tab = pluginsTabStore
			}, "Fancy Tool"},
		}
		for _, s := range shots {
			a.page = s.page
			s.prep()
			tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
			tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 80})
			tt.SetDark(dark)
			tt.SetScale(2)
			pump(t, tt, q, 5*time.Second, s.name, func() bool { return tt.HasText(s.wait) || s.page == "logs" && liveHas(a, id, s.wait) })
			settle(tt, q, 100*time.Millisecond)
			save(s.name+"-"+mode, tt)
		}
		// The quota dialog.
		st := StateFor(a, "plugins", id, func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} })
		st.tab = pluginsTabInstalled
		st.quota.open = false
		a.page = "plugins"
		tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
		tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 80})
		tt.SetDark(dark)
		tt.SetScale(2)
		pump(t, tt, q, 5*time.Second, "plugins", func() bool { return tt.HasText("Sample Plugin") })
		if err := tt.Click("查看额度"); err != nil {
			t.Fatal(err)
		}
		pump(t, tt, q, 5*time.Second, "quota", func() bool { return tt.HasText(`"used": 12`) })
		save("plugins-quota-"+mode, tt)
	}
}
