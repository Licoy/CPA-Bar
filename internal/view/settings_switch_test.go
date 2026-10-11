package view

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestSettingsSwitchThemeAndLanguage(t *testing.T) {
	defer SetLanguage(store.LanguageZH)
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("深色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.Svc.Settings().Appearance; got != store.AppearanceDark {
		t.Errorf("appearance = %q, want dark", got)
	}
	if err := tt.Click("English"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.Svc.Settings().Language; got != store.LanguageEN {
		t.Errorf("language = %q, want en", got)
	}
	if !tt.HasText("Appearance") {
		t.Errorf("the page is not in English; texts: %q", tt.Texts())
	}
}
