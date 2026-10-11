package view

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/cpa"
)

func pluginsFake(t testing.TB) *pageFake {
	f := &pageFake{plugin: "1"}
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		path := strings.TrimPrefix(r.URL.Path, "/v8/management")
		switch {
		case path == "/plugins" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"plugins_enabled":true,"plugins_dir":"/srv/plugins","plugins":[
				{"id":"sample","path":"/srv/plugins/sample.so","configured":true,"registered":true,"enabled":true,"effective_enabled":true,"supports_quota":true,"quota_provider":"claude",
				 "metadata":{"name":"Sample Plugin","version":"1.2.0","author":"Ann"}},
				{"id":"other","configured":true,"registered":true,"enabled":false,"effective_enabled":false}]}`))
		case path == "/plugins/store" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"plugins_enabled":true,"plugins":[
				{"id":"fancy","name":"Fancy Tool","description":"Does fancy things","version":"0.3.1","author":"Bob"},
				{"id":"sample","name":"Sample Plugin","version":"1.3.0","installed":true,"installed_version":"1.2.0","update_available":true},
				"garbage", {"name":"no id"}],
				"source_errors":[{"source_name":"mirror","message":"timeout"}]}`))
		case path == "/config/plugins/configs/sample" && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"ok":true}`))
		case path == "/plugins/sample/quota" && r.Method == http.MethodGet:
			if r.URL.Query().Get("auth_index") != "7" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"auth_index is required"}`))
				return true
			}
			_, _ = w.Write([]byte(`{"plan":"pro","windows":[{"name":"5h","used":12,"limit":100}]}`))
		case path == "/plugins/sample" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"status":"deleted"}`))
		case path == "/plugins/store/fancy/install" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status":"installed","id":"fancy"}`))
		default:
			return false
		}
		return true
	}
	return f
}

func TestPluginsPageListsAndToggles(t *testing.T) {
	f := pluginsFake(t)
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "plugins"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)

	pump(t, tt, q, 5*time.Second, "the plugin list", func() bool { return tt.HasText("Sample Plugin") && tt.HasText("other") })
	for _, s := range []string{"v1.2.0", "运行中", "已停用", "/srv/plugins"} {
		if !tt.HasText(s) {
			t.Errorf("page lacks %q; texts: %q", s, tt.Texts())
		}
	}
	if n := f.count("GET", "/plugins"); n != 1 {
		t.Errorf("%d plugin list reads, want 1 (loaded once on first show)", n)
	}

	// Turning "other" on patches its configuration.
	if err := tt.Click("启用 other"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the PATCH", func() bool { return f.count("PATCH", "/config/plugins/configs/other") == 1 })
	reqs := f.requests("PATCH", "/config/plugins/configs/other")
	var body map[string]bool
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil || !body["enabled"] {
		t.Errorf("PATCH body %q", reqs[0].Body)
	}
	// The list is read again afterwards.
	pump(t, tt, q, 5*time.Second, "the reload", func() bool { return f.count("GET", "/plugins") >= 2 })
	noUsageQueue(t, f)
}

func TestPluginsPageUninstallAsksFirst(t *testing.T) {
	f := pluginsFake(t)
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "plugins"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the plugin list", func() bool { return tt.HasText("Sample Plugin") })

	if err := tt.Click("卸载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Home") || !tt.HasText("卸载插件“Sample Plugin”") {
		t.Fatalf("the confirmation does not name instance and plugin; texts: %q", tt.Texts())
	}
	if n := f.count("DELETE", "/plugins/sample"); n != 0 {
		t.Fatalf("%d DELETE before confirming", n)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	settle(tt, q, 150*time.Millisecond)
	if n := f.count("DELETE", "/plugins/sample"); n != 0 {
		t.Fatalf("%d DELETE after canceling", n)
	}
	if err := tt.Click("卸载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// The dialog's confirm button has a label of its own.
	if err := tt.Click("确认卸载"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the DELETE", func() bool { return f.count("DELETE", "/plugins/sample") == 1 })
}

func TestPluginsStoreInstallAsksFirst(t *testing.T) {
	f := pluginsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "plugins"
	st := StateFor(a, "plugins", id, func() *pluginsState { return &pluginsState{toggles: map[string]*Task{}} })
	st.tab = pluginsTabStore
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the store", func() bool { return tt.HasText("Fancy Tool") })
	for _, s := range []string{"Does fancy things", "有更新", "mirror: timeout", "第三方代码"} {
		if !tt.HasText(s) {
			t.Errorf("store lacks %q; texts: %q", s, tt.Texts())
		}
	}
	if len(st.entries) != 2 {
		t.Errorf("%d entries parsed, want 2 (garbage skipped)", len(st.entries))
	}

	if err := tt.Click("安装"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("在“Home”上安装插件？") {
		t.Fatalf("no confirmation naming the instance; texts: %q", tt.Texts())
	}
	if n := f.count("POST", "/plugins/store/fancy/install"); n != 0 {
		t.Fatalf("%d installs before confirming", n)
	}
	if err := tt.Click("确认安装"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the install", func() bool { return f.count("POST", "/plugins/store/fancy/install") == 1 })
	noUsageQueue(t, f)
}

func TestPluginsPageUnsupported(t *testing.T) {
	f := &pageFake{plugin: "0"}
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "plugins"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	settle(tt, q, 200*time.Millisecond)
	if !tt.HasText("此服务器不支持插件") {
		t.Errorf("no unsupported notice; texts: %q", tt.Texts())
	}
	if n := f.count("GET", "/plugins"); n != 0 {
		t.Errorf("%d plugin reads from an unsupporting server", n)
	}
}

func TestPluginsPageMissingRoute(t *testing.T) {
	f := &pageFake{} // answers 404 for /plugins
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "plugins"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the unsupported notice", func() bool { return tt.HasText("此服务器不支持插件") })
}

func TestPluginsParseStore(t *testing.T) {
	entries, errs := pluginsParseStore(&cpa.PluginStore{
		Plugins:      []json.RawMessage{json.RawMessage(`{"id":"a","name":"A"}`), json.RawMessage(`42`), json.RawMessage(`{"id":""}`)},
		SourceErrors: []json.RawMessage{json.RawMessage(`{"source_id":"s1","error":"boom"}`), json.RawMessage(`"raw"`)},
	})
	if len(entries) != 1 || entries[0].title() != "A" {
		t.Errorf("entries %+v", entries)
	}
	if len(errs) != 2 || errs[0] != "s1: boom" {
		t.Errorf("errs %q", errs)
	}
	var p cpa.Plugin
	if err := json.Unmarshal([]byte(`{"id":"x","metadata":{"name":"N","version":"2","author":"Z"}}`), &p); err != nil {
		t.Fatal(err)
	}
	if m := pluginsMetaOf(p); m.Name != "N" || m.Version != "2" || m.Author != "Z" {
		t.Errorf("meta %+v", m)
	}
}

func TestPluginsQuotaDialog(t *testing.T) {
	f := pluginsFake(t)
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "plugins"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the plugin list", func() bool { return tt.HasText("Sample Plugin") })

	// Only "sample" reports a quota.
	if err := tt.Click("查看额度"); err != nil {
		t.Fatal(err)
	}
	// The instance has one matching credential: it is asked at once, with
	// its auth_index.
	pump(t, tt, q, 5*time.Second, "the quota", func() bool { return tt.HasText(`"used": 12`) })
	reqs := f.requests("GET", "/plugins/sample/quota")
	if len(reqs) != 1 || reqs[0].Query != "auth_index=7" {
		t.Fatalf("quota requests %+v, want one with auth_index=7", reqs)
	}
	for _, s := range []string{`"plan": "pro"`, "ok@example.com · Claude (Anthropic) · #7", "Sample Plugin 的额度"} {
		if !tt.HasText(s) {
			t.Errorf("dialog lacks %q; texts: %q", s, tt.Texts())
		}
	}
	if err := tt.Click("复制额度"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var got map[string]any
	if err := json.Unmarshal([]byte(tt.Clipboard()), &got); err != nil || got["plan"] != "pro" || !strings.Contains(tt.Clipboard(), "\n  ") {
		t.Errorf("copied %q, want the pretty-printed JSON", tt.Clipboard())
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText(`"used": 12`) {
		t.Error("the dialog stays open after Close")
	}
}

func TestPluginsQuotaCreds(t *testing.T) {
	creds := []cpa.Credential{
		{AuthIndex: "1", Provider: "claude", Email: "a@x"},
		{AuthIndex: "2", Provider: "codex", QuotaProvider: "codex", Email: "b@x"},
		{AuthIndex: "", Provider: "codex", Email: "no-index"},
		{AuthIndex: "4", Provider: "gemini", QuotaProvider: "Codex", Email: "c@x"},
		{AuthIndex: "5", Provider: "kimi", SupportsQuota: true},
	}
	var got []string
	for _, c := range pluginsQuotaCreds("codex", creds) {
		got = append(got, c.AuthIndex)
	}
	if strings.Join(got, ",") != "2,4" {
		t.Errorf("credentials for codex: %v, want 2,4 (any case, with an auth_index)", got)
	}
	got = nil
	for _, c := range pluginsQuotaCreds("", creds) {
		got = append(got, c.AuthIndex)
	}
	if strings.Join(got, ",") != "2,4,5" {
		t.Errorf("credentials for no provider: %v, want those with a quota", got)
	}
	if !pluginHasQuota(cpa.Plugin{QuotaProvider: "x"}) || pluginHasQuota(cpa.Plugin{}) {
		t.Error("pluginHasQuota")
	}
}
