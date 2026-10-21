package view

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/store"
)

func TestMasks(t *testing.T) {
	SetPrivacy(true)
	defer SetPrivacy(false)
	for in, want := range map[string]string{
		"":                   "",
		"a":                  "a***",
		"abcd":               "a***d",
		"example":            "e***e",
		"licoy@example.com":  "l****@e***.com",
		"example@qq.com":     "e****@q***.com",
		"ok@x.io":            "o****@x***.io",
		"Team Alpha Account": "Team***ount",
	} {
		if got := PrivName(in); got != want {
			t.Errorf("PrivName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"http://127.0.0.1:8317":                 "http://1**.*.*.*:8317",
		"https://example.com":                   "https://e****.com",
		"https://example.cdn.com/v0":            "https://e****.c**.com/v0",
		"https://cpa.example.com/v8/management": "https://c****.e**.com/v8/management",
		"http://localhost:8317":                 "http://l****:8317",
		"http://[::1]:8317":                     "http://[:****]:8317",
	} {
		if got := PrivURL(in); got != want {
			t.Errorf("PrivURL(%q) = %q, want %q", in, got, want)
		}
	}
	setPrivacyHosts([]store.InstanceProfile{{BaseURL: "http://10.1.2.3:8317"}})
	got := PrivText("dial tcp 10.1.2.3:8317: refused for bob@corp.example.com")
	if strings.Contains(got, "10.1.2.3") || strings.Contains(got, "bob@") {
		t.Errorf("PrivText left identifiers: %q", got)
	}

	SetPrivacy(false)
	if PrivName("licoy@example.com") != "licoy@example.com" || PrivURL("http://a.b") != "http://a.b" {
		t.Error("masks apply with privacy mode off")
	}
}

// With privacy mode on, the panel shows no email and no address, and Copy
// Address still copies the real one.
func TestPanelPrivacy(t *testing.T) {
	a, id := newTestApp(t, true)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.Privacy = true }); err != nil {
		t.Fatal(err)
	}
	SetPrivacy(true)
	defer SetPrivacy(false)
	a.panelInstance = id
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	for _, s := range tt.Texts() {
		if strings.Contains(s, "@example.com") || strings.Contains(s, "127.0.0.1") {
			t.Errorf("panel shows %q in privacy mode", s)
		}
	}
	if !tt.HasText("o****@e***.com") {
		t.Errorf("masked email missing; texts: %q", tt.Texts())
	}
	if err := tt.Click("复制地址"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("copied %q, want the real address", got)
	}
}

func TestPanelPrivacyButton(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "detail"}[detail], func(t *testing.T) {
			a, id := newTestApp(t, true)
			defer SetPrivacy(false)
			if detail {
				a.panelInstance = id
			}
			tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
			button, foundButton := tt.Find("开启隐私模式")
			refresh, foundRefresh := tt.Find("刷新")
			if !foundButton || !foundRefresh || button.X+button.W > refresh.X || button.Y != refresh.Y {
				t.Fatalf("privacy button must precede refresh in the toolbar: privacy=%v refresh=%v", button, refresh)
			}
			for _, on := range []bool{true, false} {
				label := map[bool]string{true: "开启隐私模式", false: "关闭隐私模式"}[on]
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if a.Svc.Settings().Privacy != on || Private() != on {
					t.Fatalf("privacy setting and live state must both be %v", on)
				}
				if !detail {
					if err := tt.Click("Home"); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
				if tt.HasText("ok@example.com") == on || tt.HasText("o****@e***.com") != on {
					t.Fatalf("privacy=%v: unexpected account text: %q", on, tt.Texts())
				}
				if !detail {
					if err := tt.Click("返回"); err != nil {
						t.Fatal(err)
					}
					tt.Frame()
				}
			}
		})
	}
}

func TestPanelPrivacySaveFailureHasNoEffect(t *testing.T) {
	svc, err := core.New(core.Options{Settings: failingSettings{store.Defaults()}, Secrets: &store.MemorySecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	a := NewApp(svc)
	defer SetPrivacy(false)
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	if err := tt.Click("开启隐私模式"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.Svc.Settings().Privacy || Private() {
		t.Fatal("failed save changed privacy mode")
	}
	if !tt.HasText("save settings: disk full") {
		t.Fatalf("save failure is not visible: %q", tt.Texts())
	}
}

func TestSettingsPrivacySwitch(t *testing.T) {
	a, _ := newTestApp(t, true)
	defer SetPrivacy(false)
	a.page = "settings"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	// The switch sits at the right of its row, as the language tabs do.
	label, ok1 := tt.Find("隐私模式")
	edge, ok2 := tt.Find("English")
	if !ok1 || !ok2 {
		t.Fatalf("settings lack the privacy row; texts: %q", tt.Texts())
	}
	tt.ClickAt(edge.X+edge.W-6, label.Y+label.H)
	tt.Frame()
	if !a.Svc.Settings().Privacy || !Private() {
		t.Fatal("privacy mode did not turn on")
	}
}

func TestProviderLabel(t *testing.T) {
	for in, want := range map[string]string{
		"claude": "Claude (Anthropic)", "Codex": "Codex (OpenAI)", "x_ai": "xAI (Grok)",
		"kimi-ai": "Kimi AI", "gemini-cli": "Gemini CLI", "my-llm": "My Llm", "": "", "openai-compatible-openrouter": "OpenAI Compatible · openrouter",
	} {
		if got := providerLabel(in); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
