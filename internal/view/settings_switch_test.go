package view

import (
	"errors"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
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

func TestSettingsSwitchPanelBackgroundInBothLanguages(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("panel background is a macOS setting")
	}
	defer SetLanguage(store.LanguageZH)
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("面板背景") {
		t.Fatalf("settings lack panel background: %q", tt.Texts())
	}
	if err := tt.Click("不透明"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.Svc.Settings().PanelOpaque {
		t.Error("opaque selection was not saved")
	}
	if err := tt.Click("English"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Panel background") {
		t.Fatalf("English settings lack panel background: %q", tt.Texts())
	}
	if err := tt.Click("Translucent"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().PanelOpaque {
		t.Error("translucent selection was not saved")
	}
}

type failingSettings struct{ settings store.Settings }

func (f failingSettings) Load() (store.Settings, error) { return f.settings, nil }
func (f failingSettings) Save(store.Settings) error     { return errors.New("disk full") }

func TestPanelBackgroundSaveFailureHasNoEffect(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("panel background is a macOS setting")
	}
	svc, err := core.New(core.Options{Settings: failingSettings{store.Defaults()}, Secrets: &store.MemorySecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	a := NewApp(svc)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("不透明"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().PanelOpaque {
		t.Error("failed save changed the panel background")
	}
	if !tt.HasText("save settings: disk full") {
		t.Fatalf("save failure is not visible: %q", tt.Texts())
	}
}

func TestUnsupportedOpaqueSettingIsVisibleAndCanBeCleared(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("panel background is a macOS setting")
	}
	original := panelOpaqueAvailable
	panelOpaqueAvailable = func() bool { return false }
	defer func() { panelOpaqueAvailable = original }()
	a, _ := newTestApp(t, false)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PanelOpaque = true }); err != nil {
		t.Fatal(err)
	}
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("当前使用半透明；不透明模式需要 macOS 14 或更新版本") {
		t.Fatalf("unsupported mode is not explained: %q", tt.Texts())
	}
	_ = tt.Click("不透明")
	tt.Frame()
	if !a.Svc.Settings().PanelOpaque {
		t.Error("disabled opaque choice changed the saved setting")
	}
	if err := tt.Click("保存半透明"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().PanelOpaque {
		t.Error("old opaque setting was not cleared")
	}
}
