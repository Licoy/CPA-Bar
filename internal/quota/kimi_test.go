package quota

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// kimiStub is a Caller that can also download credential files, as
// *cpa.Client can.
type kimiStub struct {
	*codexStub
	file      string
	fileErr   error
	downloads []string
}

func (s *kimiStub) DownloadCredential(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloads = append(s.downloads, name)
	return []byte(s.file), s.fileErr
}

func kimiCred(provider string) cpa.Credential {
	return cpa.Credential{Name: "kimi-1.json", AuthIndex: "kimi:1", Provider: provider, Type: provider}
}

func kimiStubFor(file string, replies map[string]codexReply) *kimiStub {
	return &kimiStub{codexStub: newCodexStub(replies), file: file}
}

func kimiNear(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestKimiWindows(t *testing.T) {
	stub := kimiStubFor(`{"type":"kimi"}`, map[string]codexReply{kimiUsageURL: codexOK(`{
	  "usage": {"limit": "100", "used": "97", "resetTime": "2099-09-30T16:48:39.136523Z"},
	  "limits": [
	    {"detail": {"limit": "100", "used": "2", "remaining": "98", "resetTime": "2099-07-31T06:59:23.136523Z"},
	     "window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}},
	    {"name": "Search", "limit": 50, "remaining": 20, "reset_in": 7200},
	    {"detail": {"limit": 0, "used": 0}},
	    {"detail": {"label": "no numbers"}},
	    {"detail": {"limit": 10, "used": 1}, "window": {"duration": 7, "timeUnit": "TIME_UNIT_DAY"}}
	  ],
	  "usages": {"limit_month_total": {"used_ratio": 0.2529, "reset_time": "2099-10-22T00:00:00Z"}}
	}`)})
	before := time.Now()
	r, err := fetchKimi(context.Background(), stub, kimiCred("kimi"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "" || len(r.Facts) != 0 || len(r.Groups) != 1 || r.Groups[0].Title != (Text{}) {
		t.Fatalf("result = %+v", r)
	}
	w := r.Groups[0].Windows
	type row struct {
		label     string
		remaining float64
		known     bool
	}
	want := []row{
		{"5h limit", 0.98, true},
		{"Search", 0.4, true},
		{"Limit #3", 0, false}, // no limit, nothing used: nothing to show
		{"7d limit", 0.9, true},
		{"Weekly limit", 0.03, true},
		{"Monthly limit", 0.75, true},
	}
	if len(w) != len(want) {
		t.Fatalf("windows = %+v", w)
	}
	for i, x := range want {
		if w[i].Label.EN != x.label || w[i].Known != x.known || !kimiNear(w[i].Remaining, x.remaining) {
			t.Errorf("window %d = %+v, want %+v", i, w[i], x)
		}
	}
	if w[0].Label.ZH != "5h 限额" || w[4].Label.ZH != "周限额" || w[5].Label.ZH != "月限额" || w[2].Label.ZH != "限额 #3" {
		t.Errorf("labels = %+v", w)
	}
	if !w[0].ResetAt.Equal(time.Date(2099, 7, 31, 6, 59, 23, 136523000, time.UTC)) {
		t.Errorf("five-hour reset = %v", w[0].ResetAt)
	}
	if d := w[1].ResetAt.Sub(before); d < 2*time.Hour-time.Second || d > 2*time.Hour+time.Minute {
		t.Errorf("relative reset in %v", d)
	}
	if !w[5].ResetAt.Equal(time.Date(2099, 10, 22, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("monthly reset = %v", w[5].ResetAt)
	}

	reqs := stub.requests(kimiUsageURL)
	if len(reqs) != 1 || reqs[0].AuthIndex != "kimi:1" || reqs[0].Method != "GET" || reqs[0].Header["Authorization"] != "Bearer $TOKEN$" {
		t.Errorf("requests = %+v", reqs)
	}
	if len(stub.downloads) != 1 || stub.downloads[0] != "kimi-1.json" {
		t.Errorf("downloads = %v", stub.downloads)
	}
}

func TestKimiMonthlyOnlyAndWeeklyOnly(t *testing.T) {
	r, err := fetchKimi(context.Background(), kimiStubFor(`{}`, map[string]codexReply{kimiUsageURL: codexOK(`{
	  "limits": [{"detail": {"limit": "100", "used": "5", "remaining": "95", "resetTime": "2099-09-27T00:30:34Z"}, "window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}}],
	  "usages": {"limit_month_total": {"used_ratio": 0.2529, "reset_time": "2099-10-22T00:00:00Z"}}}`)}), kimiCred("kimi"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Groups[0].Windows; len(got) != 2 || got[1].Label.EN != "Monthly limit" || !kimiNear(got[1].Remaining, 0.75) {
		t.Errorf("windows = %+v", got)
	}

	r, err = fetchKimi(context.Background(), kimiStubFor(`{}`, map[string]codexReply{kimiUsageURL: codexOK(`{"usage":{"limit":"100","used":"97","resetTime":"2099-09-30T16:48:39Z"},"usages":{}}`)}), kimiCred("kimi"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Groups[0].Windows; len(got) != 1 || got[0].Label.EN != "Weekly limit" {
		t.Errorf("windows = %+v", got)
	}
}

func TestKimiOverUseAndUnlimited(t *testing.T) {
	r, err := fetchKimi(context.Background(), kimiStubFor(`{}`, map[string]codexReply{kimiUsageURL: codexOK(`{"limits":[
	  {"detail":{"limit":10,"used":15}},
	  {"detail":{"limit":0,"used":4}}]}`)}), kimiCred("kimi"))
	if err != nil {
		t.Fatal(err)
	}
	w := r.Groups[0].Windows
	if len(w) != 2 || !w[0].Known || w[0].Remaining != 0 || !w[1].Known || w[1].Remaining != 0 {
		t.Errorf("windows = %+v", w)
	}
}

func TestKimiEmptyAndFailed(t *testing.T) {
	r, err := fetchKimi(context.Background(), kimiStubFor(`{}`, map[string]codexReply{kimiUsageURL: codexOK(`{"limits":[]}`)}), kimiCred("kimi"))
	if err != nil || len(r.Groups) != 0 || len(r.Notes) != 1 {
		t.Fatalf("r = %+v, err = %v", r, err)
	}
	_, err = fetchKimi(context.Background(), kimiStubFor(`{}`, map[string]codexReply{kimiUsageURL: {status: 401, body: `{"error":{"message":"bad token"}}`}}), kimiCred("kimi"))
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 401 || ue.Message != "bad token" {
		t.Fatalf("err = %v", err)
	}
	if _, err := fetchKimi(context.Background(), kimiStubFor(`{}`, nil), cpa.Credential{Name: "k.json"}); err == nil {
		t.Fatal("a credential without an auth index must fail")
	}
}

func TestKimiHost(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		file     string
		want     string
	}{
		{"kimi", "kimi", `{}`, kimiUsageURL},
		{"kimi-ai", "kimi-ai", `{}`, kimiAIUsageURL},
		{"kimi_ai", "kimi_ai", `{}`, kimiAIUsageURL},
		{"file domain wins over provider", "kimi-ai", `{"type":"kimi-ai","domain":"kimi.com"}`, kimiUsageURL},
		{"ai domain", "kimi", `{"type":"kimi","domain":"ai"}`, kimiAIUsageURL},
		{"padded domain beats base url", "kimi", `{"domain":" COM ","base_url":"https://api.kimi.ai/coding"}`, kimiUsageURL},
		{"subdomain", "kimi", `{"domain":"auth.kimi.ai","base_url":"https://api.kimi.com/coding"}`, kimiAIUsageURL},
		{"base url over provider", "kimi-ai", `{"base_url":"https://api.kimi.com/coding"}`, kimiUsageURL},
		{"base url ai", "kimi", `{"base_url":"https://api.kimi.ai/coding"}`, kimiAIUsageURL},
		{"hyphenated base url", "kimi", `{"base-url":"https://api.kimi.ai/coding"}`, kimiAIUsageURL},
		{"canonical key wins even when it is empty", "kimi", `{"base_url":"","base-url":"https://api.kimi.ai"}`, kimiUsageURL},
		{"canonical key wins when null", "kimi", `{"base_url":null,"base-url":"https://api.kimi.ai"}`, kimiUsageURL},
		{"lookalike host", "kimi", `{"base_url":"https://kimi.ai.evil.invalid/coding"}`, kimiUsageURL},
		{"userinfo trick", "kimi", `{"base_url":"https://kimi.ai@evil.invalid/coding"}`, kimiUsageURL},
		{"not a url", "kimi", `{"base_url":"not a URL"}`, kimiUsageURL},
		{"blank domain falls through", "kimi", `{"domain":" ","base_url":"https://api.kimi.ai/coding"}`, kimiAIUsageURL},
		{"unknown domain is kimi.com", "kimi-ai", `{"domain":"unknown","base_url":"https://api.kimi.ai"}`, kimiUsageURL},
		{"type in the file", "kimi", `{"type":"kimi-ai"}`, kimiAIUsageURL},
	}
	for _, tt := range tests {
		stub := kimiStubFor(tt.file, map[string]codexReply{kimiUsageURL: codexOK(`{"limits":[]}`), kimiAIUsageURL: codexOK(`{"limits":[]}`)})
		if _, err := fetchKimi(context.Background(), stub, kimiCred(tt.provider)); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		var hit string
		for _, r := range stub.sent {
			hit = r.URL
		}
		if len(stub.sent) != 1 || hit != tt.want {
			t.Errorf("%s: requested %v, want %s", tt.name, stub.sent, tt.want)
		}
	}
}

func TestKimiDoesNotGuessTheHost(t *testing.T) {
	for name, stub := range map[string]*kimiStub{
		"download fails":  {codexStub: newCodexStub(nil), fileErr: errors.New("secret-fixture")},
		"not json":        {codexStub: newCodexStub(nil), file: `{"access_token":"secret-fixture"`},
		"json but no obj": {codexStub: newCodexStub(nil), file: `"secret-fixture"`},
		"json null":       {codexStub: newCodexStub(nil), file: `null`},
		"json array":      {codexStub: newCodexStub(nil), file: `[]`},
		"runtime-only":    {codexStub: newCodexStub(nil), file: `{}`},
		"nameless":        {codexStub: newCodexStub(nil), file: `{}`},
	} {
		cr := kimiCred("kimi-ai")
		switch name {
		case "runtime-only":
			cr.RuntimeOnly = true
		case "nameless":
			cr.Name = " "
		}
		_, err := fetchKimi(context.Background(), stub, cr)
		if err == nil {
			t.Errorf("%s: want an error", name)
			continue
		}
		if strings.Contains(err.Error(), "secret-fixture") {
			t.Errorf("%s: error echoes the credential: %v", name, err)
		}
		if len(stub.sent) != 0 {
			t.Errorf("%s: sent %v after failing to find the host", name, stub.sent)
		}
	}
}

// A Caller that cannot read credential files falls back to the listing.
func TestKimiHostWithoutDownloads(t *testing.T) {
	for provider, want := range map[string]string{"kimi": kimiUsageURL, "kimi-ai": kimiAIUsageURL} {
		stub := newCodexStub(map[string]codexReply{kimiUsageURL: codexOK(`{"limits":[]}`), kimiAIUsageURL: codexOK(`{"limits":[]}`)})
		if _, err := fetchKimi(context.Background(), stub, kimiCred(provider)); err != nil {
			t.Fatal(err)
		}
		if len(stub.sent) != 1 || stub.sent[0].URL != want {
			t.Errorf("%s: requested %v, want %s", provider, stub.sent, want)
		}
	}
}

func TestKimiRegisteredForKimiAI(t *testing.T) {
	p, ok := builtin["kimi"]
	if !ok || p.Fetch == nil || p.Act != nil {
		t.Fatalf("kimi provider = %+v", p)
	}
	if got := ProviderKey(kimiCred("kimi-ai")); got != "kimi" {
		t.Errorf("ProviderKey(kimi-ai) = %q", got)
	}
}
