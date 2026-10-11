package view

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateChannel(t *testing.T) {
	existing := []string{"claude", "codex"}
	for name, ok := range map[string]bool{
		"my-channel":            true,
		"a":                     true,
		"x_y.z-1":               true,
		"0day":                  true,
		"":                      false,
		"Claude":                false, // upper case
		"has space":             false,
		"slash/path":            false,
		".":                     false,
		"..":                    false,
		"-lead":                 false,
		"é":                     false,
		"claude":                false, // already there
		"codex":                 false,
		"CODEX":                 false,
		"a b":                   false,
		"semi;colon":            false,
		"percent%2f":            false,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
	} {
		got := validateChannel(name, existing) == ""
		if got != ok {
			t.Errorf("validateChannel(%q) ok = %v, want %v", name, got, ok)
		}
	}
}

func TestRoutingAddChannelNeedsAValidName(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing")
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })
	n := len(st.channels)

	for _, bad := range []string{"My-Chan", "claude", "a b"} {
		st.newChannel = bad
		tt.Frame()
		if err := tt.Click("添加渠道"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if len(st.channels) != n || st.added[bad] {
			t.Fatalf("%q was added", bad)
		}
	}
	st.newChannel = "My-Chan"
	tt.Frame()
	if !tt.HasText("小写字母") {
		t.Errorf("no hint about the name; texts: %q", tt.Texts())
	}
	st.newChannel = "claude"
	tt.Frame()
	if !tt.HasText("该渠道已存在") {
		t.Errorf("no hint about the duplicate; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("wrote: %v", f.seen)
	}
}

func TestRoutingAddChannelSavesExcludedModels(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing", func(f *cfgFake) {
		f.set([]string{"oauth", "excluded-models", "claude"}, []any{"claude-old"})
	})
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })

	st.newChannel = "my-chan.1"
	tt.Frame()
	if err := tt.Click("添加渠道"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Contains(st.channels, "my-chan.1") || st.newChannel != "" {
		t.Fatalf("channels = %v, input %q", st.channels, st.newChannel)
	}
	if !tt.HasText("新渠道") {
		t.Errorf("a new channel is not marked; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.seen) != 0 {
		t.Fatalf("adding the channel wrote: %v", f.seen)
	}

	st.excl["my-chan.1"].text = "foo-*\n bar \n"
	// An unrelated reload keeps what was typed.
	st.reload(&PageEnv{App: a, InstanceID: id, Client: mustClient(t, a, id)})
	q.waitFor(t, tt, "reload", func() bool { return st.loaded && !st.load.Running })
	if st.excl["my-chan.1"] == nil || st.excl["my-chan.1"].text != "foo-*\n bar \n" {
		t.Fatalf("the typed list was lost: %+v", st.excl["my-chan.1"])
	}
	tt.Frame()

	// The channel's own 保存: the button in its row.
	save, ok1 := tt.Find("保存")
	row, ok2 := tt.Find("my-chan.1")
	if !ok1 || !ok2 {
		t.Fatalf("no save button or channel row; texts: %q", tt.Texts())
	}
	tt.ClickAt(save.X+save.W/2, row.Y+row.H/2)
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/oauth/excluded-models/my-chan.1")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/oauth/excluded-models/my-chan.1"); got != `["foo-*","bar"]` {
		t.Errorf("body = %s", got)
	}
	if len(f.seen) != 1 {
		t.Errorf("writes = %v", f.seen)
	}
	q.waitFor(t, tt, "reload", func() bool { return !st.op.Running && !st.load.Running && !st.isNew("my-chan.1") })
	if !slices.Contains(st.channels, "my-chan.1") {
		t.Errorf("the saved channel vanished: %v", st.channels)
	}
}

func TestRoutingAddChannelSavesAliases(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing")
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })
	st.addChannel("custom")
	tt.Frame()
	ed := st.alias["custom"]
	ed.rows = append(ed.rows, &aliasRow{raw: rawObj{}, name: " gpt-x ", alias: "g-x", fork: true})
	tt.Frame()
	st.saveAliases(&PageEnv{App: a, InstanceID: id, Client: mustClient(t, a, id)}, "custom")
	q.waitFor(t, tt, "the PUT", func() bool { return len(f.writes("PUT", "/config/oauth/model-alias/custom")) == 1 })
	if got := lastBody(t, f, "PUT", "/config/oauth/model-alias/custom"); got != `[{"alias":"g-x","fork":true,"name":"gpt-x"}]` {
		t.Errorf("body = %s", got)
	}
}

func TestRoutingNewChannelCanBeDropped(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "routing")
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })
	st.addChannel("scratch")
	tt.Frame()
	if err := tt.Click("移除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if slices.Contains(st.channels, "scratch") || st.excl["scratch"] != nil || st.added["scratch"] {
		t.Errorf("channel still there: %v", st.channels)
	}
	if len(f.seen) != 0 {
		t.Errorf("wrote: %v", f.seen)
	}
}

// A click on a strategy tab chooses it: PillTabs used to apply the click
// after the frame, to a variable nothing read again.
func TestRoutingStrategyTabsChoose(t *testing.T) {
	tt, a, id, _, q := cfgTester(t, "routing")
	st := StateFor(a, "routing", id, newRoutingState)
	q.waitFor(t, tt, "the page", func() bool { return st.loaded && st.form.loaded })
	if err := tt.Click("加权轮询"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, v := range st.form.vals {
		if strings.Join(v.f.path, ".") == "routing.strategy" {
			if v.text != "weighted-round-robin" {
				t.Errorf("strategy = %q, want weighted-round-robin", v.text)
			}
			return
		}
	}
	t.Fatal("no routing.strategy field")
}
