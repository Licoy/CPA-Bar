package view

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const provOtherSecret = "sk-other-SECRETSECRET-9999"

// seedProvidersChanged is what someone else makes of the seeded claude list
// while the user edits: the same group, and another one after it.
func seedProvidersChanged(f *cfgFake, keepMain bool) {
	var groups any
	main := `{"name":"main","prefix":"old","keys":[{"api-key":"` + provTestSecret + `","weight":2,"auth_index":"x1"}],"cloak":{"mode":"always"}},`
	if !keepMain {
		main = ""
	}
	_ = json.Unmarshal([]byte(`[`+main+`{"name":"other","keys":[{"api-key":"`+provOtherSecret+`"}]}]`), &groups)
	f.set([]string{"api-keys", "claude"}, groups)
}

// providersConflict makes the user edit the main group's prefix while the
// server's list changes, and saves: the page then holds the conflict.
func providersConflict(t *testing.T, keepMain bool) (tt *ui.Tester, st *providersState, f *cfgFake, q *cfgQueue) {
	t.Helper()
	tester, a, id, fake, queue := cfgTester(t, "providers", seedProviders)
	st = StateFor(a, "providers", id, newProvidersState)
	queue.waitFor(t, tester, "the groups", func() bool { return st.loaded })
	if err := tester.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	st.editor.prefix = "mine"
	seedProvidersChanged(fake, keepMain)
	tester.Frame()
	if err := tester.Click("保存"); err != nil {
		t.Fatal(err)
	}
	queue.waitFor(t, tester, "the conflict", func() bool { return st.cf != nil })
	queue.settle(tester)
	if got := fake.writes("PUT", "/config/api-keys/claude"); len(got) != 0 {
		t.Fatalf("overwrote the server's change: %v", got)
	}
	return tester, st, fake, queue
}

func TestProvidersConflictShowsWhatChanged(t *testing.T) {
	tt, st, _, _ := providersConflict(t, true)
	if !tt.HasText("服务器上的内容已变化") || !tt.HasText("你的修改：编辑分组“main”") {
		t.Fatalf("no conflict report; texts: %q", tt.Texts())
	}
	// The diff shows the group someone else added, with the keys masked.
	if !tt.HasText(`"name": "other"`) {
		t.Errorf("the diff lacks the added group; texts: %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, provOtherSecret) || strings.Contains(s, provTestSecret) {
			t.Fatalf("a key shown in full: %q", s)
		}
	}
	added, removed := DiffStat(st.cf.diff)
	if added == 0 || removed != 0 {
		t.Errorf("diff stat = +%d/−%d", added, removed)
	}
	// The page shows the server's list now, and the editor is closed.
	if st.editor != nil || len(st.groups["claude"]) != 2 {
		t.Errorf("editor %v, groups %d", st.editor, len(st.groups["claude"]))
	}
}

func TestProvidersConflictRetryAppliesMyEditOnTheServersList(t *testing.T) {
	tt, st, f, q := providersConflict(t, true)
	if err := tt.Click("保留我的修改并重试"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Asks first; nothing is written until the dialog's own button.
	if !tt.HasText("保留你的修改并写入？") || !tt.HasText("Home") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.writes("PUT", "/config/api-keys/claude")) != 0 {
		t.Fatal("written before the confirmation")
	}
	if err := tt.Click("确认重试"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/api-keys/claude")) == 1 })
	var got []map[string]any
	if err := json.Unmarshal([]byte(lastBody(t, f, "PUT", "/config/api-keys/claude")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["name"] != "main" || got[0]["prefix"] != "mine" || got[1]["name"] != "other" {
		t.Fatalf("list = %v", got)
	}
	if got[0]["cloak"] == nil {
		t.Error("the unknown cloak field was dropped")
	}
	if k := got[1]["keys"].([]any)[0].(map[string]any); k["api-key"] != provOtherSecret {
		t.Errorf("the other group's key = %v", k)
	}
	q.waitFor(t, tt, "the conflict to clear", func() bool { return st.cf == nil && !st.op.Running && st.conflict == "" })
}

func TestProvidersConflictReloadDiscardsMyEdit(t *testing.T) {
	tt, st, f, q := providersConflict(t, true)
	if err := tt.Click("放弃我的修改并重新加载"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the conflict to clear", func() bool { return st.cf == nil && st.loaded && !st.load.Running })
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("wrote: %v", f.seen)
	}
	if tt.HasText("服务器上的内容已变化") || len(st.groups["claude"]) != 2 {
		t.Errorf("conflict still shown, or the list is not the server's: %q", tt.Texts())
	}
	if p := rawString(st.groups["claude"][0], "prefix"); p != "old" {
		t.Errorf("prefix = %q, want the server's", p)
	}
}

func TestProvidersConflictRetryFailsWhenTheGroupIsGone(t *testing.T) {
	tt, st, f, q := providersConflict(t, false)
	if err := tt.Click("保留我的修改并重试"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认重试"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the error", func() bool { return tt.HasText("该分组已不在服务器上") })
	if len(f.writes("PUT", "/config/api-keys/claude")) != 0 {
		t.Fatal("wrote although the group is gone")
	}
	if st.cf == nil {
		t.Error("the pending edit was dropped")
	}
}

func TestProvDiffTextMasksSecrets(t *testing.T) {
	g := rawObj{
		"name":    jsonRaw("x"),
		"headers": jsonRaw(map[string]string{"Authorization": "Bearer abcdefghijklmnop"}),
		"keys":    jsonRaw([]rawObj{{"api-key": jsonRaw("sk-ant-abcdefghijklmnopqrstuvwxyz"), "weight": jsonRaw(3)}}),
	}
	out := provDiffText([]rawObj{g})
	for _, leak := range []string{"abcdefghijklmnopqrstuvwxyz", "Bearer abcdefghijklmnop"} {
		if strings.Contains(out, leak) {
			t.Fatalf("diff text leaks %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, `"weight": 3`) || !strings.Contains(out, "sk-a…wxyz #") {
		t.Errorf("diff text:\n%s", out)
	}
	// A changed secret with the same ends still shows as a change.
	g2 := rawObj{"keys": jsonRaw([]rawObj{{"api-key": jsonRaw("sk-ant-abcdefghiXXXXXXXXXXXXwxyz")}})}
	g1 := rawObj{"keys": jsonRaw([]rawObj{{"api-key": jsonRaw("sk-ant-abcdefghiYYYYYYYYYYYYwxyz")}})}
	added, removed := DiffStat(DiffLines(provDiffText([]rawObj{g1}), provDiffText([]rawObj{g2})))
	if added != 1 || removed != 1 {
		t.Errorf("a changed secret shows +%d/−%d", added, removed)
	}
}

func TestLocateGroup(t *testing.T) {
	a := rawObj{"name": jsonRaw("a"), "prefix": jsonRaw("1")}
	b := rawObj{"name": jsonRaw("b")}
	aChanged := rawObj{"name": jsonRaw("a"), "prefix": jsonRaw("2")}
	cases := []struct {
		name  string
		fresh []rawObj
		idx   int
		want  int
	}{
		{"same place", []rawObj{a, b}, 0, 0},
		{"moved", []rawObj{b, a}, 0, 1},
		{"changed by someone else, same name", []rawObj{b, aChanged}, 0, 1},
		{"gone", []rawObj{b}, 0, -1},
	}
	for _, tc := range cases {
		if got := locateGroup(tc.fresh, tc.idx, a); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
