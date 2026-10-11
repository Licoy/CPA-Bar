package view

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpamp/cpamptest"
)

// The editor's picker sets the mark the instance is saved with, and an
// unknown mark falls back to the default.
func TestInstanceMarkPicker(t *testing.T) {
	a, id := newTestApp(t, true)
	a.page = "instances"
	st := StateFor(a, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
	settings := a.Svc.Settings()
	p := settings.Instance(id)
	st.editor = instanceEditor{open: true, id: id, name: p.Name, address: p.BaseURL, flavor: flavorOfficial}
	q := installTestRun(t)
	tt := ui.NewTester(a.managerView, managerWidth, 1200)
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "instance-editor.png"), tt)
	}
	for _, label := range []string{"rocket", "green"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if st.editor.icon != "rocket" || st.editor.color != "green" {
		t.Fatalf("picked %q %q", st.editor.icon, st.editor.color)
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the save", func() bool { return !st.editor.open })
	settings = a.Svc.Settings()
	if p := settings.Instance(id); p.Icon != "rocket" || p.Color != "green" {
		t.Errorf("saved mark %q %q", p.Icon, p.Color)
	}
	if markIcon("nope") != icServer || markTint("") != TintBlue || markIcon("rocket") != icRocket {
		t.Error("marks do not fall back to the default")
	}
}

// The header of an instance page switches the instance; other pages have
// no switcher.
func TestHeaderInstanceSelect(t *testing.T) {
	a, first := newTestApp(t, true)
	srv := fakeCPA(t)
	second, err := a.Svc.AddInstance(context.Background(), core.InstanceInput{Name: "Office", Address: srv.URL, Key: testKey, Icon: "rocket", Color: "pink"})
	if err != nil {
		t.Fatal(err)
	}
	a.page = "overview"
	a.selected = first
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("当前实例"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Office"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != second {
		t.Errorf("selected %q, want %q", a.selected, second)
	}
}

// Adding with detection: the address of CPA-Manager-Plus switches the
// editor to it, fills in the CPA it collects from and moves the admin key;
// the CPA key then completes it.
func TestEditorDetectsManagerPlus(t *testing.T) {
	a, _ := newTestApp(t, false)
	cpaSrv := fakeCPA(t)
	mp := cpamptest.New(t, "cpamp_admin")
	mp.CPAAddress = cpaSrv.URL
	a.page = "instances"
	st := StateFor(a, "instances", "", func() *instancesState { return &instancesState{tests: map[string]*Task{}} })
	st.editor = instanceEditor{open: true, flavor: flavorAuto, address: mp.URL, key: "cpamp_admin"}
	q := installTestRun(t)
	tt := ui.NewTester(a.managerView, managerWidth, 1200)
	wait := func() { waitFor(t, tt, q, "the save", func() bool { return !st.editor.save.Running }) }
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	wait()
	e := &st.editor
	if !e.open || e.flavor != flavorPlus || e.managerURL != mp.URL || e.managerKey != "cpamp_admin" || e.key != "" || e.address != cpaSrv.URL || e.notice == "" || e.save.Err != nil {
		t.Fatalf("editor after detection: %+v", *e)
	}
	if !tt.HasText("管理员密钥") {
		t.Errorf("no admin key field; texts %q", tt.Texts())
	}
	e.key = testKey
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	wait()
	if e.open || e.save.Err != nil {
		t.Fatalf("not added: %v", e.save.Err)
	}
	settings := a.Svc.Settings()
	if len(settings.Instances) != 1 || !settings.Instances[0].ManagerPlus() {
		t.Errorf("instances %+v", settings.Instances)
	}
	if c, err := a.Svc.ManagerPlus(settings.Instances[0].ID); c == nil || err != nil {
		t.Errorf("no Manager-Plus client: %v", err)
	}
}
