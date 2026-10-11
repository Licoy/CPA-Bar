package view

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
)

const testKey = "test-management-key"

// fakeCPA answers the management routes the views read.
func fakeCPA(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "8.0.23")
		switch strings.TrimPrefix(r.URL.Path, "/v8/management") {
		case "/config":
			_, _ = w.Write([]byte(`{"debug":false}`))
		case "/credentials":
			retry := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(`{"files":[
				{"id":"a.json","auth_index":"1","name":"a.json","provider":"claude","email":"ok@example.com","status":"active"},
				{"id":"b.json","auth_index":"2","name":"b.json","provider":"codex","email":"cool@example.com","status":"active","unavailable":true,"next_retry_after":"` + retry + `","status_message":"rate limited"},
				{"id":"c.json","auth_index":"3","name":"c.json","provider":"gemini","email":"off@example.com","status":"disabled","disabled":true}
			]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestApp(t *testing.T, withInstance bool) (*App, string) {
	t.Helper()
	svc, err := core.New(core.Options{
		Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)},
		Secrets:  &store.MemorySecrets{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	id := ""
	if withInstance {
		srv := fakeCPA(t)
		id, err = svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: srv.URL, Key: testKey})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if s, _ := svc.Snapshot(id); s.HasData || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	a := NewApp(svc)
	a.selected = id
	return a, id
}

func TestPanelAllInstances(t *testing.T) {
	a, _ := newTestApp(t, true)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	for _, s := range []string{"Home", "在线", "1/1"} {
		if !tt.HasText(s) {
			t.Errorf("panel lacks %q; texts: %q", s, tt.Texts())
		}
	}
}

func TestPanelOneInstance(t *testing.T) {
	a, id := newTestApp(t, true)
	a.panelInstance = id
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	for _, s := range []string{"cool@example.com", "冷却中", "ok@example.com", "复制地址"} {
		if !tt.HasText(s) {
			t.Errorf("panel lacks %q; texts: %q", s, tt.Texts())
		}
	}
	if err := tt.Click("复制地址"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); !strings.HasPrefix(got, "http://127.0.0.1:") || strings.Contains(got, "/v8/") {
		t.Errorf("copied %q, want the root address", got)
	}
}

func TestPanelEmpty(t *testing.T) {
	a, _ := newTestApp(t, false)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	if !tt.HasText("还没有实例") {
		t.Errorf("texts: %q", tt.Texts())
	}
}

// Every page renders, with and without an instance, without panicking.
func TestManagerPages(t *testing.T) {
	for _, with := range []bool{true, false} {
		a, _ := newTestApp(t, with)
		for _, p := range pages {
			a.page = p.ID
			tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
			if !tt.HasText(p.Title()) {
				t.Errorf("page %s (instance %v) lacks its title; texts: %q", p.ID, with, tt.Texts())
			}
			// The header names the target of an instance page, and nothing
			// under the title of the others.
			if n := strings.Count(strings.Join(tt.Texts(), "\n"), "Home"); p.Instance && with && n < 2 {
				t.Errorf("instance page %s does not show its target in the header and the sidebar", p.ID)
			}
			if p.ID != "about" && slices.Contains(tt.Texts(), "CPA-Bar") {
				t.Errorf("page %s shows CPA-Bar under its title", p.ID)
			}
		}
	}
}

func TestOverviewStaleNeverZero(t *testing.T) {
	if got := staleNumIf(0, core.InstanceSnapshot{}); got != "—" {
		t.Errorf("no data shows %q, want a dash", got)
	}
	if got := staleNumIf(3, core.InstanceSnapshot{HasData: true, Stale: true}); got != "3*" {
		t.Errorf("stale shows %q", got)
	}
}

func TestAddressWarning(t *testing.T) {
	for addr, warn := range map[string]bool{
		"http://127.0.0.1:8317":   false,
		"localhost:8317":          false,
		"https://cpa.example.com": false,
		"http://cpa.example.com":  true,
		"192.168.1.5:8317":        true,
		"":                        false,
	} {
		if got := addressWarning(addr) != ""; got != warn {
			t.Errorf("addressWarning(%q) = %v, want %v", addr, got, warn)
		}
	}
}

func TestMain(m *testing.M) {
	system.OpenAtLogin = func() bool { return false }
	system.SetOpenAtLogin = func(bool) error { return nil }
	system.NotificationsSupported = func() bool { return true }
	system.UpdatesEnabled = func() bool { return true }
	system.AutomaticUpdateChecks = func() bool { return true }
	system.SetAutomaticChecks = func(bool) {}
	system.CheckForUpdates = func() {}
	system.SetThemeSource = func(mygo.ThemeSource) {}
	os.Exit(m.Run())
}
