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

type metaFake struct {
	mu        sync.Mutex
	file      string
	fileErr   error
	status    int
	body      string
	requests  []cpa.UpstreamRequest
	downloads []string
}

func (f *metaFake) Upstream(_ context.Context, r cpa.UpstreamRequest) (*cpa.UpstreamResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	return &cpa.UpstreamResponse{StatusCode: f.status, Body: f.body}, nil
}
func (f *metaFake) Plugins(context.Context) (*cpa.PluginList, error) { return nil, nil }
func (f *metaFake) PluginQuota(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}
func (f *metaFake) DownloadCredential(_ context.Context, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, name)
	return []byte(f.file), f.fileErr
}

const metaFixture = `{
 "api_key":"LLM|fixture-only|not-a-real-key","user_email":"fixture@example.invalid",
 "is_subs_active":true,"subs_tier_id":"fixture-tier","subs_tier_name":"Muse Code Everyday Usage",
 "subs_usage":{"window":{"used_percent":2,"window_duration_mins":300,"resets_at":1789678120},
               "weekly":{"used_percent":0,"resets_at":1789948800},"tier":"fixture-tier"}}`

func metaCredential() cpa.Credential {
	return cpa.Credential{Name: "meta.json", AuthIndex: "3", Provider: "meta"}
}

func TestMetaFetch(t *testing.T) {
	f := &metaFake{file: `{"dca_token":" dca:abc123 ","api_key":"LLM|x"}`, status: 200, body: metaFixture}
	r, err := fetchMeta(context.Background(), f, metaCredential())
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "Muse Code Everyday Usage" {
		t.Errorf("plan = %q", r.Plan)
	}
	if len(r.Facts) != 1 || r.Facts[0].Label.EN != "Subscription active" {
		t.Errorf("facts = %+v", r.Facts)
	}
	if len(r.Groups) != 1 || len(r.Groups[0].Windows) != 2 || len(r.Notes) != 0 {
		t.Fatalf("result = %+v", r)
	}
	win, week := r.Groups[0].Windows[0], r.Groups[0].Windows[1]
	if win.Label != (Text{"300 分钟周期", "300-minute window"}) || !win.Known || win.Remaining != 0.98 ||
		!win.ResetAt.Equal(time.Unix(1789678120, 0)) {
		t.Errorf("window = %+v", win)
	}
	if week.Label != (Text{"每周额度", "Weekly quota"}) || !week.Known || week.Remaining != 1 ||
		!week.ResetAt.Equal(time.Unix(1789948800, 0)) {
		t.Errorf("weekly = %+v", week)
	}

	if len(f.downloads) != 1 || f.downloads[0] != "meta.json" || len(f.requests) != 1 {
		t.Fatalf("downloads %v requests %d", f.downloads, len(f.requests))
	}
	req := f.requests[0]
	if req.URL != metaQuotaURL || req.Method != "POST" || req.Data != "{}" || req.AuthIndex != "3" ||
		req.Header["Authorization"] != "Bearer dca:abc123" || req.Header["x-api-version"] != "1.0.0" {
		t.Errorf("request = %+v", req)
	}
}

func TestMetaUnknownQuota(t *testing.T) {
	for _, body := range []string{`{}`, `{"api_key":"x"}`, `{"subs_usage":null}`, `{"subs_usage":{"window":null,"weekly":{}}}`,
		`{"subs_usage":{"window":{"used_percent":"bad"},"weekly":{"used_percent":""}}}`} {
		f := &metaFake{file: `{"dca_token":"dca:t"}`, status: 200, body: body}
		r, err := fetchMeta(context.Background(), f, metaCredential())
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		ws := r.Groups[0].Windows
		if ws[0].Known || ws[1].Known || len(r.Notes) != 1 {
			t.Errorf("%s: result = %+v", body, r)
		}
	}

	f := &metaFake{file: `{"dca_token":"dca:t"}`, status: 200,
		body: `{"is_subs_active":false,"subs_usage":{"tier":" tier-x ","window":{"used_percent":-4,"resets_at":0,"window_duration_mins":"bad"},"weekly":{"used_percent":120,"resets_at":"1789948800"}}}`}
	r, err := fetchMeta(context.Background(), f, metaCredential())
	if err != nil {
		t.Fatal(err)
	}
	ws := r.Groups[0].Windows
	if r.Plan != "tier-x" || r.Facts[0].Label.EN != "Subscription inactive" ||
		ws[0].Remaining != 1 || ws[0].Label.EN != "Usage window" || !ws[0].ResetAt.IsZero() ||
		ws[1].Remaining != 0 || !ws[1].Known || !ws[1].ResetAt.Equal(time.Unix(1789948800, 0)) {
		t.Errorf("result = %+v", r)
	}
}

func TestMetaErrors(t *testing.T) {
	ctx := context.Background()
	expect := func(name string, f *metaFake, cr cpa.Credential, want string) {
		t.Helper()
		_, err := fetchMeta(ctx, f, cr)
		var m *Msg
		if !errors.As(err, &m) || !strings.Contains(m.EN, want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(m.ZH, "secret")) {
			t.Errorf("%s: error leaks the response: %v", name, err)
		}
	}
	ok := `{"dca_token":"dca:t"}`
	runtimeOnly := metaCredential()
	runtimeOnly.RuntimeOnly = true

	expect("auth index", &metaFake{}, cpa.Credential{Name: "m.json"}, "auth_index")
	expect("runtime only", &metaFake{}, runtimeOnly, "downloadable")
	expect("no name", &metaFake{}, cpa.Credential{AuthIndex: "1"}, "downloadable")
	expect("download", &metaFake{fileErr: errors.New("nope")}, metaCredential(), "Could not download")
	expect("bad json", &metaFake{file: "{nope"}, metaCredential(), "not valid JSON")
	expect("array", &metaFake{file: "[]"}, metaCredential(), "not valid JSON")
	expect("no token", &metaFake{file: `{"api_key":"LLM|x"}`}, metaCredential(), "dca_token")
	expect("bad token", &metaFake{file: `{"dca_token":"abc"}`}, metaCredential(), "dca_token")
	expect("status", &metaFake{file: ok, status: 401, body: `{"api_key":"secret"}`}, metaCredential(), "request failed")
	expect("not object", &metaFake{file: ok, status: 200, body: `[]`}, metaCredential(), "invalid quota response")
	expect("empty", &metaFake{file: ok, status: 200, body: ``}, metaCredential(), "invalid quota response")
	expect("text", &metaFake{file: ok, status: 200, body: `secret`}, metaCredential(), "invalid quota response")

	// Without a way to read the credential file there is no token.
	_, err := fetchMeta(ctx, metaNoDownload{}, metaCredential())
	if err == nil {
		t.Error("expected an error without a downloader")
	}
	if _, ok := builtin["meta"]; !ok {
		t.Fatal("meta is not registered")
	}
}

// metaNoDownload is a Caller that cannot read credential files.
type metaNoDownload struct{}

func (metaNoDownload) Upstream(context.Context, cpa.UpstreamRequest) (*cpa.UpstreamResponse, error) {
	return nil, errors.New("unused")
}
func (metaNoDownload) Plugins(context.Context) (*cpa.PluginList, error) { return nil, nil }
func (metaNoDownload) PluginQuota(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}
