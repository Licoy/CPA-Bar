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

type devinFake struct {
	mu       sync.Mutex
	status   int
	body     string
	requests []cpa.UpstreamRequest
}

func (f *devinFake) Upstream(_ context.Context, r cpa.UpstreamRequest) (*cpa.UpstreamResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	return &cpa.UpstreamResponse{StatusCode: f.status, Body: f.body}, nil
}
func (f *devinFake) Plugins(context.Context) (*cpa.PluginList, error) { return nil, nil }
func (f *devinFake) PluginQuota(context.Context, string, string) (json.RawMessage, error) {
	return nil, errors.New("unused")
}

func devinCredential() cpa.Credential {
	return cpa.Credential{Name: "devin.json", AuthIndex: " 42", Provider: "devin"}
}

func TestDevinFetch(t *testing.T) {
	f := &devinFake{status: 200, body: `{"userStatus":{"planStatus":{
		"dailyQuotaRemainingPercent":40,"dailyQuotaResetAtUnix":"1777680000",
		"weeklyQuotaRemainingPercent":"75","weeklyQuotaResetAtUnix":1778198400,
		"planInfo":{"planName":" pro "},"planStart":"2026-05-01T00:00:00Z","planEnd":"2026-06-01T00:00:00Z"}}}`}
	r, err := fetchDevin(context.Background(), f, devinCredential())
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "pro" {
		t.Errorf("plan = %q", r.Plan)
	}
	wantEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if len(r.Facts) != 1 || r.Facts[0].Label.EN != "Subscription ends" || r.Facts[0].Value != wantEnd {
		t.Errorf("facts = %+v", r.Facts)
	}
	if len(r.Groups) != 1 || len(r.Groups[0].Windows) != 2 || !r.Groups[0].Title.IsZero() {
		t.Fatalf("groups = %+v", r.Groups)
	}
	d, w := r.Groups[0].Windows[0], r.Groups[0].Windows[1]
	if d.Label.ZH != "每日剩余额度" || !d.Known || d.Remaining != 0.4 || !d.ResetAt.Equal(time.Unix(1777680000, 0)) {
		t.Errorf("daily = %+v", d)
	}
	if w.Label.EN != "Weekly remaining" || !w.Known || w.Remaining != 0.75 || !w.ResetAt.Equal(time.Unix(1778198400, 0)) {
		t.Errorf("weekly = %+v", w)
	}

	if len(f.requests) != 1 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	req := f.requests[0]
	if req.URL != devinStatusURL || req.Method != "POST" || req.Header["Connect-Protocol-Version"] != "1" ||
		req.Header["Content-Type"] != "application/json" {
		t.Errorf("request = %+v", req)
	}
	var body struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(req.Data), &body); err != nil || body.Metadata["apiKey"] != "$TOKEN$" ||
		body.Metadata["ideName"] != "chisel" || body.Metadata["ideVersion"] != "3000.10.21" {
		t.Errorf("body = %s (%v)", req.Data, err)
	}
}

func TestDevinPartialAndInvalidValues(t *testing.T) {
	cases := map[string]struct {
		body      string
		known     [2]bool
		wantReset [2]bool
		wantErr   bool
	}{
		"percent only":    {body: `{"userStatus":{"planStatus":{"dailyQuotaRemainingPercent":0}}}`, known: [2]bool{true, false}},
		"reset only":      {body: `{"userStatus":{"planStatus":{"weeklyQuotaResetAtUnix":"1778198400"}}}`, wantReset: [2]bool{false, true}},
		"out of range":    {body: `{"userStatus":{"planStatus":{"dailyQuotaRemainingPercent":101,"weeklyQuotaRemainingPercent":-1}}}`, wantErr: true},
		"bad text":        {body: `{"userStatus":{"planStatus":{"dailyQuotaRemainingPercent":"5%","weeklyQuotaResetAtUnix":"12.5"}}}`, wantErr: true},
		"no status":       {body: `{}`, wantErr: true},
		"plan only":       {body: `{"userStatus":{"planStatus":{"planInfo":{"planName":"x"}}}}`, wantErr: true},
		"reset non-whole": {body: `{"userStatus":{"planStatus":{"dailyQuotaResetAtUnix":1778198400.5}}}`, wantErr: true},
	}
	for name, tc := range cases {
		r, err := fetchDevin(context.Background(), &devinFake{status: 200, body: tc.body}, devinCredential())
		if tc.wantErr {
			var m *Msg
			if !errors.As(err, &m) || !strings.Contains(m.EN, "no usable quota data") {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		ws := r.Groups[0].Windows
		for i := range ws {
			if ws[i].Known != tc.known[i] || ws[i].ResetAt.IsZero() == tc.wantReset[i] {
				t.Errorf("%s: window %d = %+v", name, i, ws[i])
			}
		}
	}
}

func TestDevinUpstreamError(t *testing.T) {
	_, err := fetchDevin(context.Background(), &devinFake{status: 401, body: `{"message":"bad key"}`}, devinCredential())
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 401 || ue.Message != "bad key" {
		t.Fatalf("err = %v", err)
	}
	_, err = fetchDevin(context.Background(), &devinFake{}, cpa.Credential{})
	var m *Msg
	if !errors.As(err, &m) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := builtin["devin"]; !ok {
		t.Fatal("devin is not registered")
	}
}
