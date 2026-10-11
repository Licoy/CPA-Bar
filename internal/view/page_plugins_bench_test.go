package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

// pluginsStoreApp is the Store tab with n entries already loaded, as a real
// store has them: descriptions of a sentence or three in both scripts,
// authors, some installed.
func pluginsStoreApp(t testing.TB, n int) (*App, *pluginsState, *ui.Tester) {
	t.Helper()
	a, id := newPageApp(t, pluginsFake(t))
	a.page = "plugins"
	st := StateFor(a, "plugins", id, func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} })
	st.tab, st.started, st.storeStart = pluginsTabStore, true, true
	st.list = &cpa.PluginList{PluginsEnabled: true}
	st.store = &cpa.PluginStore{}
	for i := range n {
		e := pluginsStoreEntry{
			ID:          fmt.Sprintf("vendor-plugin-%03d", i),
			Name:        fmt.Sprintf("Plugin Number %03d", i),
			Description: strings.Repeat("为代理增加提供商额度、请求路由与用量报表，支持 OAuth 登录 and quota reports. ", 1+i%3),
			Author:      "Some Author",
			Version:     "1.2.3",
			Installed:   i%5 == 0,
		}
		e.UpdateAvailable = e.Installed && i%2 == 0
		st.entries = append(st.entries, e)
	}
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	tt.Frame()
	return a, st, tt
}

// scrollStore wheels over the store (the list of a long one, else the page)
// by dy DIPs.
func scrollStore(tt *ui.Tester, dy float32) { tt.Scroll(700, 400, 0, dy) }

// BenchmarkPluginsStoreScroll is the time of a wheel event on the Store tab,
// with the frame drawn. Before the long store was a list, it went from some
// 3 ms at 200 entries to over 100 ms from 500 on: the rows made more text
// layouts than the text system keeps, so that each frame shaped them again.
func BenchmarkPluginsStoreScroll(b *testing.B) {
	for _, n := range []int{50, 200, 500, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			_, _, tt := pluginsStoreApp(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				scrollStore(tt, pick(i/100%2 == 0, float32(30), -30))
				_ = tt.Image()
			}
		})
	}
}

// TestPluginsStoreLongListBuildsRowsInView checks that a store of a thousand
// entries builds a screenful of rows, and that scrolling reaches the others.
func TestPluginsStoreLongListBuildsRowsInView(t *testing.T) {
	const n = 1000
	_, st, tt := pluginsStoreApp(t, n)
	if !tt.HasText("Plugin Number 000") {
		t.Fatalf("the first entry is not shown; texts: %q", tt.Texts())
	}
	if tt.HasText("Plugin Number 500") || tt.HasText(fmt.Sprintf("Plugin Number %03d", n-1)) {
		t.Error("rows far from the view are built")
	}
	if got := len(tt.Texts()); got > 400 {
		t.Errorf("%d texts in a frame of a %d-entry store, want only the rows in view", got, n)
	}
	for i := 0; i < 400 && !tt.HasText("Plugin Number 100"); i++ {
		scrollStore(tt, 300)
	}
	if !tt.HasText("Plugin Number 100") {
		t.Fatalf("scrolling does not reach later entries; first row %v", st.storeList)
	}
	// A row of the list works as one of the page: Install asks first.
	if err := tt.Click("重新安装"); err != nil {
		if err := tt.Click("安装"); err != nil {
			t.Fatal(err)
		}
	}
	tt.Frame()
	if !st.installOpen {
		t.Errorf("the button of a listed row did nothing")
	}
}

// TestPluginsStoreFrameTime logs the time of the frames of stores of
// different lengths (run with -v).
func TestPluginsStoreFrameTime(t *testing.T) {
	for _, n := range []int{50, 200, 500, 1000} {
		_, _, tt := pluginsStoreApp(t, n)
		const frames = 20
		start := time.Now()
		for i := range frames {
			scrollStore(tt, pick(i < frames/2, float32(40), -40))
			_ = tt.Image()
		}
		t.Logf("store with %d entries: %v per wheel event with the frame drawn", n, time.Since(start)/frames)
	}
}
