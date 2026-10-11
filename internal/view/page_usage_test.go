package view

import (
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/store"
	"github.com/licoy/cpa-bar/internal/usage"
)

// usageApp is a test app on its Usage page, with a usage manager in a
// temporary directory.
func usageApp(t *testing.T, withInstance bool) (*App, string) {
	t.Helper()
	a, id := newTestApp(t, withInstance)
	a.Usage = usage.New(usage.Options{Dir: t.TempDir()})
	t.Cleanup(a.Usage.Close)
	a.page = "usage"
	return a, id
}

// urec is a request ago ago, of model "model" through claude.
func urec(ago time.Duration, model string, mod func(*usage.Record)) usage.Record {
	r := usage.Record{
		Time:      time.Now().Add(-ago),
		RequestID: fmt.Sprintf("req-%s-%d", model, ago),
		Provider:  "claude",
		Model:     model,
		Source:    "alice@example.com",
		KeyID:     "k1",
		KeyMask:   "••••9f2c",
		LatencyMs: 1200,
		TTFTMs:    300,
		Tokens:    usage.Tokens{Input: 1000, Output: 200, CacheRead: 400, Total: 1200},
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

func addUsage(t *testing.T, a *App, id string, recs ...usage.Record) {
	t.Helper()
	if err := a.Usage.Add(id, recs...); err != nil {
		t.Fatal(err)
	}
}

func TestUsagePageShowsSummary(t *testing.T) {
	a, id := usageApp(t, true)
	addUsage(t, a, id,
		urec(10*time.Minute, "claude-sonnet-4", nil),
		urec(20*time.Minute, "claude-sonnet-4", nil),
		urec(30*time.Minute, "gpt-5", func(r *usage.Record) {
			r.Provider = "codex"
			r.Alias = "my-gpt"
			r.Failed, r.Status, r.Error = true, 429, "rate limited for bob@corp.example.com"
			r.KeyID, r.KeyMask = "", ""
		}),
	)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	for _, want := range []string{
		"用量", "请求数", "3", "成功 2 · 失败 1 · 取消 0", "成功率", "66.7%", "Token 总量", "3.6K",
		"缓存命中率", "40.0%", "平均延迟", "1.2 s", "Token 趋势", "Token 构成", "请求明细", "共 3 条", "第 1–3 条，共 3 条",
		"claude-sonnet-4", "my-gpt", "gpt-5", "••••9f2c", "Claude (Anthropic)", "alice@example.com",
		"HTTP 429 · rate limited for bob@corp.example.com", "失败", "成功", "已关闭，可在“设置”中开启。",
	} {
		if !tt.HasText(want) {
			t.Errorf("usage page lacks %q; texts: %q", want, tt.Texts())
		}
	}
	// 2 requests of one model: 2,400 tokens, 2 requests.
	if !tt.HasText("2.4K Token") || !tt.HasText("2 次请求") {
		t.Errorf("model panel lacks its figures; texts: %q", tt.Texts())
	}
}

func TestUsagePageKeepsRawNamesPrivate(t *testing.T) {
	a, id := usageApp(t, true)
	if err := a.Svc.UpdateSettings(func(s *store.Settings) { s.Privacy = true }); err != nil {
		t.Fatal(err)
	}
	SetPrivacy(true)
	defer SetPrivacy(false)
	addUsage(t, a, id, urec(time.Minute, "claude-sonnet-4", func(r *usage.Record) {
		r.Failed, r.Status, r.Error = true, 500, "upstream denied bob@corp.example.com"
	}))
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	for _, s := range tt.Texts() {
		if strings.Contains(s, "alice@example.com") || strings.Contains(s, "bob@corp.example.com") {
			t.Errorf("privacy mode shows %q", s)
		}
	}
	if !tt.HasText(PrivName("alice@example.com")) || !tt.HasText("HTTP 500 · upstream denied "+PrivName("bob@corp.example.com")) {
		t.Errorf("masked names are missing; texts: %q", tt.Texts())
	}
	if !tt.HasText("••••9f2c") {
		t.Errorf("the masked key is missing; texts: %q", tt.Texts())
	}
}

func TestUsageFilters(t *testing.T) {
	a, id := usageApp(t, true)
	addUsage(t, a, id,
		urec(10*time.Minute, "claude-sonnet-4", nil),
		urec(20*time.Minute, "claude-sonnet-4", nil),
		urec(30*time.Minute, "gpt-5", func(r *usage.Record) {
			r.Provider, r.Source = "codex", "carol@example.com"
			r.Failed, r.Status = true, 500
		}),
		urec(40*time.Minute, "gpt-5", func(r *usage.Record) { r.Canceled, r.Failed, r.Status = true, true, 499 }),
		urec(3*24*time.Hour, "old-model", nil),
	)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	st := StateFor(a, "usage", id, newUsageState)
	if !tt.HasText("共 4 条") || tt.HasText("old-model") {
		t.Errorf("24 hours show %q", tt.Texts())
	}

	st.model = "gpt-5"
	tt.Frame()
	if !tt.HasText("共 2 条") || !tt.HasText("成功 0 · 失败 1 · 取消 1") {
		t.Errorf("model filter shows %q", tt.Texts())
	}

	st.model, st.provider = "", "claude"
	tt.Frame()
	if !tt.HasText("共 3 条") || tt.HasText("carol@example.com") {
		t.Errorf("provider filter shows %q", tt.Texts())
	}

	st.provider, st.source = "", "carol@example.com"
	tt.Frame()
	if !tt.HasText("共 1 条") {
		t.Errorf("account filter shows %q", tt.Texts())
	}

	st.source, st.result = "", int(usage.ResultCanceled)
	tt.Frame()
	if !tt.HasText("共 1 条") || !tt.HasText("已取消") || !tt.HasText("成功 0 · 失败 0 · 取消 1") {
		t.Errorf("result filter shows %q", tt.Texts())
	}

	st.result = 0
	st.rng = 3 // 7 days
	tt.Frame()
	if !tt.HasText("共 5 条") || !tt.HasText("old-model") {
		t.Errorf("7 days show %q", tt.Texts())
	}

	// A filter on something the range no longer holds falls back to all.
	st.rng, st.model = usageDefaultRange, "old-model"
	tt.Frame()
	if st.model != "" || !tt.HasText("共 4 条") {
		t.Errorf("a stale model filter stays: model %q, texts %q", st.model, tt.Texts())
	}
}

func TestUsageRangeTabs(t *testing.T) {
	a, id := usageApp(t, true)
	addUsage(t, a, id,
		urec(10*time.Minute, "recent", nil),
		urec(6*time.Hour, "earlier", nil),
	)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	st := StateFor(a, "usage", id, newUsageState)
	if st.rng != usageDefaultRange || !tt.HasText("共 2 条") {
		t.Fatalf("default range %d shows %q", st.rng, tt.Texts())
	}
	if err := tt.Click("最近 4 小时"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.rng != 0 || !tt.HasText("共 1 条") || tt.HasText("earlier") {
		t.Errorf("4 hours: range %d, texts %q", st.rng, tt.Texts())
	}
}

func TestUsagePagination(t *testing.T) {
	a, id := usageApp(t, true)
	var recs []usage.Record
	for i := range 120 {
		recs = append(recs, urec(time.Duration(i+1)*time.Second, fmt.Sprintf("model-%03d", i), nil))
	}
	addUsage(t, a, id, recs...)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	scrollDown(tt)
	if !tt.HasText("第 1–50 条，共 120 条") || !tt.HasText("model-049") || tt.HasText("model-050") {
		t.Fatalf("first page shows %q", tt.Texts())
	}
	if err := tt.Click("下一页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("第 51–100 条，共 120 条") || !tt.HasText("model-050") || tt.HasText("model-049") {
		t.Fatalf("second page shows %q", tt.Texts())
	}
	if err := tt.Click("下一页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("第 101–120 条，共 120 条") || !tt.HasText("model-119") {
		t.Fatalf("last page shows %q", tt.Texts())
	}
	// Past the last page nothing happens.
	if err := tt.Click("下一页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("第 101–120 条，共 120 条") {
		t.Errorf("next past the end shows %q", tt.Texts())
	}
	if err := tt.Click("上一页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !tt.HasText("第 51–100 条，共 120 条") {
		t.Errorf("previous shows %q", tt.Texts())
	}

	// A new filter starts over at the first page.
	st := StateFor(a, "usage", id, newUsageState)
	st.model = "model-007"
	tt.Frame()
	if st.page != 0 || !tt.HasText("第 1–1 条，共 1 条") {
		t.Errorf("filtered shows page %d, %q", st.page, tt.Texts())
	}
}

func TestUsageUpdatesWithNewRecords(t *testing.T) {
	a, id := usageApp(t, true)
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("所选范围内没有用量记录。") {
		t.Fatalf("empty page shows %q", tt.Texts())
	}
	addUsage(t, a, id, urec(time.Second, "fresh-model", nil))
	tt.Frame()
	if !tt.HasText("fresh-model") || !tt.HasText("共 1 条") {
		t.Errorf("a new record is not shown: %q", tt.Texts())
	}
}

func TestUsageWithoutManager(t *testing.T) {
	a, _ := newTestApp(t, true)
	a.page = "usage"
	if a.Usage != nil {
		t.Fatal("the test app has a usage manager")
	}
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if !tt.HasText("用量不可用") || tt.HasText("请求明细") {
		t.Errorf("no empty state: %q", tt.Texts())
	}
}

func TestUsageClearAsksFirst(t *testing.T) {
	a, id := usageApp(t, true)
	addUsage(t, a, id, urec(time.Minute, "keep-me", nil))
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	if err := tt.Click("清除本地记录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("清除本地用量记录？") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	if n := countRecords(a, id); n != 1 {
		t.Fatalf("records cleared before confirming: %d", n)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := countRecords(a, id); n != 1 || !tt.HasText("keep-me") {
		t.Fatalf("canceling cleared records: %d", n)
	}
	if err := tt.Click("清除本地记录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认清除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := countRecords(a, id); n != 0 {
		t.Errorf("records left: %d", n)
	}
	if tt.HasText("keep-me") || !tt.HasText("所选范围内没有用量记录。") {
		t.Errorf("cleared page shows %q", tt.Texts())
	}
}

// scrollDown brings the end of the page into view.
func scrollDown(tt *ui.Tester) {
	tt.Frame()
	tt.Scroll(700, 400, 0, 20000)
	tt.Frame()
}

func countRecords(a *App, id string) int {
	n := 0
	a.Usage.Read(id, func(r []usage.Record) { n = len(r) })
	return n
}

// With the server's usage statistics off, the page offers to turn them on,
// and writes only once the dialog is confirmed.
func TestUsageEnableStatsNeedsConfirmation(t *testing.T) {
	q := newPageQueue(t)
	f := &pageFake{}
	f.extra = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if strings.HasSuffix(r.URL.Path, "/usage-statistics-enabled") {
			if r.Method == http.MethodPut {
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			} else {
				_, _ = w.Write([]byte(`false`))
			}
			return true
		}
		return false
	}
	a, id := newPageApp(t, f)
	a.Usage = usage.New(usage.Options{Dir: t.TempDir()})
	t.Cleanup(a.Usage.Close)
	a.page = "usage"
	cl, err := a.Svc.Client(id)
	if err != nil {
		t.Fatal(err)
	}
	a.Usage.Sync([]usage.Target{{ID: id, Client: cl}})
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	pump(t, tt, q, 5*time.Second, "the warning", func() bool {
		return tt.HasText("CPA 未开启 usage-statistics-enabled")
	})

	put := func() int {
		n := 0
		for _, r := range f.all() {
			if r.Method == http.MethodPut && strings.HasSuffix(r.Path, "usage-statistics-enabled") {
				n++
			}
		}
		return n
	}
	if err := tt.Click("开启用量统计"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the confirmation", func() bool {
		return tt.HasText("开启用量统计？")
	})
	if put() != 0 {
		t.Fatalf("a write before the confirmation (%d)", put())
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	settle(tt, q, 200*time.Millisecond)
	if put() != 0 {
		t.Fatalf("canceling wrote %d times", put())
	}

	if err := tt.Click("开启用量统计"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认开启"); err != nil {
		t.Fatal(err)
	}
	pump(t, tt, q, 5*time.Second, "the write", func() bool { return put() == 1 })
	pump(t, tt, q, 5*time.Second, "the warning to go", func() bool {
		return !tt.HasText("CPA 未开启 usage-statistics-enabled")
	})
	settle(tt, q, 200*time.Millisecond)
	if put() != 1 {
		t.Errorf("the write was sent %d times", put())
	}
}

// TestUsageSnapshot draws the page with data to $CPABAR_SNAPSHOTS.
func TestUsageSnapshot(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("CPABAR_SNAPSHOTS is not set")
	}
	a, id := usageApp(t, true)
	models := []string{"claude-sonnet-4-5-20250929", "claude-opus-4-1", "gpt-5-codex", "gemini-2.5-pro", "qwen3-coder", "glm-4.6", "kimi-k2", "deepseek-v3.2"}
	sources := []string{"alice@example.com", "bob.long.name@company-example.org", "carol@example.com"}
	var recs []usage.Record
	for i := range 400 {
		ago := time.Duration(i) * 13 * time.Minute
		m := models[(i*7+i/5)%len(models)]
		recs = append(recs, urec(ago, m, func(r *usage.Record) {
			r.Source = sources[i%len(sources)]
			r.Provider = []string{"claude", "codex", "gemini", "qwen", "openai-compatibility"}[i%5]
			r.Tokens = usage.Tokens{Input: int64(2000 + i*37%9000), Output: int64(300 + i*11%2500), Reasoning: int64(i * 3 % 400), CacheRead: int64(i * 53 % 1500), CacheCreate: int64(i * 7 % 300)}
			r.Tokens.Total = r.Tokens.Input + r.Tokens.Output
			r.LatencyMs = int64(600 + i*97%9000)
			r.TTFTMs = int64(150 + i*31%900)
			if i%2 == 0 {
				r.Alias = "my-" + m
			}
			switch {
			case i%17 == 3:
				r.Failed, r.Status, r.Error = true, 429, "rate limit exceeded for "+r.Source+": please retry after 20 seconds, quota resets at midnight"
			case i%29 == 5:
				r.Failed, r.Canceled, r.Status = true, true, 499
			}
			if i%4 == 1 {
				r.KeyID, r.KeyMask = "k2", "••••a1b2"
			}
		}))
	}
	addUsage(t, a, id, recs...)
	for _, dark := range []bool{false, true} {
		tt := ui.NewTester(a.managerView, managerWidth, 3400) // the whole page
		tt.SetDark(dark)
		tt.Frame()
		name := map[bool]string{false: "usage-light", true: "usage-dark"}[dark]
		out, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
		out.Close()
	}
}
