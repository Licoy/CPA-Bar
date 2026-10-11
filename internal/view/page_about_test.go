package view

import (
	"runtime"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestAboutPageShowsAppInfo(t *testing.T) {
	a, _ := newTestApp(t, false)
	a.page = "about"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	for _, want := range []string{"CPA-Bar", Version, runtime.GOOS + "/" + runtime.GOARCH, RepoURL, "打开仓库"} {
		if !tt.HasText(want) {
			t.Errorf("the About page shows no %q; texts: %q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"运行环境", "mygo", runtime.Version()} {
		if tt.HasText(gone) {
			t.Errorf("the About page still shows %q", gone)
		}
	}
	if RepoURL != "https://github.com/Licoy/CPA-Bar" {
		t.Errorf("RepoURL = %q", RepoURL)
	}
	if err := tt.Click("打开仓库"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.OpenedURLs(); !slices.Equal(got, []string{RepoURL}) {
		t.Errorf("opened %q, want the repository", got)
	}
	// The repository's line is a link.
	if err := tt.Click(RepoURL); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.OpenedURLs(); !slices.Equal(got, []string{RepoURL, RepoURL}) {
		t.Errorf("opened %q after the link", got)
	}
}

func TestAboutSectionIsLast(t *testing.T) {
	var ids []string
	for _, p := range pages {
		if p.Group == GroupAbout {
			ids = append(ids, p.ID)
		}
	}
	if !slices.Equal(ids, []string{"about", "bug"}) {
		t.Fatalf("About section = %q", ids)
	}
	if last := pages[len(pages)-1]; last.ID != "bug" {
		t.Errorf("last sidebar item = %q, want bug", last.ID)
	}
	if GroupAbout <= GroupApp {
		t.Error("the About group must come after App")
	}
	if p, ok := pageByID("settings"); !ok || p.Group != GroupApp {
		t.Errorf("settings left the App group: %+v", p)
	}
	if _, ok := pageByID("bug"); ok {
		t.Error("the bug link is a page")
	}
}

func TestBugItemOpensRepositoryWithoutSelecting(t *testing.T) {
	a, _ := newTestApp(t, false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	if err := tt.Click("BUG 反馈"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.OpenedURLs(); !slices.Equal(got, []string{RepoURL}) {
		t.Errorf("opened %q, want the repository home page", got)
	}
	if a.page != "settings" {
		t.Errorf("page = %q after clicking the link, want settings", a.page)
	}
	if !tt.HasText("Appearance") && !tt.HasText("外观") {
		t.Errorf("the page changed; texts: %q", tt.Texts())
	}
}

func TestBugItemInEnglish(t *testing.T) {
	defer SetLanguage(store.LanguageZH)
	a, _ := newTestApp(t, false)
	SetLanguage(store.LanguageEN)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	if !tt.HasText("Report a Bug") || !tt.HasText("About") {
		t.Fatalf("texts: %q", tt.Texts())
	}
}

func TestLinkNeverBecomesTheCurrentPage(t *testing.T) {
	a, _ := newTestApp(t, false)
	a.page = "overview"
	// OpenManager would create a window: the guard is pageByID, which it
	// and the manager view share.
	for _, id := range []string{"bug", "nope"} {
		if _, ok := pageByID(id); ok {
			t.Errorf("%q is selectable", id)
		}
	}
	a.page = "bug"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	tt.Frame()
	if a.page == "bug" {
		t.Error("the manager view kept a link as the page")
	}
}

func TestPanelOverMaterialUsesTransparentTheme(t *testing.T) {
	a, _ := newTestApp(t, false)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	tt.SetVibrancy(true)
	tt.Frame()
	for i, dark := range []bool{false, true} {
		if materialThemes[i] == nil && i == 0 {
			t.Fatal("the panel did not build the material theme")
		}
		if materialThemes[i] == nil {
			continue
		}
		if materialThemes[i].Background != ui.Transparent || materialThemes[i].Dark != dark {
			t.Errorf("material theme %d: dark=%v background=%v", i, materialThemes[i].Dark, materialThemes[i].Background)
		}
		// Toasts take their colors from Inverse: text the color of the
		// (transparent) background would not show.
		if materialThemes[i].Inverse.A == 0 || materialThemes[i].InverseText.A == 0 || materialThemes[i].Inverse == materialThemes[i].InverseText {
			t.Errorf("material theme %d: toast colors %v on %v", i, materialThemes[i].InverseText, materialThemes[i].Inverse)
		}
		if pal(materialThemes[i]) != materialPalettes[i] {
			t.Errorf("palette %d is not the material one", i)
		}
		if pal(themes[i]) != palettes[i] {
			t.Errorf("opaque palette %d changed", i)
		}
	}
}
