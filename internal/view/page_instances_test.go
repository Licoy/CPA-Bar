package view

import (
	"testing"

	"github.com/egoist/mygo/ui"
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
