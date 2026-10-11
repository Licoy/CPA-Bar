package view

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Every provider CPA names has a LobeHub logo, but iFlow, which LobeHub has
// none of; every embedded logo is used.
func TestProviderLogos(t *testing.T) {
	for id := range providerNames {
		if id == "iflow" {
			continue
		}
		if providerLogo(id) == nil {
			t.Errorf("provider %q has no logo", id)
		}
	}
	for _, id := range []string{"Claude", "openai-compatible-deepseek", "x_ai"} {
		if providerLogo(id) == nil {
			t.Errorf("provider %q has no logo", id)
		}
	}
	if providerLogo("iflow") != nil || providerLogo("my-plugin") != nil {
		t.Error("a provider without a LobeHub logo has one")
	}
	used := map[string]bool{}
	for _, f := range providerLogoFiles {
		used[f] = true
	}
	files, _ := fs.Glob(providerSVGs, "assets/providers/*.svg")
	for _, f := range files {
		if name := strings.TrimSuffix(strings.TrimPrefix(f, "assets/providers/"), ".svg"); !used[name] {
			t.Errorf("%s is embedded but not used", f)
		}
	}
}

// The panel has no instance picker: an instance is opened from the list,
// and Back returns to all of them. GitHub opens the repository.
func TestPanelBackAndGitHub(t *testing.T) {
	a, id := newTestApp(t, true)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	if tt.HasText("全部实例") || tt.HasText("返回") {
		t.Errorf("the overview has a picker or Back; texts: %q", tt.Texts())
	}
	if err := tt.Click("Home"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.panelInstance != id || !tt.HasText("返回") {
		t.Fatalf("instance %q after a click; texts: %q", a.panelInstance, tt.Texts())
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.panelInstance != "" || tt.HasText("返回") {
		t.Errorf("instance %q after Back", a.panelInstance)
	}
	if err := tt.Click("GitHub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != RepoURL {
		t.Errorf("opened %q", got)
	}
}
