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
	"github.com/licoy/cpa-bar/internal/store"
)

// quotaCPA is a fake CPA whose api-call route answers as Anthropic does
// for a Claude Pro account with one banked reset, recording the upstream
// requests.
type quotaCPA struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string // method + " " + upstream URL
}

func (s *quotaCPA) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newQuotaCPA(t *testing.T) *quotaCPA {
	t.Helper()
	s := &quotaCPA{}
	reset5h := time.Now().Add(4*time.Hour + 10*time.Minute).UTC().Format(time.RFC3339)
	reset7d := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "8.0.23")
		switch r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v8/management") {
		case "GET /config":
			_, _ = w.Write([]byte(`{}`))
		case "GET /credentials":
			_, _ = w.Write([]byte(`{"files":[
				{"id":"a.json","auth_index":"1","name":"a.json","provider":"claude","email":"pro@example.com","status":"active"},
				{"id":"c.json","auth_index":"3","name":"c.json","provider":"claude","email":"off@example.com","status":"disabled","disabled":true}
			]}`))
		case "POST /requests/api-call":
			var req struct {
				AuthIndex string `json:"auth_index"`
				Method    string `json:"method"`
				URL       string `json:"url"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &req)
			s.mu.Lock()
			s.calls = append(s.calls, req.Method+" "+req.URL)
			s.mu.Unlock()
			var body string
			switch {
			case req.AuthIndex != "1":
				body = `{"error":{"message":"unexpected account"}}`
			case strings.Contains(req.URL, "cedar_ember=1"):
				body = `{"cedar_ember":{"eligible":true,"at_limit":true,"grants":[{"id":"g1","resets_total":3,"resets_left":2,"usable_now":true,"use_requires_limit":true,"clears":["five_hour"]}]}}`
			case strings.HasSuffix(req.URL, "/api/oauth/usage"):
				body = `{"five_hour":{"utilization":5,"resets_at":"` + reset5h + `"},"seven_day":{"utilization":33,"resets_at":"` + reset7d + `"}}`
			case strings.HasSuffix(req.URL, "/api/oauth/profile"):
				body = `{"account":{"has_claude_pro":true,"has_claude_max":false},"organization":{"uuid":"0b5c2f4e-1d2a-4c3b-9e8f-7a6b5c4d3e2f"}}`
			case strings.HasSuffix(req.URL, "/reset_rate_limits"):
				body = `{"result":"reset"}`
			}
			out, _ := json.Marshal(map[string]any{"status_code": 200, "header": map[string][]string{}, "body": body})
			_, _ = w.Write(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func quotaApp(t *testing.T) (*App, string, *quotaCPA, *uiQueue) {
	t.Helper()
	q := installTestRun(t)
	srv := newQuotaCPA(t)
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
	return a, id, srv, q
}

// The panel reads the quota of the accounts it shows, and shows the plan,
// the resets left and each limit with what is left and when it refills.
// Disabled accounts are asked about too.
func TestPanelShowsQuota(t *testing.T) {
	a, id, srv, q := quotaApp(t)
	a.panelInstance = id
	tt := ui.NewTester(a.panelView, panelWidth, panelHeight)
	waitFor(t, tt, q, "the quota", func() bool { return tt.HasText("5 小时限额") })
	for _, s := range []string{"Pro", "剩余重置次数 2", "剩余 95%", "7 天限额", "剩余 67%", "刷新额度", "重置额度"} {
		if !tt.HasText(s) {
			t.Errorf("panel lacks %q; texts: %q", s, tt.Texts())
		}
	}
	found := false
	for _, s := range tt.Texts() {
		if strings.Contains(s, "4 小时 9 分钟后") || strings.Contains(s, "4 小时 10 分钟后") {
			found = true
		}
	}
	if !found {
		t.Errorf("no reset time; texts: %q", tt.Texts())
	}
	// usage + grants, of the enabled account and the disabled one.
	if n := srv.count("GET https://api.anthropic.com/api/oauth/usage"); n != 4 {
		t.Errorf("%d usage reads, want 4", n)
	}

	// A refresh within the TTL reads nothing more; the button reads again.
	tt.Frame()
	q.drain()
	before := srv.count("GET")
	if err := tt.Click("刷新额度"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the second read", func() bool { return srv.count("GET") >= before+3 })
}

// A reset spends a limited grant: it runs only after the confirmation,
// is sent once, and the quota is read again afterwards.
func TestQuotaResetNeedsConfirmation(t *testing.T) {
	a, id, srv, q := quotaApp(t)
	a.page = "accounts"
	tt := ui.NewTester(a.managerView, managerWidth, managerHeight)
	waitFor(t, tt, q, "the quota", func() bool { return tt.HasText("重置额度") })
	if err := tt.Click("重置额度"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if srv.count("POST") != 0 {
		t.Fatal("the reset ran before the confirmation")
	}
	if !tt.HasText("确认重置额度") {
		t.Fatalf("no confirmation; texts: %q", tt.Texts())
	}
	reads := srv.count("GET https://api.anthropic.com/api/oauth/usage")
	if err := tt.Click("确认重置额度"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, q, "the reset", func() bool { return srv.count("POST") == 1 && tt.HasText("已重置用量限制。") })
	waitFor(t, tt, q, "the read after the reset", func() bool {
		return srv.count("GET https://api.anthropic.com/api/oauth/usage") > reads
	})
	for range 5 {
		q.drain()
		tt.Frame()
	}
	if n := srv.count("POST"); n != 1 {
		t.Errorf("%d reset claims sent, want 1", n)
	}
	_ = id
}
