package quota

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// antigravityFake answers upstream calls by URL; a pointer, since Callers
// are map keys elsewhere in the package.
type antigravityFake struct {
	mu       sync.Mutex
	replies  map[string]*cpa.UpstreamResponse
	files    map[string]string
	requests []cpa.UpstreamRequest
}

func (f *antigravityFake) Upstream(_ context.Context, r cpa.UpstreamRequest) (*cpa.UpstreamResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if resp, ok := f.replies[r.URL]; ok {
		return resp, nil
	}
	return &cpa.UpstreamResponse{StatusCode: 500, Body: "no canned answer"}, nil
}

func (f *antigravityFake) Plugins(context.Context) (*cpa.PluginList, error) { return nil, nil }
func (f *antigravityFake) PluginQuota(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func (f *antigravityFake) DownloadCredential(_ context.Context, name string) ([]byte, error) {
	if s, ok := f.files[name]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("not found")
}

func (f *antigravityFake) urls(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if strings.Contains(r.URL, prefix) {
			out = append(out, r.URL)
		}
	}
	return out
}

func agReply(status int, body string) *cpa.UpstreamResponse {
	return &cpa.UpstreamResponse{StatusCode: status, Body: body}
}

const agSummary = `{"groups":[
 {"displayName":"Claude and GPT models","description":"Models within this group: Claude Sonnet, GPT-OSS","buckets":[
   {"window":"weekly","displayName":"Weekly limit","remainingFraction":0.5,"resetTime":"2026-10-17T00:00:00Z"},
   {"window":"5h","displayName":"5-hour limit","remainingFraction":"80%","resetTime":"2026-10-11T05:00:00Z"},
   {"displayName":"Broken bucket"}]},
 {"displayName":"Gemini models","buckets":[
   {"window":"5h","description":"Custom","remaining_fraction":1,"reset_time":"2026-10-11T06:00:00Z"}]},
 {"displayName":"Empty","buckets":[]}]}`

const agTier = `{"currentTier":{"id":"free-tier","name":"Free"},"paidTier":{"id":"g1-ultra-lite-tier","name":"Google AI Ultra Lite"}}`

func agCredential() cpa.Credential {
	return cpa.Credential{Name: "ag.json", AuthIndex: "7", Provider: "antigravity", ProjectID: "proj-1"}
}

func TestAntigravityFetch(t *testing.T) {
	f := &antigravityFake{replies: map[string]*cpa.UpstreamResponse{
		antigravityQuotaURLs[0]:  agReply(200, agSummary),
		antigravityCodeAssistURL: agReply(200, agTier),
	}}
	r, err := fetchAntigravity(context.Background(), f, agCredential())
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "Ultra Lite" {
		t.Errorf("plan = %q", r.Plan)
	}
	if len(r.Groups) != 2 {
		t.Fatalf("groups = %+v", r.Groups)
	}
	g := r.Groups[0]
	if g.Title != (Text{"Claude 和 GPT 模型", "Claude and GPT models"}) {
		t.Errorf("title = %+v", g.Title)
	}
	if g.Note != (Text{"此分组包含：Claude Sonnet, GPT-OSS", "Models in this group: Claude Sonnet, GPT-OSS"}) {
		t.Errorf("note = %+v", g.Note)
	}
	if len(g.Windows) != 2 {
		t.Fatalf("windows = %+v", g.Windows)
	}
	five, week := g.Windows[0], g.Windows[1]
	if five.Label.EN != "5-hour limit" || five.Label.ZH != "5 小时限额" || !five.Known || five.Remaining != 0.8 {
		t.Errorf("five-hour = %+v", five)
	}
	if want := time.Date(2026, 10, 11, 5, 0, 0, 0, time.UTC); !five.ResetAt.Equal(want) {
		t.Errorf("five-hour reset = %v", five.ResetAt)
	}
	if week.Label.EN != "Weekly limit" || week.Label.ZH != "周限额" || week.Remaining != 0.5 {
		t.Errorf("weekly = %+v", week)
	}
	if g2 := r.Groups[1]; g2.Title.EN != "Gemini models" || g2.Title.ZH != "Gemini 模型" || !g2.Note.IsZero() ||
		len(g2.Windows) != 1 || g2.Windows[0].Label.EN != "Custom" || g2.Windows[0].Remaining != 1 {
		t.Errorf("gemini group = %+v", g2)
	}

	// The requests carry the placeholder token and the project.
	for _, req := range f.requests {
		if req.AuthIndex != "7" || req.Method != "POST" || req.Header["Authorization"] != "Bearer $TOKEN$" ||
			!strings.HasPrefix(req.Header["User-Agent"], "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)") {
			t.Errorf("request = %+v", req)
		}
		if req.URL == antigravityQuotaURLs[0] && req.Data != `{"project":"proj-1"}` {
			t.Errorf("quota body = %s", req.Data)
		}
		if req.URL == antigravityCodeAssistURL && req.Data != `{"metadata":{"ideType":"ANTIGRAVITY"}}` {
			t.Errorf("tier body = %s", req.Data)
		}
	}
}

func TestAntigravityFallsBackAcrossURLs(t *testing.T) {
	f := &antigravityFake{replies: map[string]*cpa.UpstreamResponse{
		antigravityQuotaURLs[0]:  agReply(503, `{"error":{"message":"unavailable"}}`),
		antigravityQuotaURLs[1]:  agReply(200, `{"groups":[]}`),
		antigravityQuotaURLs[2]:  agReply(200, agSummary),
		antigravityCodeAssistURL: agReply(500, "boom"),
	}}
	r, err := fetchAntigravity(context.Background(), f, agCredential())
	if err != nil {
		t.Fatal(err)
	}
	if got := f.urls("retrieveUserQuotaSummary"); len(got) != 3 || got[0] != antigravityQuotaURLs[0] || got[2] != antigravityQuotaURLs[2] {
		t.Errorf("urls tried = %v", got)
	}
	if len(r.Groups) != 2 || r.Plan != "" {
		t.Errorf("result = %+v", r)
	}
}

func TestAntigravityErrors(t *testing.T) {
	// 403/404 outrank the later statuses, with the last message.
	f := &antigravityFake{replies: map[string]*cpa.UpstreamResponse{
		antigravityQuotaURLs[0]: agReply(403, `{"error":{"message":"denied"}}`),
		antigravityQuotaURLs[1]: agReply(500, `{"message":"oops"}`),
		antigravityQuotaURLs[2]: agReply(502, "bad gateway"),
	}}
	_, err := fetchAntigravity(context.Background(), f, agCredential())
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 403 || ue.Message != "bad gateway" {
		t.Fatalf("err = %v", err)
	}

	// A project id is required.
	cr := agCredential()
	cr.ProjectID = ""
	_, err = fetchAntigravity(context.Background(), f, cr)
	var m *Msg
	if !errors.As(err, &m) || !strings.Contains(m.EN, "project_id") {
		t.Fatalf("err = %v", err)
	}

	// Answers without groups are an empty quota, not an error.
	f = &antigravityFake{replies: map[string]*cpa.UpstreamResponse{
		antigravityQuotaURLs[0]:  agReply(200, `{"other":1}`),
		antigravityQuotaURLs[1]:  agReply(200, `not json`),
		antigravityQuotaURLs[2]:  agReply(200, `{"groups":[{"displayName":"x","buckets":[{"remainingFraction":"n/a"}]}]}`),
		antigravityCodeAssistURL: agReply(200, `{"currentTier":{"id":"custom","name":"Team Plan"}}`),
	}}
	r, err := fetchAntigravity(context.Background(), f, agCredential())
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "Team Plan" || len(r.Groups) != 0 || len(r.Notes) != 1 {
		t.Errorf("result = %+v", r)
	}
}

func TestAntigravityProjectID(t *testing.T) {
	cases := map[string]struct {
		raw   string
		files map[string]string
		want  string
	}{
		"top":        {raw: `{"projectId":"a"}`, want: "a"},
		"metadata":   {raw: `{"metadata":{"project_id":"b"}}`, want: "b"},
		"attributes": {raw: `{"attributes":{"gemini_virtual_project":"c"}}`, want: "c"},
		"file":       {raw: `{}`, files: map[string]string{"ag.json": `{"installed":{"project_id":"d"}}`}, want: "d"},
		"none":       {raw: `{}`, want: ""},
	}
	for name, tc := range cases {
		cr := cpa.Credential{Name: "ag.json", Raw: json.RawMessage(tc.raw)}
		got := antigravityProjectID(context.Background(), &antigravityFake{files: tc.files}, cr)
		if got != tc.want {
			t.Errorf("%s: project = %q, want %q", name, got, tc.want)
		}
	}
}

func TestAntigravityPayloadUnwrapsBody(t *testing.T) {
	p := antigravityPayload(`{"body":"{\"groups\":[{\"displayName\":\"G\",\"buckets\":[{\"remainingFraction\":0.25}]}]}"}`)
	gs := antigravityGroups(p)
	if len(gs) != 1 || gs[0].Windows[0].Remaining != 0.25 {
		t.Fatalf("groups = %+v", gs)
	}
}

func TestAntigravityRegistered(t *testing.T) {
	if _, ok := builtin["antigravity"]; !ok {
		t.Fatal("antigravity is not registered")
	}
}
