package view

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

const cfgTestYAML = "config-version: 8\nserver:\n  port: 8317\naccess:\n  api-keys:\n    - sk-1111AAAABBBBCCCCDDDDEEEEFFFF2222\nrouting:\n  strategy: round-robin\n"

const cfgTestClientKey = "sk-1111AAAABBBBCCCCDDDDEEEEFFFF2222"

type cfgSeen struct{ method, path, body string }

// cfgFake is a fake CPA with an editable v8 configuration: JSON paths and
// the YAML text, recording every write.
type cfgFake struct {
	*httptest.Server

	mu   sync.Mutex
	doc  map[string]any
	yaml string
	seen []cfgSeen
	// putYAMLStatus, when set, rejects PUT /config.yaml with that status.
	putYAMLStatus int
	putYAMLMsg    string
}

func (f *cfgFake) writes(method, path string) []cfgSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cfgSeen
	for _, s := range f.seen {
		if s.method == method && s.path == path {
			out = append(out, s)
		}
	}
	return out
}

func (f *cfgFake) setYAML(s string) {
	f.mu.Lock()
	f.yaml = s
	f.mu.Unlock()
}

func (f *cfgFake) set(path []string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfgSetPath(f.doc, path, v)
}

func cfgSetPath(m map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, _ := m[k].(map[string]any)
		if next == nil {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

func cfgMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				cfgMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func newCfgFake(t *testing.T) *cfgFake {
	t.Helper()
	f := &cfgFake{yaml: cfgTestYAML, doc: map[string]any{
		"access":        map[string]any{"api-keys": []any{cfgTestClientKey}},
		"routing":       map[string]any{"strategy": "round-robin", "retry": map[string]any{"request-retry": 3}},
		"observability": map[string]any{"logs": map[string]any{"debug": false}},
	}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.Path, "/v8/management")
		w.Header().Set("X-CPA-VERSION", "8.0.23")
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodGet {
			f.seen = append(f.seen, cfgSeen{r.Method, path, string(body)})
		}
		reply := func(status int, s string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(s))
		}
		switch {
		case path == "/credentials":
			reply(200, `{"files":[]}`)
		case path == "/config.yaml" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte(f.yaml))
		case path == "/config.yaml" && r.Method == http.MethodPut:
			if f.putYAMLStatus != 0 {
				reply(f.putYAMLStatus, `{"error":"invalid_config","message":"`+f.putYAMLMsg+`"}`)
				return
			}
			f.yaml = string(body)
			reply(200, `{"status":"ok","config-version":8}`)
		case path == "/config" || strings.HasPrefix(path, "/config/"):
			var parts []string
			for _, p := range strings.Split(strings.TrimPrefix(path, "/config"), "/") {
				if p != "" {
					parts = append(parts, p)
				}
			}
			switch r.Method {
			case http.MethodGet:
				var node any = f.doc
				for _, p := range parts {
					m, ok := node.(map[string]any)
					if !ok {
						reply(404, `{"error":"not_found"}`)
						return
					}
					if node, ok = m[p]; !ok {
						reply(404, `{"error":"not_found"}`)
						return
					}
				}
				b, _ := json.Marshal(node)
				reply(200, string(b))
			case http.MethodPut, http.MethodPatch:
				var v any
				if err := json.Unmarshal(body, &v); err != nil {
					reply(400, `{"error":"invalid_json"}`)
					return
				}
				if r.Method == http.MethodPatch {
					cfgMerge(f.doc, v.(map[string]any))
				} else {
					cfgSetPath(f.doc, parts, v)
				}
				reply(200, `{"status":"ok"}`)
			case http.MethodDelete:
				m := f.doc
				for _, p := range parts[:len(parts)-1] {
					m, _ = m[p].(map[string]any)
					if m == nil {
						reply(404, `{"error":"not_found"}`)
						return
					}
				}
				delete(m, parts[len(parts)-1])
				reply(200, `{"status":"ok"}`)
			}
		default:
			reply(404, `{"error":"not_found"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// cfgQueue lands the results of tasks on the test's goroutine.
type cfgQueue struct{ ch chan func() }

func installCfgRun(t *testing.T) *cfgQueue {
	t.Helper()
	q := &cfgQueue{ch: make(chan func(), 256)}
	t.Cleanup(setRunOnMain(func(fn func()) { q.ch <- fn }))
	return q
}

func (q *cfgQueue) drain() {
	for {
		select {
		case f := <-q.ch:
			f()
		default:
			return
		}
	}
}

func (q *cfgQueue) waitFor(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.drain()
		tt.Frame()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, tt.Texts())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settle runs frames and lands results for a short while, so that writes
// that must not happen have had the chance to.
func (q *cfgQueue) settle(tt *ui.Tester) {
	for i := 0; i < 8; i++ {
		q.drain()
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
}

func cfgTester(t *testing.T, page string, seed ...func(*cfgFake)) (*ui.Tester, *App, string, *cfgFake, *cfgQueue) {
	t.Helper()
	q := installCfgRun(t)
	f := newCfgFake(t)
	svc, err := core.New(core.Options{
		Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)},
		Secrets:  &store.MemorySecrets{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	id, err := svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: f.URL, Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if s, _ := svc.Snapshot(id); s.HasData {
			break
		}
	}
	a := NewApp(svc)
	a.selected, a.page = id, page
	// The tester builds a first frame, which starts the loads: seed first.
	for _, fn := range seed {
		fn(f)
	}
	return ui.NewTester(a.managerView, managerWidth, 2400), a, id, f, q // tall: whole pages are on screen
}

func TestConfigYAMLSaveNeedsConfirmation(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	st.mode = cfgModeYAML
	q.waitFor(t, tt, "YAML to load", func() bool { return st.yaml.loaded && tt.HasText("目标实例：Home") })
	if st.yaml.edit.String() != cfgTestYAML {
		t.Fatalf("editor holds %q", st.yaml.edit.String())
	}
	if !tt.HasText("YAML 含有上游密钥") {
		t.Errorf("no warning about secrets; texts: %q", tt.Texts())
	}

	edited := strings.Replace(cfgTestYAML, "8317", "9000", 1)
	st.yaml.edit.Set(edited)
	tt.Frame()
	if !tt.HasText("+1 / −1") {
		t.Fatalf("no diff summary; texts: %q", tt.Texts())
	}
	if err := tt.Click("预览改动"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the diff", func() bool { return tt.HasText("  port: 9000") && tt.HasText("  port: 8317") })

	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Home") || !tt.HasText("替换整个配置？") {
		t.Fatalf("no confirmation naming the instance; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if got := f.writes("PUT", "/config.yaml"); len(got) != 0 {
		t.Fatalf("written before the confirmation: %v", got)
	}

	if err := tt.Click("确认保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the write", func() bool { return len(f.writes("PUT", "/config.yaml")) == 1 })
	if got := f.writes("PUT", "/config.yaml")[0].body; got != edited {
		t.Errorf("PUT body = %q, want the edited text", got)
	}
	// After the save the editor shows the server's text and is clean.
	q.waitFor(t, tt, "reload after save", func() bool { return !st.yaml.save.Running && !st.yaml.load.Running && !st.yaml.dirty })
}

func TestConfigYAMLRejectedKeepsEdit(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	f.putYAMLStatus, f.putYAMLMsg = 422, "unknown v8 configuration section bogus"
	st := StateFor(a, "config", id, newConfigState)
	st.mode = cfgModeYAML
	q.waitFor(t, tt, "YAML to load", func() bool { return st.yaml.loaded })

	edited := cfgTestYAML + "bogus: 1\n"
	st.yaml.edit.Set(edited)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the error", func() bool { return tt.HasText("unknown v8 configuration section bogus") })
	if st.yaml.edit.String() != edited || !st.yaml.dirty {
		t.Errorf("the edit was not kept: %q", st.yaml.edit.String())
	}
	if !tt.HasText("HTTP 422") {
		t.Errorf("error lacks the status; texts: %q", tt.Texts())
	}
}

func TestConfigYAMLServerChangedDoesNotOverwrite(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	st.mode = cfgModeYAML
	q.waitFor(t, tt, "YAML to load", func() bool { return st.yaml.loaded })

	edited := strings.Replace(cfgTestYAML, "8317", "9000", 1)
	st.yaml.edit.Set(edited)
	tt.Frame()
	// Someone else edits the file meanwhile.
	theirs := cfgTestYAML + "observability:\n  logs:\n    debug: true\n"
	f.setYAML(theirs)

	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the conflict", func() bool { return st.yaml.conflict })
	q.settle(tt)
	if got := f.writes("PUT", "/config.yaml"); len(got) != 0 {
		t.Fatalf("overwrote the server's change: %v", got)
	}
	if st.yaml.edit.String() != edited || st.yaml.base != theirs {
		t.Errorf("edit or base wrong: edit=%q base=%q", st.yaml.edit.String(), st.yaml.base)
	}
	if !tt.HasText("已被修改") || !tt.HasText("    debug: true") {
		t.Errorf("no warning with the diff; texts: %q", tt.Texts())
	}
}

func TestConfigYAMLDiscard(t *testing.T) {
	tt, a, id, _, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	st.mode = cfgModeYAML
	q.waitFor(t, tt, "YAML to load", func() bool { return st.yaml.loaded })
	st.yaml.edit.Set("changed: true\n")
	tt.Frame()
	if err := tt.Click("放弃修改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Nothing is thrown away before the confirmation.
	if !tt.HasText("放弃所有修改？") || st.yaml.edit.String() == cfgTestYAML {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	if err := tt.Click("确认放弃"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.yaml.edit.String() != cfgTestYAML || tt.HasText("+1 /") {
		t.Errorf("revert left %q", st.yaml.edit.String())
	}
}

func TestConfigYAMLReloadAsksBeforeDroppingEdits(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	st.mode = cfgModeYAML
	q.waitFor(t, tt, "YAML to load", func() bool { return st.yaml.loaded })
	f.setYAML(cfgTestYAML + "# comment\n")
	st.yaml.edit.Set("mine: true\n")
	tt.Frame()
	if err := tt.Click("重新加载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("丢弃未保存的修改？") || st.yaml.edit.String() != "mine: true\n" {
		t.Fatalf("no confirmation, or the edit is gone: %q", tt.Texts())
	}
	if err := tt.Click("确认重新加载"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the reload", func() bool { return st.yaml.edit.String() == cfgTestYAML+"# comment\n" })
	if len(f.seen) != 0 {
		t.Errorf("reload wrote: %v", f.seen)
	}
}

func TestConfigFormPatchesChangedSettingsOnly(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	q.waitFor(t, tt, "the form", func() bool { return st.form.loaded && tt.HasText("调试日志") })
	if !tt.HasText("目标实例：Home") {
		t.Errorf("save controls lack the target; texts: %q", tt.Texts())
	}

	var debug, retry *cfgVal
	for _, v := range st.form.vals {
		switch v.f.id() {
		case "observability/logs/debug":
			debug = v
		case "routing/retry/request-retry":
			retry = v
		}
	}
	if retry.base != "3" || debug.base != "false" {
		t.Fatalf("baseline = %q / %q", retry.base, debug.base)
	}
	debug.on = true
	retry.text = "5"
	tt.Frame()
	if err := tt.Click("保存更改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Home") {
		t.Fatalf("confirmation lacks the instance; texts: %q", tt.Texts())
	}
	q.settle(tt)
	if len(f.writes("PATCH", "/config")) != 0 {
		t.Fatal("written before the confirmation")
	}
	if err := tt.Click("确认保存"); err != nil {
		t.Fatal(err)
	}
	q.waitFor(t, tt, "the patch", func() bool { return len(f.writes("PATCH", "/config")) == 1 })
	var got map[string]any
	_ = json.Unmarshal([]byte(f.writes("PATCH", "/config")[0].body), &got)
	want := `{"observability":{"logs":{"debug":true}},"routing":{"retry":{"request-retry":5}}}`
	b, _ := json.Marshal(got)
	if string(b) != want {
		t.Errorf("patch = %s, want %s", b, want)
	}
	q.waitFor(t, tt, "reload", func() bool { return !st.form.save.Running && !st.form.load.Running && !st.form.dirty() })
}

func TestConfigFormConflictDoesNotWrite(t *testing.T) {
	tt, a, id, f, q := cfgTester(t, "config")
	st := StateFor(a, "config", id, newConfigState)
	q.waitFor(t, tt, "the form", func() bool { return st.form.loaded })
	for _, v := range st.form.vals {
		if v.f.id() == "routing/retry/request-retry" {
			v.text = "5"
		}
	}
	f.set([]string{"routing", "retry", "request-retry"}, 9) // changed by someone else
	st.form.saveChanges(&PageEnv{App: a, InstanceID: id, Client: mustClient(t, a, id)})
	q.waitFor(t, tt, "the conflict", func() bool { return st.form.conflict != "" })
	if len(f.writes("PATCH", "/config")) != 0 {
		t.Fatal("wrote over the server's change")
	}
	if !strings.Contains(st.form.conflict, "3 → 9") {
		t.Errorf("conflict = %q", st.form.conflict)
	}
}

func mustClient(t *testing.T, a *App, id string) *cpa.Client {
	t.Helper()
	cl, err := a.Svc.Client(id)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}
