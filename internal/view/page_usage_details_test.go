package view

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/licoy/cpa-bar/internal/usage"
)

func TestUsageResultPillFitsText(t *testing.T) {
	newTestApp(t, false)
	st := &usageState{rows: []usage.Record{urec(time.Minute, "model", nil)}, total: 1}
	tt := ui.NewTester(func(c *ui.Context) { w, _ := c.Size(); st.table(c, nil, w) }, 880, 180)
	label, ok := tt.Find("成功")
	if !ok {
		t.Fatal("result label missing")
	}
	img := tt.Image()
	y := int(label.Y + label.H/2)
	inside := img.RGBAAt(int(label.X-5), y)
	after := img.RGBAAt(int(label.X+label.W+12), y)
	if inside == after {
		t.Fatalf("result capsule extends beyond its 10 px padding: text width %.1f, capsule still visible 12 px after text", label.W)
	}
}

func TestUsageDetailsScrollsInNarrowWindow(t *testing.T) {
	newTestApp(t, false)
	st := &usageState{rows: []usage.Record{urec(time.Minute, "model", nil)}, total: 1}
	tt := ui.NewTester(func(c *ui.Context) { w, _ := c.Size(); st.table(c, nil, w) }, 500, 240)
	model, _ := tt.Find("model")
	latency, _ := tt.Find("1.2 s")
	if model.W < 80 || latency.X < 500 {
		t.Fatalf("narrow table squeezed its columns: model %v, latency %v", model, latency)
	}
	tt.Scroll(250, model.Y+model.H/2, 1000, 0)
	latency, _ = tt.Find("1.2 s")
	if latency.X < 0 || latency.X+latency.W > 500 {
		t.Fatalf("last column cannot be reached by scrolling: %v", latency)
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "usage-details-narrow-scrolled.png"), tt)
	}
}

func TestUsageDetailsFillsWideWindow(t *testing.T) {
	newTestApp(t, false)
	st := &usageState{rows: []usage.Record{urec(time.Minute, "model", nil)}, total: 1}
	tt := ui.NewTester(func(c *ui.Context) { w, _ := c.Size(); st.table(c, nil, w) }, 980, 240)
	for _, width := range []int{980, 1200, 820} {
		tt.SetSize(width, 240)
		latency, ok := tt.Find("1.2 s")
		if !ok || latency.X+latency.W < float32(width-24) || latency.X+latency.W > float32(width-18) {
			t.Errorf("table does not fill %d px viewport: last cell %v", width, latency)
		}
	}
}

func TestUsageClientKeyTooltip(t *testing.T) {
	tt, a, id, _, q := cfgTester(t, "usage")
	a.Usage = usage.New(usage.Options{Dir: t.TempDir()})
	t.Cleanup(a.Usage.Close)
	r, ok := usage.Parse([]byte(fmt.Sprintf(`{"request_id":"with-key","model":"gpt-5","api_key":%q}`, cfgTestClientKey)), time.Now())
	if !ok {
		t.Fatal("usage record was not parsed")
	}
	// Matching must also work for historical records without persisting secrets.
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), cfgTestClientKey) {
		t.Fatal("full key reached the stored record")
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	addUsage(t, a, id, r,
		urec(time.Minute, "deleted-key", nil),
		urec(2*time.Minute, "no-key", func(r *usage.Record) { r.KeyID, r.KeyMask = "", "" }),
	)
	q.waitFor(t, tt, "the client keys", func() bool { return keysStateOf(a, id).loaded })
	if !tt.HasText("Key") || !tt.HasText("••••9f2c") || tt.HasText(cfgTestClientKey) {
		t.Fatalf("unexpected key display: %q", tt.Texts())
	}
	key, ok := tt.Find(maskMiddle(cfgTestClientKey))
	if !ok {
		t.Fatalf("masked client key missing: %q", tt.Texts())
	}
	tt.Move(key.X+key.W/2, key.Y+key.H/2)
	tt.Frame()
	if !tt.HasText(cfgTestClientKey) {
		t.Fatal("hover did not reveal the full client key")
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "usage-key-tooltip.png"), tt)
	}
	tt.Move(0, 0)
	tt.Frame()
	if tt.HasText(cfgTestClientKey) {
		t.Fatal("full client key remained visible after leaving")
	}
	// Exercise the normal scrollable manager window as well as the tall view.
	tt.SetSize(managerWidth+300, managerHeight)
	scrollDown(tt)
	key, ok = tt.Find(maskMiddle(cfgTestClientKey))
	if !ok || key.Y < 0 || key.Y+key.H > managerHeight {
		t.Fatalf("key is not visible after scrolling: %v", key)
	}
	tt.Move(key.X+key.W/2, key.Y+key.H/2)
	tt.Frame()
	if !tt.HasText(cfgTestClientKey) {
		t.Fatal("hover did not immediately reveal the key after scrolling")
	}
	if dir := os.Getenv("CPABAR_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, filepath.Join(dir, "usage-key-tooltip-scrolled.png"), tt)
	}
}

func TestUsageClientKeyLoadErrorIsVisible(t *testing.T) {
	tt, a, id, _, q := cfgTester(t, "usage", func(f *cfgFake) {
		f.set(clientKeysPath, map[string]string{"unexpected": "shape"})
	})
	a.Usage = usage.New(usage.Options{Dir: t.TempDir()})
	t.Cleanup(a.Usage.Close)
	addUsage(t, a, id, urec(time.Minute, "model", nil))
	q.waitFor(t, tt, "the key load error", func() bool { return keysStateOf(a, id).load.Err != nil })
	if !tt.HasText("无法解析客户端密钥列表") || !tt.HasText("请求明细") || !tt.HasText("••••9f2c") {
		t.Fatalf("key failure or request details missing: %q", tt.Texts())
	}
}

func TestUsageDetailsSnapshot(t *testing.T) {
	dir := os.Getenv("CPABAR_SNAPSHOTS")
	if dir == "" {
		t.Skip("CPABAR_SNAPSHOTS is not set")
	}
	newTestApp(t, false)
	r, ok := usage.Parse([]byte(fmt.Sprintf(`{"request_id":"snapshot","model":"claude-opus-5-5","provider":"claude","source":"alice@example.com","api_key":%q,"tokens":{"input_tokens":230528,"output_tokens":336,"cached_tokens":228200,"total_tokens":230864},"latency_ms":4200,"ttft_ms":966}`, cfgTestClientKey)), time.Now())
	if !ok {
		t.Fatal("usage record was not parsed")
	}
	st := &usageState{rows: []usage.Record{r, r, r}, total: 3}
	st.rows[1].Failed = true
	st.rows[2].Canceled = true
	for _, width := range []int{980, 780, 500} {
		for _, dark := range []bool{false, true} {
			tt := ui.NewTester(func(c *ui.Context) { w, _ := c.Size(); st.table(c, []string{cfgTestClientKey}, w) }, width, 320)
			tt.SetDark(dark)
			saveSnapshot(t, filepath.Join(dir, fmt.Sprintf("usage-details-%d-dark-%t.png", width, dark)), tt)
		}
	}
}
