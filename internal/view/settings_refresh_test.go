package view

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
)

func enterQuotaRefresh(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	row, ok1 := tt.Find(label)
	edge, ok2 := tt.Find("English")
	if !ok1 || !ok2 {
		t.Fatalf("missing quota interval: %q", tt.Texts())
	}
	tt.ClickAt(edge.X+edge.W-30, row.Y+row.H)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(value)
	tt.Frame()
}

func TestSettingsQuotaRefreshBothLanguages(t *testing.T) {
	defer SetLanguage(store.LanguageZH)
	_ = SetLanguage(store.LanguageZH)
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := quotaSettingsTester(a)
	if a.Svc.Settings().QuotaRefreshMinutes != 5 {
		t.Fatal("initial quota interval is not five minutes")
	}
	enterQuotaRefresh(t, tt, "额度刷新间隔（分钟）", "17")
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().QuotaRefreshMinutes != 17 {
		t.Fatal("Chinese quota interval was not saved")
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "settings-quota-refresh.png"), tt)
	}
	if err := tt.Click("English"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	enterQuotaRefresh(t, tt, "Quota refresh interval (min)", "9")
	if err := tt.Click("Apply"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().QuotaRefreshMinutes != 9 {
		t.Fatal("English quota interval was not saved")
	}
	// Passive updates preserve an editable preset for the next active read.
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.PassiveQuota = true }); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	enterQuotaRefresh(t, tt, "Quota refresh interval (min)", "11")
	if err := tt.Click("Apply"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().QuotaRefreshMinutes != 11 || !a.Svc.Settings().PassiveQuota {
		t.Fatal("passive preset did not save independently")
	}
}

func TestSettingsQuotaRefreshRejectsInvalidInput(t *testing.T) {
	_ = SetLanguage(store.LanguageZH)
	for _, value := range []string{"0", "-1", "1.5", "not-a-number", fmt.Sprint(store.MaxQuotaRefreshMinutes + 1), "999999999999999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			a, _ := newTestApp(t, false)
			a.page = "settings"
			tt := quotaSettingsTester(a)
			enterQuotaRefresh(t, tt, "额度刷新间隔（分钟）", value)
			if err := tt.Click("应用"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.Svc.Settings().QuotaRefreshMinutes != 5 {
				t.Fatal("invalid input changed saved interval")
			}
			if !tt.HasText("请输入有效的整数分钟数") {
				t.Fatalf("validation error missing: %q", tt.Texts())
			}
		})
	}
}

func TestSettingsQuotaRefreshSaveFailureKeepsOriginal(t *testing.T) {
	_ = SetLanguage(store.LanguageZH)
	svc, err := core.New(core.Options{Settings: failingSettings{store.Defaults()}, Secrets: &store.MemorySecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	a := NewApp(svc)
	a.page = "settings"
	tt := quotaSettingsTester(a)
	enterQuotaRefresh(t, tt, "额度刷新间隔（分钟）", "17")
	if err := tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().QuotaRefreshMinutes != 5 {
		t.Fatal("failed save changed interval")
	}
	if !tt.HasText("save settings: disk full") {
		t.Fatalf("save failure hidden: %q", tt.Texts())
	}
}

func quotaSettingsTester(a *App) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(24).Children(func() { settingsPage(c, &PageEnv{App: a}) })
	}, managerWidth, 1800)
}
