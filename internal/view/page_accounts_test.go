package view

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/core"
	"github.com/licoy/cpa-bar/internal/cpa"
	"github.com/licoy/cpa-bar/internal/store"
)

type seenRequest struct {
	method, path, query, body string
}

// accountsCPA is a fake CPA with the routes of the accounts and OAuth
// pages, recording the writes it receives.
type accountsCPA struct {
	*httptest.Server

	mu   sync.Mutex
	seen []seenRequest

	// device makes the login a device-code flow.
	device bool
	// loginStatus is what /oauth/status answers.
	loginStatus string
	loginError  string
}

func (s *accountsCPA) requests(method, path string) []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []seenRequest
	for _, r := range s.seen {
		if r.method == method && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func newAccountsCPA(t *testing.T) *accountsCPA {
	t.Helper()
	s := &accountsCPA{loginStatus: cpa.OAuthWait}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.Path, "/v8/management")
		s.mu.Lock()
		if r.Method != http.MethodGet {
			s.seen = append(s.seen, seenRequest{r.Method, path, r.URL.RawQuery, string(body)})
		}
		status, errText, device := s.loginStatus, s.loginError, s.device
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "8.0.23")
		switch r.Method + " " + path {
		case "GET /config":
			_, _ = w.Write([]byte(`{"debug":false}`))
		case "GET /credentials":
			retry := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(`{"files":[
				{"id":"a.json","auth_index":"1","name":"a.json","provider":"claude","email":"ok@example.com","status":"active","success":12,"failed":1,"priority":3,"note":"main seat"},
				{"id":"b.json","auth_index":"2","name":"b.json","provider":"codex","email":"cool@example.com","status":"active","unavailable":true,"next_retry_after":"` + retry + `","status_message":"rate limited"},
				{"id":"c.json","auth_index":"3","name":"c.json","provider":"gemini","email":"off@example.com","status":"disabled","disabled":true},
				{"id":"d","auth_index":"4","name":"d","provider":"openai","label":"memory key","status":"active","runtime_only":true,"source":"memory"}
			]}`))
		case "PATCH /credentials/status", "PATCH /credentials/fields", "POST /credentials/refresh", "POST /routing/cooldown/reset", "DELETE /credentials", "DELETE /oauth/session":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "POST /oauth/callback":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "GET /credentials/download":
			// A credential file: tokens, and the fields the editor reads.
			_, _ = w.Write([]byte(`{"type":"codex","access_token":"SECRET-TOKEN","prefix":"team","proxy_url":"socks5://p.example:1080","headers":{"X-Org":"acme","X-Gone":"old"}}`))
		case "GET /credentials/models":
			_, _ = w.Write([]byte(`{"models":[{"id":"claude-sonnet","display_name":"Claude Sonnet","owned_by":"anthropic"}]}`))
		case "GET /oauth/auth-url":
			if device {
				_, _ = w.Write([]byte(`{"status":"ok","url":"https://auth.example.com/device","state":"st-device","flow":"device","user_code":"ABCD-1234","expires_in":600}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","url":"https://auth.example.com/authorize?state=st-code","state":"st-code"}`))
		case "GET /oauth/status":
			resp := map[string]string{"status": status}
			if errText != "" {
				resp["error"] = errText
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newAccountsApp(t *testing.T, s *accountsCPA) (*App, string) {
	t.Helper()
	svc, err := core.New(core.Options{
		Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)},
		Secrets:  &store.MemorySecrets{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	id, err := svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: s.URL, Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap, _ := svc.Snapshot(id); snap.HasData || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a := NewApp(svc)
	a.selected = id
	return a, id
}

// uiQueue lands the results of tasks on the test's goroutine, as the main
// loop does in the app (a test has none).
type uiQueue struct{ ch chan func() }

func installTestRun(t *testing.T) *uiQueue {
	t.Helper()
	q := &uiQueue{ch: make(chan func(), 256)}
	t.Cleanup(setRunOnMain(func(fn func()) { q.ch <- fn }))
	return q
}

func (q *uiQueue) drain() {
	for {
		select {
		case f := <-q.ch:
			f()
		default:
			return
		}
	}
}

// waitFor builds frames, landing task results, until cond holds.
func waitFor(t *testing.T, tt *ui.Tester, q *uiQueue, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.drain()
		tt.Advance(time.Since(tt.Now()))
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

func accountsTester(t *testing.T, page string) (*ui.Tester, *App, string, *accountsCPA, *uiQueue) {
	t.Helper()
	q := installTestRun(t)
	s := newAccountsCPA(t)
	a, id := newAccountsApp(t, s)
	a.page = page
	return ui.NewTester(a.managerView, managerWidth, managerHeight), a, id, s, q
}

func TestAccountsListsAccounts(t *testing.T) {
	tt, _, _, _, _ := accountsTester(t, "accounts")
	for _, s := range []string{
		"ok@example.com", "cool@example.com", "off@example.com", "memory key",
		"可用", "冷却中", "已停用", "rate limited", "仅内存", "main seat", "成功 12 · 失败 1", "目标实例：Home",
	} {
		if !tt.HasText(s) {
			t.Errorf("accounts page lacks %q; texts: %q", s, tt.Texts())
		}
	}
}

func TestAccountsSearchAndFilters(t *testing.T) {
	tt, a, id, _, _ := accountsTester(t, "accounts")
	st := StateFor(a, "accounts", id, newAccountsState)

	st.search = "COOL"
	tt.Frame()
	if !tt.HasText("cool@example.com") || tt.HasText("ok@example.com") || tt.HasText("off@example.com") {
		t.Errorf("search \"COOL\" shows %q", tt.Texts())
	}

	st.search = "rate limited" // the status message
	tt.Frame()
	if !tt.HasText("cool@example.com") || tt.HasText("ok@example.com") {
		t.Errorf("search by status message shows %q", tt.Texts())
	}

	st.search = "nothing-matches"
	tt.Frame()
	if !tt.HasText("没有符合条件的账号") {
		t.Errorf("empty result shows %q", tt.Texts())
	}

	st.search = ""
	st.health = filterDisabled
	tt.Frame()
	if !tt.HasText("off@example.com") || tt.HasText("ok@example.com") || tt.HasText("cool@example.com") {
		t.Errorf("disabled filter shows %q", tt.Texts())
	}

	st.health = filterAll
	st.provider = "claude"
	tt.Frame()
	if !tt.HasText("ok@example.com") || tt.HasText("cool@example.com") {
		t.Errorf("provider filter shows %q", tt.Texts())
	}
}

func TestFilterCredentials(t *testing.T) {
	retry := time.Now().Add(time.Hour)
	creds := []cpa.Credential{
		{Name: "a.json", Provider: "claude", Email: "A@x.io", Status: "active"},
		{Name: "b.json", Provider: "codex", Label: "Team", Status: "active", Unavailable: true, NextRetryAfter: &retry},
		{Name: "c.json", Type: "gemini", Account: "acct-7", Disabled: true},
		{Name: "d.json", Provider: "claude", Status: "error", StatusMessage: "invalid_grant"},
	}
	names := func(l []cpa.Credential) string {
		var out []string
		for _, c := range l {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		query, provider string
		health          int
		want            string
	}{
		{"", "", filterAll, "a.json,b.json,c.json,d.json"},
		{"a@x", "", filterAll, "a.json"},
		{"team", "", filterAll, "b.json"},
		{"ACCT-7", "", filterAll, "c.json"},
		{"gemini", "", filterAll, "c.json"},
		{"invalid", "", filterAll, "d.json"},
		{"", "claude", filterAll, "a.json,d.json"},
		{"", "", filterCooling, "b.json"},
		{"", "", filterError, "d.json"},
		{"", "", filterDisabled, "c.json"},
		{"", "", filterAvailable, "a.json"},
		{"", "claude", filterError, "d.json"},
	} {
		if got := names(filterCredentials(creds, c.query, c.provider, c.health)); got != c.want {
			t.Errorf("filter(%q, %q, %d) = %q, want %q", c.query, c.provider, c.health, got, c.want)
		}
	}
}

func TestAccountsToggleSendsPatch(t *testing.T) {
	tt, _, _, s, q := accountsTester(t, "accounts")
	tt.Frame()
	// Rows are sorted by need: the cooling account b.json comes first, and
	// is enabled.
	if err := tt.Click("启用"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the PATCH", func() bool { return len(s.requests("PATCH", "/credentials/status")) > 0 })
	var body map[string]any
	if err := json.Unmarshal([]byte(s.requests("PATCH", "/credentials/status")[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "b.json" || body["auth_index"] != "2" || body["disabled"] != true {
		t.Errorf("body = %v, want b.json/2 disabled", body)
	}
}

// chooseRowMenu opens the "More" menu of the first row and chooses an item.
func chooseRowMenu(t *testing.T, tt *ui.Tester, item string) {
	t.Helper()
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatalf("%v; menu: %q", err, tt.Menu())
	}
	tt.Frame()
}

func TestAccountsMoreMenuHoldsTheRareActions(t *testing.T) {
	tt, _, _, _, _ := accountsTester(t, "accounts")
	tt.Frame()
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	want := []string{"刷新 OAuth", "重置冷却", "模型", "下载", "-", "删除…"}
	if got := tt.Menu(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("menu = %q, want %q", got, want)
	}
	tt.CloseMenu()
	// The common actions stay on the row.
	for _, s := range []string{"编辑", "启用"} {
		if _, ok := tt.Find(s); !ok {
			t.Errorf("row lacks %q", s)
		}
	}
}

func TestAccountsDeleteAsksFirst(t *testing.T) {
	tt, _, _, s, q := accountsTester(t, "accounts")
	tt.Frame()
	chooseRowMenu(t, tt, "删除…")
	for _, want := range []string{"删除账号？", "Home", "cool@example.com"} {
		if !tt.HasText(want) {
			t.Errorf("delete dialog lacks %q; texts: %q", want, tt.Texts())
		}
	}
	if n := len(s.requests("DELETE", "/credentials")); n != 0 {
		t.Fatalf("%d deletes sent before the confirmation", n)
	}
	// Canceling sends nothing.
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("删除账号？") || len(s.requests("DELETE", "/credentials")) != 0 {
		t.Fatal("cancel did not close the dialog without deleting")
	}

	chooseRowMenu(t, tt, "删除…")
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the DELETE", func() bool { return len(s.requests("DELETE", "/credentials")) > 0 })
	if got := s.requests("DELETE", "/credentials")[0].query; got != "name=b.json" {
		t.Errorf("delete query = %q, want name=b.json", got)
	}
}

func TestAccountsBulkDeleteSelected(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "accounts")
	st := StateFor(a, "accounts", id, newAccountsState)
	tt.Frame()
	st.selected["1"], st.selected["3"] = true, true // a.json and c.json
	tt.Frame()
	if !tt.HasText("已选 2 个") {
		t.Fatalf("no selection shown; texts: %q", tt.Texts())
	}
	if err := tt.Click("删除所选 (2)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("删除账号？") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the DELETE", func() bool { return len(s.requests("DELETE", "/credentials")) > 0 })
	if got := s.requests("DELETE", "/credentials")[0].query; got != "name=a.json&name=c.json" && got != "name=c.json&name=a.json" {
		t.Errorf("delete query = %q", got)
	}
}

func TestAccountsRuntimeOnlyCannotBeDeleted(t *testing.T) {
	tt, a, id, _, _ := accountsTester(t, "accounts")
	st := StateFor(a, "accounts", id, newAccountsState)
	snap, _ := a.Svc.Snapshot(id)
	var mem []cpa.Credential
	for _, cr := range snap.Credentials {
		if cr.RuntimeOnly {
			mem = append(mem, cr)
		}
	}
	if len(mem) != 1 {
		t.Fatalf("memory-only accounts = %d", len(mem))
	}
	st.askDelete(mem, []string{"4"})
	if st.del.open {
		t.Error("a memory-only account can be asked to delete")
	}
	st.selected["4"] = true
	st.askDelete(snap.Credentials, nil)
	if st.del.open {
		t.Error("a memory-only account is deleted with the selection")
	}
	tt.Frame()
}

func TestAccountsEditSendsOnlyChangedFields(t *testing.T) {
	// The server writes every key but name into the credential file, so
	// auth_index must never be in the body.
	e := &accountEdit{name: "a.json", priority: "3", origPriority: "3", note: "main seat", origNote: "main seat"}
	fields, err := editFields(e)
	if err != nil || len(fields) != 1 || fields["name"] != "a.json" {
		t.Fatalf("unchanged edit = %v, %v", fields, err)
	}
	e.priority, e.note = " 7 ", ""
	fields, err = editFields(e)
	if err != nil {
		t.Fatal(err)
	}
	if fields["priority"] != int64(7) || fields["note"] != nil || len(fields) != 3 {
		t.Errorf("fields = %#v", fields)
	}
	if _, ok := fields["auth_index"]; ok {
		t.Error("auth_index in the edit body")
	}
	e.priority = ""
	fields, _ = editFields(e)
	if v, ok := fields["priority"]; !ok || v != nil {
		t.Errorf("clearing the priority = %#v", fields)
	}
	e.priority = "high"
	if _, err := editFields(e); err == nil {
		t.Error("a non-integer priority is accepted")
	}
}

func TestAccountsEditPrefixProxyHeaders(t *testing.T) {
	// Not read from the server yet: empty means unchanged, and what is
	// filled is sent.
	e := &accountEdit{name: "a.json"}
	if f, err := editFields(e); err != nil || len(f) != 1 {
		t.Fatalf("untouched edit = %v, %v", f, err)
	}
	e.prefix, e.proxy = " team ", "http://proxy.local:8080"
	e.headers = []headerRow{{id: 1, key: "X-Org", val: " acme "}, {id: 2, key: "X-Old", val: " "}, {id: 3}}
	f, err := editFields(e)
	if err != nil {
		t.Fatal(err)
	}
	hs, _ := f["headers"].(map[string]any)
	if f["prefix"] != "team" || f["proxy_url"] != "http://proxy.local:8080" || len(f) != 4 ||
		len(hs) != 2 || hs["X-Org"] != "acme" || hs["X-Old"] != "" {
		t.Errorf("fields = %#v", f)
	}
	if _, ok := f["auth_index"]; ok {
		t.Error("auth_index in the edit body")
	}

	// Read from the server: only changes are sent, and emptying clears.
	e = &accountEdit{name: "a.json"}
	e.applyLoaded(editableFields{prefix: "team", proxy: "socks5://p:1", headers: map[string]string{"X-Org": "acme", "X-Gone": "old", "X-Keep": "k"}})
	if !e.loaded || e.prefix != "team" || e.proxy != "socks5://p:1" || len(e.headers) != 3 {
		t.Fatalf("loaded edit = %+v", e)
	}
	if f, err := editFields(e); err != nil || len(f) != 1 {
		t.Fatalf("unchanged loaded edit = %v, %v", f, err)
	}
	e.prefix, e.proxy = "", "socks5://p:1"
	for i := range e.headers {
		switch e.headers[i].key {
		case "X-Org":
			e.headers[i].val = "globex"
		case "X-Gone":
			e.headers[i].val = ""
		}
	}
	e.addHeader()
	e.headers[len(e.headers)-1].key, e.headers[len(e.headers)-1].val = "X-New", "n"
	f, err = editFields(e)
	if err != nil {
		t.Fatal(err)
	}
	hs, _ = f["headers"].(map[string]any)
	if v, ok := f["prefix"]; !ok || v != "" || len(f) != 3 || len(hs) != 3 ||
		hs["X-Org"] != "globex" || hs["X-Gone"] != "" || hs["X-New"] != "n" {
		t.Errorf("fields = %#v", f)
	}
	if _, ok := f["proxy_url"]; ok {
		t.Error("an unchanged proxy is sent")
	}
	// Dropping a loaded row removes the header.
	e.headers = slices.DeleteFunc(e.headers, func(r headerRow) bool { return r.key == "X-Keep" })
	f, _ = editFields(e)
	if hs, _ = f["headers"].(map[string]any); hs["X-Keep"] != "" {
		t.Errorf("a dropped header is kept: %#v", hs)
	}

	// Bad rows are refused.
	for _, bad := range []headerRow{{key: "Bad Name", val: "v"}, {key: "", val: "v"}, {key: "X-Org", val: "again"}} {
		e = &accountEdit{name: "a.json", headers: []headerRow{{id: 1, key: "X-Org", val: "1"}, bad}}
		if _, err := editFields(e); err == nil {
			t.Errorf("header %+v accepted", bad)
		}
	}
}

func TestReadEditableFieldsKeepsOnlyThree(t *testing.T) {
	f, found := readEditableFields([]byte(`{"access_token":"SECRET","refresh_token":"R","prefix":" p ","proxy-url":"http://x","headers":{"A":"1","B":"","C":2}}`))
	if !found || f.prefix != "p" || f.proxy != "http://x" || len(f.headers) != 1 || f.headers["A"] != "1" {
		t.Errorf("fields = %+v, found %v", f, found)
	}
	if _, found := readEditableFields([]byte(`{"email":"a@b.c"}`)); found {
		t.Error("found fields in a credential without them")
	}
	if _, found := readEditableFields([]byte(`not json`)); found {
		t.Error("found fields in garbage")
	}
	// A server that reports them in the list is used.
	cr := cpa.Credential{Name: "x.json", Raw: []byte(`{"name":"x.json","prefix":"pp","headers":{"H":"v"}}`)}
	if e := newAccountEdit(cr); !e.loaded || e.prefix != "pp" || e.headers[0].key != "H" {
		t.Errorf("edit = %+v", e)
	}
}

func TestAccountsEditDialogPatches(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "accounts")
	tt.Frame()
	st := StateFor(a, "accounts", id, newAccountsState)
	// The first "编辑" is the cooling account's, b.json.
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !st.edit.open || st.edit.name != "b.json" {
		t.Fatalf("edit = %+v", st.edit)
	}
	for _, want := range []string{"编辑账号", "Home", "前缀", "代理地址", "自定义请求头", "读取当前值"} {
		if !tt.HasText(want) {
			t.Errorf("edit dialog lacks %q; texts: %q", want, tt.Texts())
		}
	}
	st.edit.priority, st.edit.note = "9", "spare"
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the PATCH", func() bool { return len(s.requests("PATCH", "/credentials/fields")) > 0 })
	var body map[string]any
	if err := json.Unmarshal([]byte(s.requests("PATCH", "/credentials/fields")[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "b.json" || body["priority"] != float64(9) || body["note"] != "spare" || len(body) != 3 {
		t.Errorf("body = %v", body)
	}
	waitFor(t, tt, q, "the dialog to close", func() bool { return !st.edit.open })
}

func TestAccountsEditDialogReadsAndPatchesMetadata(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "accounts")
	tt.Frame()
	st := StateFor(a, "accounts", id, newAccountsState)
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Nothing changed yet: Save does nothing.
	_ = tt.Click("保存")
	tt.Frame()
	if n := len(s.requests("PATCH", "/credentials/fields")); n != 0 {
		t.Fatalf("%d patches for an unchanged edit", n)
	}

	if err := tt.Click("读取当前值"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the values", func() bool { return st.edit.loaded })
	if st.edit.prefix != "team" || st.edit.proxy != "socks5://p.example:1080" || len(st.edit.headers) != 2 {
		t.Fatalf("edit = %+v", st.edit)
	}
	if !tt.HasText("已读取服务器上的当前值") {
		t.Errorf("no loaded note; texts: %q", tt.Texts())
	}
	for _, r := range st.edit.headers {
		if strings.Contains(r.key+r.val, "SECRET") {
			t.Error("a token reached the editor")
		}
	}

	// Change the prefix, edit one header, drop the other, add a third.
	st.edit.prefix = "squad"
	for i := range st.edit.headers {
		if st.edit.headers[i].key == "X-Org" {
			st.edit.headers[i].val = "globex"
		}
	}
	st.edit.headers = slices.DeleteFunc(st.edit.headers, func(r headerRow) bool { return r.key == "X-Gone" })
	st.edit.addHeader()
	st.edit.headers[len(st.edit.headers)-1].key, st.edit.headers[len(st.edit.headers)-1].val = "X-Team", "red"
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the PATCH", func() bool { return len(s.requests("PATCH", "/credentials/fields")) > 0 })
	var body map[string]any
	if err := json.Unmarshal([]byte(s.requests("PATCH", "/credentials/fields")[0].body), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "b.json", "prefix": "squad", "headers": map[string]any{"X-Org": "globex", "X-Gone": "", "X-Team": "red"}}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	waitFor(t, tt, q, "the dialog to close", func() bool { return !st.edit.open })
}

func TestAccountsModelsDialog(t *testing.T) {
	tt, _, _, _, q := accountsTester(t, "accounts")
	tt.Frame()
	chooseRowMenu(t, tt, "模型")
	waitFor(t, tt, q, "the models", func() bool { return tt.HasText("claude-sonnet") })
	for _, want := range []string{"账号模型", "Claude Sonnet", "anthropic"} {
		if !tt.HasText(want) {
			t.Errorf("models dialog lacks %q; texts: %q", want, tt.Texts())
		}
	}
}

func TestAccountsRefreshAllAsksFirst(t *testing.T) {
	tt, _, _, s, q := accountsTester(t, "accounts")
	tt.Frame()
	if err := tt.Click("刷新全部 OAuth"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("刷新全部 OAuth？") || !tt.HasText("将让实例“Home”刷新所有 OAuth 账号的令牌。这会向各服务商发起请求。") {
		t.Errorf("confirmation texts: %q", tt.Texts())
	}
	if n := len(s.requests("POST", "/credentials/refresh")); n != 0 {
		t.Fatalf("%d refreshes sent before the confirmation", n)
	}
	if err := tt.Click("确认刷新"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the refresh", func() bool { return len(s.requests("POST", "/credentials/refresh")) > 0 })
	if got := s.requests("POST", "/credentials/refresh")[0].body; !strings.Contains(got, `"all":true`) {
		t.Errorf("body = %s", got)
	}
}

func TestAccountsStaleDisablesStateWrites(t *testing.T) {
	tt, a, id, s, _ := accountsTester(t, "accounts")
	tt.Frame()
	s.Close() // the next refresh fails; the data turns stale
	a.Svc.Refresh(id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap, _ := a.Svc.Snapshot(id); snap.Stale || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	if !tt.HasText("可能已过期") {
		t.Fatalf("no stale notice; texts: %q", tt.Texts())
	}
	// A disabled menu item cannot be chosen.
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("删除…"); err == nil {
		t.Error("the delete item is enabled on stale data")
	}
	tt.CloseMenu()
	tt.Frame()
	if tt.HasText("删除账号？") {
		t.Error("delete can be asked for on stale data")
	}
}

func TestParsePriorityAndText(t *testing.T) {
	if p, err := parsePriority(" 12 "); err != nil || *p != 12 {
		t.Errorf("parsePriority = %v, %v", p, err)
	}
	if p, err := parsePriority(""); err != nil || p != nil {
		t.Errorf("empty priority = %v, %v", p, err)
	}
	if _, err := parsePriority("1.5"); err == nil {
		t.Error("fractional priority accepted")
	}
	f := 3.0
	if priorityText(&f) != "3" || priorityText(nil) != "" {
		t.Error("priorityText")
	}
}

func TestWritePrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cred.json")
	if err := writePrivateFile(path, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, path)
}

// ---- OAuth ----

func TestOAuthShowsURLAfterStart(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "oauth")
	tt.Frame()
	for _, want := range []string{"选择服务商", "Home", "登录在你本机的浏览器中完成"} {
		if !tt.HasText(want) {
			t.Errorf("oauth page lacks %q; texts: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatalf("%v; texts: %q", err, tt.Texts())
	}
	waitFor(t, tt, q, "the login link", func() bool {
		return tt.HasText("https://auth.example.com/authorize?state=st-code")
	})
	for _, want := range []string{"在浏览器中打开", "复制链接", "等待你在浏览器中完成登录", "提交回调 URL", "取消登录"} {
		if !tt.HasText(want) {
			t.Errorf("login lacks %q; texts: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("复制链接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != "https://auth.example.com/authorize?state=st-code" {
		t.Errorf("copied %q", got)
	}
	if err := tt.Click("在浏览器中打开"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://auth.example.com/authorize?state=st-code" {
		t.Errorf("opened %q", got)
	}

	// The login is polled; the server finishing it ends the wait and
	// refreshes the accounts.
	st := StateFor(a, "oauth", id, newOAuthState)
	s.mu.Lock()
	s.loginStatus = cpa.OAuthOK
	s.mu.Unlock()
	st.lastPoll = time.Time{} // the next frame polls
	waitFor(t, tt, q, "the success message", func() bool { return tt.HasText("登录成功，凭证已保存到实例“Home”。") })
	if !tt.HasText("查看账号") {
		t.Errorf("no way on after success; texts: %q", tt.Texts())
	}
}

func TestOAuthPollsOnlyAfterInterval(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "oauth")
	tt.Frame()
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the login link", func() bool { return tt.HasText("取消登录") })
	st := StateFor(a, "oauth", id, newOAuthState)
	// Right after the start nothing is polled yet (the interval is 2s).
	for range 5 {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if st.poll.Running || st.poll.Done {
		t.Error("polled before the interval passed")
	}
	_ = s
}

func TestOAuthFailureShowsServerError(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "oauth")
	tt.Frame()
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the login", func() bool { return tt.HasText("取消登录") })
	s.mu.Lock()
	s.loginStatus, s.loginError = cpa.OAuthError, "State code error"
	s.mu.Unlock()
	StateFor(a, "oauth", id, newOAuthState).lastPoll = time.Time{}
	waitFor(t, tt, q, "the failure", func() bool { return tt.HasText("登录失败") })
	if !tt.HasText("State code error") {
		t.Errorf("server error not shown; texts: %q", tt.Texts())
	}
}

func TestOAuthDeviceCode(t *testing.T) {
	tt, _, _, s, q := accountsTester(t, "oauth")
	s.mu.Lock()
	s.device = true
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the code", func() bool { return tt.HasText("ABCD-1234") })
	for _, want := range []string{"复制验证码", "剩余时间 ", "https://auth.example.com/device", "在浏览器中打开"} {
		if !tt.HasText(want) {
			t.Errorf("device login lacks %q; texts: %q", want, tt.Texts())
		}
	}
	if tt.HasText("提交回调 URL") {
		t.Error("a device flow offers a callback URL")
	}
	if err := tt.Click("复制验证码"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.Clipboard() != "ABCD-1234" {
		t.Errorf("copied %q", tt.Clipboard())
	}
}

func TestOAuthCancel(t *testing.T) {
	tt, _, _, s, q := accountsTester(t, "oauth")
	// The provider picker pushes the login's steps down: show them all.
	tt.SetSize(managerWidth, 1200)
	tt.Frame()
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the login", func() bool { return tt.HasText("取消登录") })
	if err := tt.Click("取消登录"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the cancel", func() bool { return tt.HasText("登录已取消。") })
	if got := s.requests("DELETE", "/oauth/session"); len(got) != 1 || got[0].query != "state=st-code" {
		t.Errorf("cancel requests = %+v", got)
	}
}

func TestOAuthSubmitCallback(t *testing.T) {
	tt, a, id, s, q := accountsTester(t, "oauth")
	// The provider picker pushes the login's steps down: show them all.
	tt.SetSize(managerWidth, 1200)
	tt.Frame()
	if err := tt.Click("开始登录 claude"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the login", func() bool { return tt.HasText("提交回调 URL") })
	st := StateFor(a, "oauth", id, newOAuthState)

	st.callback = "not a url"
	tt.Frame()
	if !tt.HasText("这不像完整的回调网址") {
		t.Errorf("invalid URL not flagged; texts: %q", tt.Texts())
	}
	_ = tt.Click("提交")
	tt.Frame()
	if n := len(s.requests("POST", "/oauth/callback")); n != 0 {
		t.Fatalf("%d callbacks sent for an invalid URL", n)
	}

	st.callback = "http://localhost:54545/callback?code=abc&state=st-code"
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the callback", func() bool { return len(s.requests("POST", "/oauth/callback")) > 0 })
	var body map[string]string
	if err := json.Unmarshal([]byte(s.requests("POST", "/oauth/callback")[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if body["redirect_url"] != "http://localhost:54545/callback?code=abc&state=st-code" || body["provider"] != "claude" {
		t.Errorf("body = %v", body)
	}
	waitFor(t, tt, q, "the submitted note", func() bool { return tt.HasText("已提交，等待服务器完成登录") })
	if st.callback != "" {
		t.Error("the pasted URL is kept after submitting")
	}
}

func TestOAuthPluginProviderWins(t *testing.T) {
	st := newOAuthState()
	st.provider = oauthProviderLabel("codex")
	if got := st.effectiveProvider(); got != "codex" {
		t.Errorf("provider = %q", got)
	}
	st.custom = "  My-Plugin "
	if got := st.effectiveProvider(); got != "my-plugin" {
		t.Errorf("plugin provider = %q", got)
	}
}

func TestOAuthURLChecks(t *testing.T) {
	for u, want := range map[string]bool{
		"https://a.example/x?y=1": true,
		"http://localhost:1/":     true,
		"file:///etc/passwd":      false,
		"javascript:alert(1)":     false,
		"":                        false,
		"https://":                false,
	} {
		if got := safeWebURL(u); got != want {
			t.Errorf("safeWebURL(%q) = %v", u, got)
		}
	}
	for u, want := range map[string]bool{
		"http://localhost:1455/auth/callback?code=a&state=b": true,
		"https://x.example/cb#code=a":                        true,
		"http://localhost:1455/auth/callback":                false,
		"localhost:1455/?code=a":                             false,
		"ftp://x/?code=a":                                    false,
	} {
		if got := validCallbackURL(u); got != want {
			t.Errorf("validCallbackURL(%q) = %v", u, got)
		}
	}
	if got := oauthClock(125 * time.Second); got != "2:05" {
		t.Errorf("clock = %q", got)
	}
}

func checkPrivate(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("%s has mode %v, want owner-only", path, fi.Mode().Perm())
	}
}

func TestPinnedFirst(t *testing.T) {
	creds := []cpa.Credential{{Name: "a"}, {Name: "b"}, {Name: "c"}, {ID: "d"}}
	names := func(cs []cpa.Credential) []string {
		var out []string
		for _, c := range cs {
			out = append(out, credFileName(c))
		}
		return out
	}
	if got := names(pinnedFirst(creds, nil)); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("no pins = %v", got)
	}
	// Pinned in the order they were pinned; a pin of a gone account is
	// ignored; the rest keep their order.
	if got := names(pinnedFirst(creds, []string{"d", "gone", "b"})); !slices.Equal(got, []string{"d", "b", "a", "c"}) {
		t.Errorf("pinned = %v", got)
	}
	if got := names(creds); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("input reordered: %v", got)
	}
}

func TestAccountsPinToTop(t *testing.T) {
	tt, a, id, _, _ := accountsTester(t, "accounts")
	above := func(tt *ui.Tester, x, y string) bool {
		rx, okx := tt.Find(x)
		ry, oky := tt.Find(y)
		return okx && oky && rx.Y < ry.Y
	}
	if !above(tt, "cool@example.com", "off@example.com") {
		t.Fatalf("unpinned order: %q", tt.Texts())
	}

	// The pin of the first row, the cooling account, pins it.
	if err := tt.Click("置顶"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.Svc.Settings().Instances[0].PinnedAccounts; !slices.Equal(got, []string{"b.json"}) {
		t.Fatalf("pinned = %v", got)
	}

	// A pinned account comes first in the accounts list and the panel.
	if err := a.Svc.SetCredentialPinned(id, "c.json", true); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !above(tt, "cool@example.com", "off@example.com") || !above(tt, "off@example.com", "ok@example.com") {
		t.Errorf("pinned order: %q", tt.Texts())
	}
	a.panelInstance = id
	pt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	if !above(pt, "cool@example.com", "off@example.com") || !above(pt, "off@example.com", "ok@example.com") {
		t.Errorf("panel order: %q", pt.Texts())
	}
	if _, ok := pt.Find("已置顶"); !ok {
		t.Errorf("panel does not mark pinned accounts: %q", pt.Texts())
	}

	// Unpinning puts it back in its place.
	if err := tt.Click("取消置顶"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.Svc.Settings().Instances[0].PinnedAccounts; !slices.Equal(got, []string{"c.json"}) {
		t.Fatalf("pinned after unpin = %v", got)
	}
	if err := a.Svc.SetCredentialPinned(id, "c.json", false); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !above(tt, "ok@example.com", "off@example.com") {
		t.Errorf("unpinned order: %q", tt.Texts())
	}
}
