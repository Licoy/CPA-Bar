package view

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func lastBody(t *testing.T, f *cfgFake, method, path string) string {
	t.Helper()
	got := f.writes(method, path)
	if len(got) == 0 {
		t.Fatalf("no %s %s; writes: %v", method, path, f.seen)
	}
	return got[len(got)-1].body
}

func TestGenerateClientKey(t *testing.T) {
	re := regexp.MustCompile(`^sk-[0-9A-Za-z]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		k, err := generateClientKey()
		if err != nil || !re.MatchString(k) {
			t.Fatalf("key %q, err %v", k, err)
		}
		if seen[k] {
			t.Fatalf("repeated key %q", k)
		}
		seen[k] = true
	}
}

func TestKeysAreMaskedAndRevealedOnRequest(t *testing.T) {
	tt, a, id, _, q := cfgTester(t, "keys")
	q.waitFor(t, tt, "the keys", func() bool { return tt.HasText(cfgMask(cfgTestClientKey)) })
	for _, s := range tt.Texts() {
		if strings.Contains(s, cfgTestClientKey) || strings.Contains(s, "AAAABBBB") {
			t.Fatalf("full key on screen: %q", s)
		}
	}
	// The first 显示 is the add form's checkbox; the key's button is the last.
	st := keysStateOf(a, id)
	st.reveal[cfgTestClientKey] = true
	q.waitFor(t, tt, "reveal", func() bool { return tt.HasText(cfgTestClientKey) })
	if err := tt.Click("隐藏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText(cfgTestClientKey) {
		t.Error("still revealed after hiding")
	}
}

func keysStateOf(a *App, id string) *keysState { return StateFor(a, "keys", id, newKeysState) }

func TestKeysAddSendsFullList(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "keys")
	st := keysStateOf(a, id)
	q.waitFor(t, tt, "the keys", func() bool { return st.loaded })
	if !tt.HasText("目标实例：Home") {
		t.Errorf("no target; texts: %q", tt.Texts())
	}
	st.newKey = "sk-brand-new-key-0000"
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/access/api-keys")) == 1 })
	var got []string
	if err := json.Unmarshal([]byte(lastBody(t, f, "PUT", "/config/access/api-keys")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != cfgTestClientKey || got[1] != "sk-brand-new-key-0000" {
		t.Errorf("list = %v", got)
	}
	q.waitFor(t, tt, "reload", func() bool { return len(st.keys) == 2 && st.newKey == "" })
	for _, s := range tt.Texts() {
		if strings.Contains(s, "sk-brand-new-key-0000") {
			t.Fatalf("new key shown in full: %q", s)
		}
	}
}

func TestKeysChangedOnServerIsNotOverwritten(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "keys")
	st := keysStateOf(a, id)
	q.waitFor(t, tt, "the keys", func() bool { return st.loaded })
	f.set([]string{"access", "api-keys"}, []any{cfgTestClientKey, "sk-someone-else"})
	st.newKey = "sk-mine"
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the warning", func() bool { return st.conflict != "" })
	q.settle(tt)
	if len(f.writes("PUT", "/config/access/api-keys")) != 0 {
		t.Fatal("overwrote the server's list")
	}
	if len(st.keys) != 2 {
		t.Errorf("list not refreshed: %v", st.keys)
	}
}

func TestKeysDeleteAsksFirst(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "keys")
	st := keysStateOf(a, id)
	q.waitFor(t, tt, "the keys", func() bool { return st.loaded })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("删除客户端密钥？") || !tt.HasText("Home") || !tt.HasText("最后一个密钥") {
		t.Fatalf("no confirmation naming the instance; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("written before the confirmation: %v", f.seen)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	q.settle(tt)
	if len(f.seen) != 0 || tt.HasText("删除客户端密钥？") {
		t.Fatalf("cancel wrote or kept the dialog: %v", f.seen)
	}
}

func TestKeysDeleteConfirmSendsList(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "keys", func(f *cfgFake) {
		f.set([]string{"access", "api-keys"}, []any{cfgTestClientKey, "sk-other"})
	})
	st := keysStateOf(a, id)
	q.waitFor(t, tt, "the keys", func() bool { return st.loaded && len(st.keys) == 2 })
	// Delete the first key: the row's own button, then the dialog's.
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("written before the confirmation: %v", f.seen)
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/access/api-keys")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/access/api-keys"); got != `["sk-other"]` {
		t.Errorf("body = %s", got)
	}
}

func TestKeysRemoveSendsListWithoutIt(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "keys", func(f *cfgFake) {
		f.set([]string{"access", "api-keys"}, []any{cfgTestClientKey, "sk-other"})
	})
	st := keysStateOf(a, id)
	q.waitFor(t, tt, "the keys", func() bool { return st.loaded && len(st.keys) == 2 })
	st.mutate(&PageEnv{App: a, InstanceID: id, Client: mustClient(t, a, id)}, func(fresh []string) ([]string, error) {
		return []string{fresh[1]}, nil
	}, func() {})
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/access/api-keys")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/access/api-keys"); got != `["sk-other"]` {
		t.Errorf("body = %s", got)
	}
}

const provTestSecret = "sk-ant-SECRETSECRETSECRET-1234"

func seedProviders(f *cfgFake) {
	var groups any
	_ = json.Unmarshal([]byte(`[{"name":"main","prefix":"old","keys":[{"api-key":"`+provTestSecret+`","weight":2,"auth_index":"x1"}],"cloak":{"mode":"always"}}]`), &groups)
	f.set([]string{"api-keys", "claude"}, groups)
}

func TestProvidersMaskKeysAndEditKeepsUnknownFields(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "providers", seedProviders)
	st := StateFor(a, "providers", id, newProvidersState)
	q.waitFor(t, tt, "the groups", func() bool { return st.loaded && tt.HasText(cfgMask(provTestSecret)) })
	for _, s := range tt.Texts() {
		if strings.Contains(s, provTestSecret) {
			t.Fatalf("full key on screen: %q", s)
		}
	}
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.editor == nil {
		t.Fatal("no editor")
	}
	st.editor.prefix = "team-a"
	tt.Frame()
	if !tt.HasText("Home") {
		t.Errorf("editor does not name the instance; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("written before saving: %v", f.seen)
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/api-keys/claude")) == 1 })
	var got []map[string]any
	if err := json.Unmarshal([]byte(lastBody(t, f, "PUT", "/config/api-keys/claude")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["prefix"] != "team-a" || got[0]["name"] != "main" {
		t.Fatalf("group = %v", got)
	}
	if got[0]["cloak"] == nil {
		t.Error("the unknown cloak field was dropped")
	}
	keys := got[0]["keys"].([]any)
	k := keys[0].(map[string]any)
	if k["api-key"] != provTestSecret || k["weight"] != float64(2) {
		t.Errorf("key = %v", k)
	}
	if _, ok := k["auth_index"]; ok {
		t.Error("auth_index sent back")
	}
}

func TestProvidersChangedOnServerIsNotOverwritten(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "providers", seedProviders)
	st := StateFor(a, "providers", id, newProvidersState)
	q.waitFor(t, tt, "the groups", func() bool { return st.loaded })
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	st.editor.prefix = "mine"
	f.set([]string{"api-keys", "claude"}, []any{map[string]any{"name": "other", "keys": []any{map[string]any{"api-key": "k"}}}})
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the warning", func() bool { return st.conflict != "" })
	q.settle(tt)
	if len(f.writes("PUT", "/config/api-keys/claude")) != 0 {
		t.Fatal("overwrote the server's change")
	}
}

func TestProvidersDeleteAsksFirst(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "providers", seedProviders)
	st := StateFor(a, "providers", id, newProvidersState)
	q.waitFor(t, tt, "the groups", func() bool { return st.loaded && tt.HasText("main") })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("删除分组？") || !tt.HasText("Home") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("written before the confirmation: %v", f.seen)
	}
	// The dialog's own button, not the row's 删除, sends the write.
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/api-keys/claude")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/api-keys/claude"); got != `[]` {
		t.Errorf("body = %s, want the empty list", got)
	}
}

func TestRoutingStrategyPatches(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing")
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })
	for _, v := range st.form.vals {
		if v.f.id() == "routing/strategy" {
			if v.base != "round-robin" {
				t.Fatalf("base = %q", v.base)
			}
			v.text = "fill-first"
		}
	}
	tt.Frame()
	if err := tt.Click("保存更改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	q.settle(tt)
	if len(f.writes("PATCH", "/config")) != 0 {
		t.Fatal("written before the confirmation")
	}
	// The dialog's confirm label is its own ("保存" belongs to the channel rows).
	if err := tt.Click("确认保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the patch", func() bool { return len(f.writes("PATCH", "/config")) == 1 })
	if got := lastBody(t, f, "PATCH", "/config"); got != `{"routing":{"strategy":"fill-first"}}` {
		t.Errorf("patch = %s", got)
	}
}

func TestRoutingExcludedModelsSave(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing", func(f *cfgFake) {
		f.set([]string{"oauth", "excluded-models", "claude"}, []any{"claude-old"})
	})
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.excl["claude"] != nil })
	if st.excl["claude"].text != "claude-old" {
		t.Fatalf("text = %q", st.excl["claude"].text)
	}
	st.excl["claude"].text = "claude-old\nclaude-*-preview"
	tt.Frame()
	if !tt.HasText("目标实例：Home") {
		t.Errorf("no target; texts: %q", tt.Texts())
	}
	// Every channel row has a 保存 button in the same column: click the one
	// on the claude row.
	save, ok1 := tt.Find("保存")
	row, ok2 := tt.Find("claude")
	if !ok1 || !ok2 {
		t.Fatalf("no save button or claude row; texts: %q", tt.Texts())
	}
	tt.ClickAt(save.X+save.W/2, row.Y+row.H/2)
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/oauth/excluded-models/claude")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/oauth/excluded-models/claude"); got != `["claude-old","claude-*-preview"]` {
		t.Errorf("body = %s", got)
	}
}
