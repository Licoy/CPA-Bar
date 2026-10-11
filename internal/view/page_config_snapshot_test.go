package view

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestConfigPagesSnapshots draws the four configuration pages with data, to
// $CPABAR_SNAPSHOTS, as cfgpages-<page>-<light|dark>.png.
func TestConfigPagesSnapshots(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("CPABAR_SNAPSHOTS is not set")
	}
	seed := func(f *cfgFake) {
		var providers map[string]any
		_ = json.Unmarshal([]byte(`{
			"gemini":[{"name":"gemini-1","prefix":"team-a","excluded-models":["gemini-2.5-pro","*-preview"],"keys":[{"api-key":"AIzaSyA1234567890abcdefghijklmnopqrstuv","weight":5},{"api-key":"AIzaSyB1234567890abcdefghijklmnopqrstuv","proxy-url":"direct"}]}],
			"claude":[{"name":"main","base-url":"https://api.anthropic.com","models":[{"name":"claude-sonnet-4-5","alias":"sonnet"},{"name":"claude-opus-4-1","alias":"opus"}],"keys":[{"api-key":"sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ-1234"}]}],
			"openai-compatibility":[{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","disabled":true,"keys":[{"api-key":"sk-or-v1-abcdefghijklmnopqrstuvwxyz0123"}]}]
		}`), &providers)
		f.set([]string{"api-keys"}, providers)
		f.set([]string{"access", "api-keys"}, []any{cfgTestClientKey, "sk-9999AAAABBBBCCCCDDDDEEEEFFFF0000"})
		f.set([]string{"oauth", "excluded-models", "claude"}, []any{"claude-old", "claude-*-preview"})
		f.set([]string{"oauth", "model-alias", "codex"}, []any{map[string]any{"name": "gpt-5", "alias": "g5", "fork": true}})
		f.set([]string{"routing", "strategy"}, "weighted-round-robin")
	}
	save := func(name string, tt *ui.Tester) {
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, page := range []string{"config", "config-yaml", "providers", "providers-conflict", "keys", "routing"} {
		for _, dark := range []bool{false, true} {
			id := page
			if id == "config-yaml" {
				id = "config"
			}
			if id == "providers-conflict" {
				id = "providers"
			}
			tt, a, inst, f, q := cfgTester(t, id, seed)
			switch page {
			case "config-yaml":
				StateFor(a, "config", inst, newConfigState).mode = cfgModeYAML
			case "providers-conflict":
				// provoke the conflict
			}
			tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 80})
			tt.SetDark(dark)
			q.waitFor(t, tt, "the page", func() bool {
				switch page {
				case "config":
					return StateFor(a, "config", inst, newConfigState).form.loaded
				case "config-yaml":
					return StateFor(a, "config", inst, newConfigState).yaml.loaded
				case "providers", "providers-conflict":
					return StateFor(a, "providers", inst, newProvidersState).loaded
				case "keys":
					return keysStateOf(a, inst).loaded
				}
				st := StateFor(a, "routing", inst, newRoutingState)
				return st.loaded && st.form.loaded
			})
			if page == "providers-conflict" {
				st := StateFor(a, "providers", inst, newProvidersState)
				st.editor = newProvEditor("claude", 0, st.groups["claude"][0])
				st.editor.prefix = "mine"
				var g any
				_ = json.Unmarshal([]byte(`[{"name":"main","base-url":"https://api.anthropic.com","models":[{"name":"claude-sonnet-4-5","alias":"sonnet-new"}],"keys":[{"api-key":"sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ-1234"}]}]`), &g)
				f.set([]string{"api-keys", "claude"}, g)
				st.editor.save(&PageEnv{App: a, InstanceID: inst, Client: mustClient(t, a, inst)}, st)
				q.waitFor(t, tt, "the conflict", func() bool { return st.cf != nil })
			}
			if page == "routing" {
				StateFor(a, "routing", inst, newRoutingState).addChannel("my-channel")
			}
			for range 3 {
				q.settle(tt)
			}
			save("cfgpages-"+page+"-"+map[bool]string{false: "light", true: "dark"}[dark], tt)
		}
	}
}
