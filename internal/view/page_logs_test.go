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

// ---- helpers shared by the logs and plugins page tests ----

// pageFake is a CPA that records every request. Routes it does not know
// itself go to extra, which answers the page under test.
type pageFake struct {
	mu   sync.Mutex
	reqs []pageReq
	// extra answers a request, returning false for an unknown route.
	extra func(w http.ResponseWriter, r *http.Request, body []byte) bool
	// version is the X-CPA-VERSION header.
	version string
	plugin  string
}

type pageReq struct {
	Method, Path, Query string
	Body                string
}

func (f *pageFake) record(r *http.Request, body []byte) {
	f.mu.Lock()
	f.reqs = append(f.reqs, pageReq{r.Method, strings.TrimPrefix(r.URL.Path, "/v8/management"), r.URL.RawQuery, string(body)})
	f.mu.Unlock()
}

// requests returns the recorded requests of method and path.
func (f *pageFake) requests(method, path string) []pageReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pageReq
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (f *pageFake) count(method, path string) int { return len(f.requests(method, path)) }

func (f *pageFake) all() []pageReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pageReq(nil), f.reqs...)
}

func (f *pageFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.record(r, body)
	w.Header().Set("Content-Type", "application/json")
	v := f.version
	if v == "" {
		v = "8.0.23"
	}
	w.Header().Set("X-CPA-VERSION", v)
	if f.plugin != "" {
		w.Header().Set("X-CPA-SUPPORT-PLUGIN", f.plugin)
	}
	switch strings.TrimPrefix(r.URL.Path, "/v8/management") {
	case "/config":
		_, _ = w.Write([]byte(`{"debug":false}`))
		return
	case "/credentials":
		_, _ = w.Write([]byte(`{"files":[{"id":"a.json","auth_index":"7","name":"a.json","provider":"claude","email":"ok@example.com","status":"active"}]}`))
		return
	}
	if f.extra != nil && f.extra(w, r, body) {
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}`))
}

// newPageApp is a test app with one instance, "Home", on the fake.
func newPageApp(t testing.TB, f *pageFake) (*App, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	svc, err := core.New(core.Options{
		Settings: store.File{Path: filepath.Join(t.TempDir(), store.FileName)},
		Secrets:  &store.MemorySecrets{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	id, err := svc.AddInstance(context.Background(), core.InstanceInput{Name: "Home", Address: srv.URL, Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s, _ := svc.Snapshot(id); s.HasData || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a := NewApp(svc)
	a.selected = id
	return a, id
}

// pageQueue replaces Run in tests: the outcome of a task is applied by the
// test, on its own goroutine, like the main thread would.
type pageQueue struct {
	mu  sync.Mutex
	fns []func()
}

func (q *pageQueue) push(fn func()) {
	q.mu.Lock()
	q.fns = append(q.fns, fn)
	q.mu.Unlock()
}

func (q *pageQueue) drain() {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func newPageQueue(t *testing.T) *pageQueue {
	t.Helper()
	q := &pageQueue{}
	t.Cleanup(setRunOnMain(q.push))
	return q
}

// pump builds frames, applying finished tasks, until cond holds; it fails
// the test after timeout.
func pump(t *testing.T, tt *ui.Tester, q *pageQueue, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
		time.Sleep(20 * time.Millisecond)
	}
}

// settle builds frames for d, applying finished tasks.
func settle(tt *ui.Tester, q *pageQueue, d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		q.drain()
		tt.Advance(time.Since(tt.Now()))
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
}

func noUsageQueue(t *testing.T, f *pageFake) {
	t.Helper()
	for _, r := range f.all() {
		if strings.Contains(r.Path, "usage/queue") {
			t.Errorf("the destructive usage queue was requested: %+v", r)
		}
	}
}

// ---- logs page ----

// liveHas reports whether the live log's text area holds s.
func liveHas(a *App, id, s string) bool {
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} })
	return strings.Contains(st.doc.text, s)
}

// viewerHas reports whether the viewer dialog's text area holds s.
func viewerHas(a *App, id, s string) bool {
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} })
	return st.viewer.open && st.viewer.doc != nil && strings.Contains(st.viewer.doc.text, s)
}

func logsFake(t *testing.T) *pageFake {
	f := &pageFake{}
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		path := strings.TrimPrefix(r.URL.Path, "/v8/management")
		switch {
		case path == "/observability/logs" && r.Method == http.MethodGet:
			switch r.URL.Query().Get("cursor") {
			case "":
				_, _ = w.Write([]byte(`{"lines":["first line","second line [error] boom"],"line-count":2,"latest-timestamp":1,"next-cursor":"c1"}`))
			case "c1":
				_, _ = w.Write([]byte(`{"lines":["third line"],"line-count":1,"latest-timestamp":2,"next-cursor":"c2"}`))
			default:
				_, _ = w.Write([]byte(`{"lines":[],"line-count":0,"latest-timestamp":2,"next-cursor":"` + r.URL.Query().Get("cursor") + `"}`))
			}
		case path == "/observability/logs" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"success":true,"removed":1}`))
		case path == "/observability/logs/errors":
			_, _ = w.Write([]byte(`{"files":[{"name":"error-v1-2026.log","size":2048,"modified":1700000000}]}`))
		case path == "/observability/logs/errors/error-v1-2026.log":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("request failed\nupstream said 500\n"))
		case path == "/server/latest-version":
			_, _ = w.Write([]byte(`{"latest-version":"v8.0.99"}`))
		case path == "/requests/api-call":
			_, _ = w.Write([]byte(`{"status_code":200,"header":{"Content-Type":["application/json"]},"body":"{\"ok\":true}"}`))
		default:
			return false
		}
		return true
	}
	return f
}

func TestLogsPageIncrementalAndOnlyWhileShown(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)

	pump(t, tt, q, 5*time.Second, "the first read", func() bool { return f.count("GET", "/observability/logs") >= 1 })
	pump(t, tt, q, 5*time.Second, "the first lines", func() bool { return liveHas(a, id, "first line") })
	first := f.requests("GET", "/observability/logs")[0]
	if strings.Contains(first.Query, "cursor") {
		t.Errorf("the first read carries a cursor: %q", first.Query)
	}

	// The next read comes after about 3 s and carries the cursor.
	pump(t, tt, q, 8*time.Second, "the second read", func() bool { return f.count("GET", "/observability/logs") >= 2 })
	second := f.requests("GET", "/observability/logs")[1]
	if !strings.Contains(second.Query, "cursor=c1") {
		t.Errorf("the second read has query %q, want cursor=c1", second.Query)
	}
	pump(t, tt, q, 5*time.Second, "the appended line", func() bool { return liveHas(a, id, "third line") && liveHas(a, id, "first line") })
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{} })
	if len(st.lines) != 3 || st.cursor != "c2" {
		t.Errorf("buffer %q cursor %q", st.lines, st.cursor)
	}

	// Another page: nothing polls any more.
	a.page = "overview"
	tt.Frame()
	n := f.count("GET", "/observability/logs")
	settle(tt, q, 3800*time.Millisecond)
	if got := f.count("GET", "/observability/logs"); got != n {
		t.Errorf("%d log reads after leaving the page, want %d", got, n)
	}
	noUsageQueue(t, f)
}

func TestLogsPagePause(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the lines", func() bool { return liveHas(a, id, "first line") })
	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("继续") {
		t.Fatalf("no resume button; texts: %q", tt.Texts())
	}
	n := f.count("GET", "/observability/logs")
	settle(tt, q, 3500*time.Millisecond)
	if got := f.count("GET", "/observability/logs"); got != n {
		t.Errorf("%d reads while paused, want %d", got, n)
	}
}

func TestLogsPageClearAsksFirst(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the lines", func() bool { return liveHas(a, id, "first line") })

	if err := tt.Click("清空日志"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Home") || !tt.HasText("不可恢复") {
		t.Fatalf("the confirmation does not name the instance; texts: %q", tt.Texts())
	}
	if n := f.count("DELETE", "/observability/logs"); n != 0 {
		t.Fatalf("%d DELETE before confirming", n)
	}
	// Cancel: still nothing.
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	settle(tt, q, 200*time.Millisecond)
	if n := f.count("DELETE", "/observability/logs"); n != 0 {
		t.Fatalf("%d DELETE after canceling", n)
	}

	if err := tt.Click("清空日志"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the DELETE", func() bool { return f.count("DELETE", "/observability/logs") == 1 })
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{} })
	pump(t, tt, q, 5*time.Second, "the buffer reset", func() bool { return !st.clear.Running })
	if st.cursor != "" && st.cursor != "c1" {
		t.Errorf("cursor %q after clearing", st.cursor)
	}
	if n := f.count("DELETE", "/observability/logs"); n != 1 {
		t.Errorf("%d DELETE, want 1", n)
	}
}

func TestLogsPageDisabledExplainsHowToEnable(t *testing.T) {
	f := &pageFake{}
	f.extra = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if strings.HasSuffix(r.URL.Path, "/observability/logs") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"logging to file disabled"}`))
			return true
		}
		return false
	}
	q := newPageQueue(t)
	a, _ := newPageApp(t, f)
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the notice", func() bool { return tt.HasText("logging-to-file: true") })
	if tt.HasText("HTTP 400") {
		t.Errorf("a generic error is shown; texts: %q", tt.Texts())
	}
	n := f.count("GET", "/observability/logs")
	settle(tt, q, 500*time.Millisecond)
	if got := f.count("GET", "/observability/logs"); got != n {
		t.Errorf("polling goes on while logging is off: %d vs %d", got, n)
	}
}

func TestLogsPageFilterAndCopy(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the lines", func() bool { return liveHas(a, id, "second line") })
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{} })
	st.filter = "BOOM"
	tt.Frame()
	if liveHas(a, id, "first line") || !liveHas(a, id, "second line") {
		t.Errorf("filter not applied; texts: %q", tt.Texts())
	}
	if err := tt.Click("复制可见日志"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != "second line [error] boom" {
		t.Errorf("copied %q", got)
	}
}

func TestLogsPageErrorFilesAndViewer(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} })
	st.tab = logsTabFiles
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the file list", func() bool { return tt.HasText("error-v1-2026.log") })
	if tt.HasText("2.0 KB") == false {
		t.Errorf("size missing; texts: %q", tt.Texts())
	}
	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the viewer", func() bool { return viewerHas(a, id, "upstream said 500") })
	// The files tab never reads the live log.
	if n := f.count("GET", "/observability/logs"); n != 0 {
		t.Errorf("%d live log reads from the files tab", n)
	}
}

func TestLogsPageDiagnostics(t *testing.T) {
	f := logsFake(t)
	q := newPageQueue(t)
	a, id := newPageApp(t, f)
	a.page = "logs"
	st := StateFor(a, "logs", id, func() *logsState { return &logsState{apiMethod: "GET"} })
	st.tab = logsTabDiag
	tt := ui.NewTester(a.managerView, managerWidth, 2200) // tall: the whole form is on screen
	tt.Frame()
	if !tt.HasText("此请求由“Home”服务器发出") {
		t.Errorf("no explanation that the server sends the call; texts: %q", tt.Texts())
	}

	if err := tt.Click("检查最新版本"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the version answer", func() bool { return tt.HasText("有新版本 v8.0.99") })

	// A call with a credential: GET goes out at once.
	st.apiURL = "https://api.example.com/v1/ping"
	st.apiHeaders = "Authorization: Bearer $TOKEN$\nX-Test: 1"
	st.apiCred = "ok@example.com · Claude (Anthropic) · #7"
	tt.Frame()
	if err := tt.Click("通过“Home”发送"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the api call", func() bool { return tt.HasText("上游返回 HTTP 200") })
	calls := f.requests("POST", "/requests/api-call")
	if len(calls) != 1 {
		t.Fatalf("%d api calls", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	hdr, _ := body["header"].(map[string]any)
	if body["method"] != "GET" || body["url"] != "https://api.example.com/v1/ping" || body["auth_index"] != "7" ||
		hdr["Authorization"] != "Bearer $TOKEN$" || hdr["X-Test"] != "1" {
		t.Errorf("payload %v", body)
	}
	if r := st.apiResult; r == nil || r.doc == nil || !strings.Contains(r.doc.text, `"ok": true`) {
		t.Errorf("body not pretty printed; texts: %q", tt.Texts())
	}

	// A POST asks first, naming the instance.
	st.apiMethod = "POST"
	tt.Frame()
	if err := tt.Click("通过“Home”发送"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := f.count("POST", "/requests/api-call"); n != 1 {
		t.Fatalf("%d api calls before confirming a POST", n)
	}
	if !tt.HasText("发送请求？") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the second call", func() bool { return f.count("POST", "/requests/api-call") == 2 })
	noUsageQueue(t, f)
}

func TestLogsHelpers(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"8.0.23", "v8.0.24", -1}, {"v8.0.23", "8.0.23", 0}, {"8.1.0", "8.0.99", 1},
		{"8.0.23-rc1", "8.0.23", 0}, {"8.0", "8.0.1", -1}, {"9", "8.9.9", 1},
	} {
		if got := logsCompareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	h, err := logsParseHeaders("A: 1\n\n B : x: y \r\n")
	if err != nil || h["A"] != "1" || h["B"] != "x: y" || len(h) != 2 {
		t.Errorf("headers %v %v", h, err)
	}
	if _, err := logsParseHeaders("no colon"); err == nil {
		t.Error("a header line without colon is accepted")
	}
	files, err := logsParseErrorLogs(json.RawMessage(`{"files":[{"name":"a","size":1,"modified":1},{"name":"b","size":2,"modified":9}]}`))
	if err != nil || len(files) != 2 || files[0].Name != "b" {
		t.Errorf("files %v %v", files, err)
	}
	if files, err := logsParseErrorLogs(json.RawMessage(`[{"name":"z"}]`)); err != nil || len(files) != 1 {
		t.Errorf("bare array: %v %v", files, err)
	}
	lines, note := logsSplit([]byte(strings.Repeat("x\n", logsViewMaxLines+10)))
	if len(lines) != logsViewMaxLines || note == "" {
		t.Errorf("%d lines, note %q", len(lines), note)
	}
	r := logsParseAPIResult(json.RawMessage(`{"status_code":404,"header":{"B":["1","2"],"A":["x"]},"body":"plain"}`))
	if r.status != 404 || r.lines[0] != "A: x" || r.lines[1] != "B: 1, 2" || r.lines[len(r.lines)-1] != "plain" {
		t.Errorf("result %+v", r)
	}
	if logsBytes(1536) != "1.5 KB" || logsBytes(10) != "10 B" {
		t.Errorf("bytes %q %q", logsBytes(1536), logsBytes(10))
	}
}

func TestLogsBufferCap(t *testing.T) {
	st := &logsState{}
	lines := make([]string, logsMaxLines+50)
	for i := range lines {
		lines[i] = "l"
	}
	lines[len(lines)-1] = "last"
	st.applyFetch(&cpa.LogPage{Lines: lines, NextCursor: "c"}, nil, "", logsTailLimit)
	if len(st.lines) != logsMaxLines || st.lines[len(st.lines)-1] != "last" {
		t.Errorf("buffer %d", len(st.lines))
	}
	// A cursor reset replaces the buffer.
	st.applyFetch(&cpa.LogPage{Lines: []string{"fresh"}, NextCursor: "d", CursorReset: true}, nil, "c", logsDeltaLimit)
	if len(st.lines) != 1 || st.cursor != "d" {
		t.Errorf("after reset: %d lines, cursor %q", len(st.lines), st.cursor)
	}
	st.applyFetch(&cpa.LogPage{Lines: []string{"more"}, NextCursor: "e"}, nil, "d", logsDeltaLimit)
	if len(st.lines) != 2 || st.cursor != "e" {
		t.Errorf("after append: %d lines, cursor %q", len(st.lines), st.cursor)
	}
}
