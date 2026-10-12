package view

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestInstancesPauseAndDeleteAsksFirst(t *testing.T) {
	a, id := newTestApp(t, true)
	a.page = "instances"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st := a.Svc.Settings(); st.Instance(id) == nil || !st.Instance(id).Paused {
		t.Fatal("pause did not pause the instance")
	}
	if !tt.HasText("恢复") {
		t.Errorf("a paused instance offers no resume; texts: %q", tt.Texts())
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st := a.Svc.Settings(); st.Instance(id) == nil {
		t.Fatal("delete removed the instance without asking")
	}
	if !tt.HasText("删除实例？") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st := a.Svc.Settings(); st.Instance(id) != nil {
		t.Fatal("confirming did not remove the instance")
	}
}

func TestInstanceEditorAdvancedSettings(t *testing.T) {
	a, _ := newTestApp(t, false)
	a.page = "instances"
	st := StateFor(a, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
	st.imports = []store.ImportedInstance{{
		Name: "Imported", BaseURL: "https://cpa.example.com/v8/management",
		CACertPEM: "PEM", ForwardAuthOnRedirect: true,
	}}
	tt := ui.NewTester(a.managerView, managerWidth, 1200)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	if !st.editor.advanced || !st.editor.forwardAuthOnRedirect || st.editor.ca != "PEM" {
		t.Fatalf("imported advanced settings not restored: %+v", st.editor)
	}
	if !tt.HasText("管理员密钥会发送到重定向目标，包括其他域名") || !tt.HasText("自定义 CA 证书") {
		t.Fatalf("advanced risk and CA fields are not visible: %q", tt.Texts())
	}
	if n := strings.Count(strings.Join(tt.Texts(), "\n"), "重定向时继续携带授权信息"); n < 2 {
		t.Fatalf("redirect switch lacks a visible label: %q", tt.Texts())
	}
	if err := tt.Click("高级设置"); err != nil {
		t.Fatal(err)
	}
	if st.editor.advanced || tt.HasText("包括其他域名") || tt.HasText("自定义 CA 证书") {
		t.Fatalf("advanced settings did not collapse: %q", tt.Texts())
	}
	if got := st.editor.input().CACertPEM; got != "PEM" {
		t.Errorf("collapsing advanced settings lost CA %q", got)
	}
	if !st.editor.input().ForwardAuthOnRedirect {
		t.Error("collapsing advanced settings lost redirect authorization")
	}
	if err := tt.Click("高级设置"); err != nil {
		t.Fatal(err)
	}
	if !st.editor.advanced || !tt.HasText("包括其他域名") || !tt.HasText("自定义 CA 证书") {
		t.Fatalf("advanced settings did not expand: %q", tt.Texts())
	}

	e := editorFor(store.InstanceProfile{CACertPEM: "OTHER", ForwardAuthOnRedirect: true})
	if !e.advanced || !e.input().ForwardAuthOnRedirect || e.input().CACertPEM != "OTHER" {
		t.Errorf("stored advanced settings not restored: %+v", e)
	}
	var empty instanceEditor
	if empty.input().ForwardAuthOnRedirect {
		t.Error("redirect authorization must default to off")
	}
}

func TestDeleteFailureKeepsViewState(t *testing.T) {
	secrets := &store.MemorySecrets{}
	svc, err := core.New(core.Options{
		Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)},
		Secrets:  secrets,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	srv := fakeCPA(t)
	id, err := svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: srv.URL, Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(svc)
	a.page = "instances"
	marker := &struct{}{}
	a.pageState["overview\x00"+id] = marker
	st := StateFor(a, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
	st.tests[id] = &Task{}
	secrets.Err = errors.New("keychain unavailable")

	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	settings := svc.Settings()
	if settings.Instance(id) == nil {
		t.Fatal("delete failure removed the instance")
	}
	if a.pageState["overview\x00"+id] != marker || st.tests[id] == nil {
		t.Error("delete failure cleared instance view state")
	}
	if st.op.Err == nil {
		t.Error("delete failure was not shown")
	}
}
