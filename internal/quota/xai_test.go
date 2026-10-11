package quota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

func xaiCred(raw string) cpa.Credential {
	cr := cpa.Credential{Name: "grok.json", AuthIndex: "xai:1", Type: "xai"}
	if raw != "" {
		cr.Raw = json.RawMessage(raw)
	}
	return cr
}

const (
	xaiWeeklyStart = "2026-09-17T13:32:42.093205+00:00"
	xaiWeeklyEnd   = "2026-09-24T13:32:42.093205+00:00"
	xaiMonthEnd    = "2026-10-01T00:00:00+00:00"
)

func xaiWeeklyBody(extra string) string {
	return `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"` + xaiWeeklyStart + `","end":"` + xaiWeeklyEnd + `"},
	  "billingPeriodStart":"` + xaiWeeklyStart + `","billingPeriodEnd":"` + xaiWeeklyEnd + `","onDemandCap":{"val":0}` + extra + `}}`
}

func xaiMonthlyBody(extra string) string {
	return `{"config":{"monthlyLimit":{"val":0},"used":{"val":0},"onDemandCap":{"val":0},
	  "billingPeriodStart":"2026-09-01T00:00:00+00:00","billingPeriodEnd":"` + xaiMonthEnd + `"` + extra + `}}`
}

func xaiLocalShort(s string) string {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t.Local().Format("01/02 15:04")
}

func xaiFacts(r *Result) map[string]string {
	out := map[string]string{}
	for _, f := range r.Facts {
		out[f.Label.EN] = f.Value
	}
	return out
}

func TestXAIFetchWeeklyAndMonthly(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{
		xaiBillingWeeklyURL:  codexOK(xaiWeeklyBody(`,"creditUsagePercent":25,"productUsage":[{"product":"Chat","usagePercent":30},{"usage_percent":"5"},{"product":"Voice"}]`)),
		xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(`,"monthlyLimit":{"val":15000},"used":{"val":2500},"prepaidBalance":{"val":123456}`)),
		xaiUserURL:           codexOK(`{"subscriptionTier":"SuperGrokPro"}`),
		xaiSettingsURL:       codexOK(`{"subscription_tier_display":"SuperGrok Heavy"}`),
	})
	r, err := fetchXAI(context.Background(), stub, xaiCred(`{"metadata":{"sub":"user-7"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "SuperGrok Heavy" {
		t.Errorf("plan = %q", r.Plan)
	}
	facts := xaiFacts(r)
	if facts["Resets"] != xaiLocalShort(xaiWeeklyEnd) || facts["Prepaid"] != "$1,234.56" || facts["Pay as you go (disabled)"] == "" || len(facts) != 3 {
		t.Errorf("facts = %+v", r.Facts)
	}
	if len(r.Notes) != 0 {
		t.Errorf("notes = %+v", r.Notes)
	}

	if len(r.Groups) != 2 {
		t.Fatalf("groups = %+v", r.Groups)
	}
	w := r.Groups[0].Windows
	if len(w) != 2 {
		t.Fatalf("windows = %+v", w)
	}
	weeklyEnd, _ := time.Parse(time.RFC3339Nano, xaiWeeklyEnd)
	if w[0].Label.EN != "Weekly limit" || w[0].Label.ZH != "周限额" || !w[0].Known || w[0].Remaining != 0.75 || !w[0].ResetAt.Equal(weeklyEnd) {
		t.Errorf("weekly = %+v", w[0])
	}
	monthEnd, _ := time.Parse(time.RFC3339Nano, xaiMonthEnd)
	if w[1].Label.EN != "Monthly credits" || w[1].Label.ZH != "月度积分" || !w[1].Known || !kimiNear(w[1].Remaining, 1-2500.0/15000) || w[1].Detail != "$125.00 / $150.00" || !w[1].ResetAt.Equal(monthEnd) {
		t.Errorf("monthly = %+v", w[1])
	}

	g := r.Groups[1]
	if g.Title.EN != "Usage breakdown (shared quota)" || len(g.Windows) != 3 {
		t.Fatalf("breakdown = %+v", g)
	}
	if g.Windows[0].Label.EN != "Chat usage" || !kimiNear(g.Windows[0].Remaining, 0.7) || !g.Windows[0].Known {
		t.Errorf("chat = %+v", g.Windows[0])
	}
	if g.Windows[1].Label.EN != "Product 2 usage" || !kimiNear(g.Windows[1].Remaining, 0.95) {
		t.Errorf("unnamed product = %+v", g.Windows[1])
	}
	if g.Windows[2].Label.EN != "Voice usage" || g.Windows[2].Known {
		t.Errorf("product without a percentage = %+v", g.Windows[2])
	}

	// Both billing reads and both plan reads use the Grok CLI headers, and
	// the user ID the credential names.
	for _, url := range []string{xaiBillingWeeklyURL, xaiBillingMonthlyURL, xaiUserURL, xaiSettingsURL} {
		reqs := stub.requests(url)
		if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].AuthIndex != "xai:1" {
			t.Fatalf("%s: requests = %+v", url, reqs)
		}
		h := reqs[0].Header
		if h["Authorization"] != "Bearer $TOKEN$" || h["x-xai-token-auth"] != "xai-grok-cli" || h["x-grok-client-version"] != "1.0.46" ||
			h["user-agent"] != "grok-pager/1.0.46 grok-shell/1.0.46 (macos; aarch64)" || h["accept"] != "*/*" || h["x-userid"] != "user-7" {
			t.Errorf("%s: headers = %+v", url, h)
		}
	}
	for _, req := range stub.sent {
		if req.Method != http.MethodGet {
			t.Errorf("a non-GET request was sent: %+v", req)
		}
	}
}

func TestXAIWeeklyUsageUnavailable(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{
		xaiBillingWeeklyURL:  codexOK(xaiWeeklyBody(``)),
		xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(``)),
	})
	r, err := fetchXAI(context.Background(), stub, xaiCred(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != 1 || len(r.Groups[0].Windows) != 1 {
		t.Fatalf("groups = %+v (the zero-budget monthly row hides while weekly data exists)", r.Groups)
	}
	w := r.Groups[0].Windows[0]
	if w.Label.EN != "Weekly limit" || w.Known {
		t.Errorf("weekly = %+v", w)
	}
	if len(r.Notes) != 1 || r.Notes[0].EN != "Usage unavailable from xAI" {
		t.Errorf("notes = %+v", r.Notes)
	}
	if r.Plan != "" {
		t.Errorf("plan = %q", r.Plan)
	}

	// A malformed percentage is just as unknown; an explicit zero is known.
	stub = newCodexStub(map[string]codexReply{xaiBillingWeeklyURL: codexOK(xaiWeeklyBody(`,"creditUsagePercent":"not-a-number"`)), xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(``))})
	if r, _ = fetchXAI(context.Background(), stub, xaiCred("")); r.Groups[0].Windows[0].Known {
		t.Error("a malformed percentage must stay unknown")
	}
	stub = newCodexStub(map[string]codexReply{xaiBillingWeeklyURL: codexOK(xaiWeeklyBody(`,"creditUsagePercent":0`)), xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(``))})
	if r, _ = fetchXAI(context.Background(), stub, xaiCred("")); !r.Groups[0].Windows[0].Known || r.Groups[0].Windows[0].Remaining != 1 {
		t.Errorf("an explicit zero is a full quota: %+v", r.Groups[0].Windows[0])
	}
}

func TestXAIMonthlyOnlyAndOnDemand(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{
		xaiBillingWeeklyURL:  {status: 403, body: `{"error":"Access denied"}`},
		xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(`,"monthlyLimit":{"val":150000},"used":{"val":160000},"onDemandCap":{"val":5000}`)),
		xaiUserURL:           codexOK(`{"subscription_tier":"SuperGrokHeavy"}`),
	})
	r, err := fetchXAI(context.Background(), stub, xaiCred(""))
	if err != nil {
		t.Fatal(err)
	}
	// With no settings display name, the tier names the plan.
	if r.Plan != "SuperGrokHeavy" {
		t.Errorf("plan = %q", r.Plan)
	}
	if got := xaiFacts(r)["Resets"]; got != xaiLocalShort(xaiMonthEnd) {
		t.Errorf("the billing end is the reset when there is no weekly period: %q", got)
	}
	w := r.Groups[0].Windows
	if len(w) != 2 {
		t.Fatalf("windows = %+v", w)
	}
	// $100 spent past the monthly limit against a $50 cap leaves nothing.
	if w[0].Label.EN != "Pay as you go" || w[0].Known != true || w[0].Remaining != 0 || w[0].Detail != "$0.00 / $50.00" {
		t.Errorf("on demand = %+v", w[0])
	}
	if w[1].Label.EN != "Monthly credits" || !w[1].Known || w[1].Remaining != 0 || w[1].Detail != "$0.00 / $1,500.00" {
		t.Errorf("monthly = %+v", w[1])
	}
	if _, has := xaiFacts(r)["Pay as you go (disabled)"]; has {
		t.Error("pay as you go is enabled")
	}
}

func TestXAIPlanFromMonthlyLimit(t *testing.T) {
	for limit, want := range map[string]string{"15000": "SuperGrok", "150000": "SuperGrok Heavy", "99": ""} {
		stub := newCodexStub(map[string]codexReply{
			xaiBillingWeeklyURL:  {status: 500},
			xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(`,"monthlyLimit":"` + limit + `","used":10`)),
		})
		r, err := fetchXAI(context.Background(), stub, xaiCred(""))
		if err != nil {
			t.Fatal(err)
		}
		if r.Plan != want {
			t.Errorf("limit %s: plan = %q, want %q", limit, r.Plan, want)
		}
	}
}

func TestXAIPlanReadsAreOptional(t *testing.T) {
	billing := map[string]codexReply{
		xaiBillingWeeklyURL:  codexOK(xaiWeeklyBody(`,"creditUsagePercent":10`)),
		xaiBillingMonthlyURL: codexOK(xaiMonthlyBody(``)),
	}
	for name, extra := range map[string]map[string]codexReply{
		"network":   {xaiUserURL: {err: errors.New("unavailable")}, xaiSettingsURL: {err: errors.New("unavailable")}},
		"http":      {xaiUserURL: {status: 503, body: `{}`}, xaiSettingsURL: {status: 503, body: `{}`}},
		"malformed": {xaiUserURL: codexOK(`not-json`), xaiSettingsURL: codexOK(`[]`)},
	} {
		replies := map[string]codexReply{}
		for k, v := range billing {
			replies[k] = v
		}
		for k, v := range extra {
			replies[k] = v
		}
		r, err := fetchXAI(context.Background(), newCodexStub(replies), xaiCred(""))
		if err != nil || r.Plan != "" || len(r.Groups) == 0 {
			t.Errorf("%s: r = %+v, err = %v", name, r, err)
		}
	}
	// The user tier alone is enough when settings fails.
	replies := map[string]codexReply{xaiUserURL: codexOK(`{"subscriptionTier":"SuperGrok"}`), xaiSettingsURL: {err: errors.New("unavailable")}}
	for k, v := range billing {
		replies[k] = v
	}
	if r, err := fetchXAI(context.Background(), newCodexStub(replies), xaiCred("")); err != nil || r.Plan != "SuperGrok" {
		t.Errorf("r = %+v, err = %v", r, err)
	}
}

func TestXAIBillingErrors(t *testing.T) {
	// Both failing: the weekly error, as the management center shows it.
	stub := newCodexStub(map[string]codexReply{
		xaiBillingWeeklyURL:  {status: 403, body: `{"error":"Access denied"}`},
		xaiBillingMonthlyURL: {status: 500, body: `{"error":"boom"}`},
	})
	_, err := fetchXAI(context.Background(), stub, xaiCred(""))
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 403 || ue.Message != "Access denied" {
		t.Fatalf("err = %v", err)
	}
	// Nothing in the answers: no data.
	stub = newCodexStub(map[string]codexReply{xaiBillingWeeklyURL: codexOK(`{"config":{}}`), xaiBillingMonthlyURL: {status: 500}})
	if _, err = fetchXAI(context.Background(), stub, xaiCred("")); err == nil || errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	// A credential without an auth index asks nothing.
	cr := xaiCred("")
	cr.AuthIndex = ""
	if _, err = fetchXAI(context.Background(), stub, cr); err == nil {
		t.Fatal("want an error")
	}
}

// The management center's paid fallback sends a chat completion, which spends
// usage. It must never be sent.
func TestXAINeverSendsChatCompletions(t *testing.T) {
	paid := `{"using_api":true,"prefix":"Paid"}`
	failing := func() map[string]codexReply {
		return map[string]codexReply{
			xaiBillingWeeklyURL:  {status: 403, body: `{"error":"Access denied"}`},
			xaiBillingMonthlyURL: {status: 403, body: `{"error":"Access denied"}`},
		}
	}

	replies := failing()
	replies[xaiMeURL] = codexOK(`{"user_id":"u"}`)
	replies[xaiSettingsURL] = codexOK(`{"subscription_tier_display":"SuperGrok"}`)
	stub := newCodexStub(replies)
	r, err := fetchXAI(context.Background(), stub, xaiCred(paid))
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "SuperGrok" || len(r.Notes) != 1 || len(r.Groups) != 0 {
		t.Errorf("r = %+v", r)
	}
	me := stub.requests(xaiMeURL)
	if len(me) != 1 || me[0].Method != http.MethodGet || me[0].Header["Authorization"] != "Bearer $TOKEN$" {
		t.Errorf("profile request = %+v", me)
	}
	for _, req := range stub.sent {
		if req.Method != http.MethodGet || req.URL == "https://api.x.ai/v1/chat/completions" || req.Data != "" {
			t.Errorf("request that could spend usage: %+v", req)
		}
	}

	// Without a plan name a paid credential is just "Paid".
	replies = failing()
	replies[xaiMeURL] = codexOK(`{}`)
	if r, err = fetchXAI(context.Background(), newCodexStub(replies), xaiCred(paid)); err != nil || r.Plan != "Paid" {
		t.Errorf("r = %+v, err = %v", r, err)
	}

	// The profile failing too: the billing error stands.
	_, err = fetchXAI(context.Background(), newCodexStub(failing()), xaiCred(paid))
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 403 {
		t.Errorf("err = %v", err)
	}

	// A credential that does not look paid never asks api.x.ai.
	stub = newCodexStub(failing())
	if _, err = fetchXAI(context.Background(), stub, xaiCred(`{"using_api":true}`)); err == nil {
		t.Fatal("want the billing error")
	}
	if len(stub.requests(xaiMeURL)) != 0 {
		t.Error("an unmarked credential must not be probed")
	}
}

func TestXAIPaidRecognition(t *testing.T) {
	jwt := func(claims string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	}
	for raw, want := range map[string]bool{
		`{"using_api":true,"prefix":"PAID"}`:                                true,
		`{"metadata":{"using_api":"yes"},"attributes":{"prefix":"paid"}}`:   true,
		`{"metadata":{"access_token":"` + jwt(`{"tier":1}`) + `"}}`:         true,
		`{"oauth":{"idToken":"` + jwt(`{"https://x.ai/tier":"2"}`) + `"}}`:  true,
		`{"token":"` + jwt(`{"tier":0}`) + `"}`:                             false,
		`{"access_token":"` + jwt(`{"name":"x"}`) + `"}`:                    false,
		`{"access_token":"not-a-jwt"}`:                                      false,
		`{"using_api":true}`:                                                false,
		`{"prefix":"paid"}`:                                                 false,
		`{"using_api":false,"prefix":"paid"}`:                               false,
		`{"base_url":"https://api.x.ai/v1"}`:                                false,
		`{"a":{"b":{"c":{"metadata":{"using_api":true,"prefix":"paid"}}}}}`: false, // nested too deep
	} {
		var file map[string]any
		_ = json.Unmarshal([]byte(raw), &file)
		if got := xaiIsPaid(file); got != want {
			t.Errorf("%s: paid = %v, want %v", raw, got, want)
		}
	}
}

func TestXAIUserID(t *testing.T) {
	for raw, want := range map[string]string{
		`{"sub":"a","metadata":{"sub":"b"}}`:         "a",
		`{"metadata":{"user_id":"b"}}`:               "b",
		`{"attributes":{"userId":"c"}}`:              "c",
		`{"oauth":{"subject":"d"}}`:                  "d",
		`{"metadata":{"user":{"id":"e"}}}`:           "e",
		`{"metadata":{"oauth":{"sub":"f"}}}`:         "f",
		`{"sub":12,"oauth":{"sub":"ignored-later"}}`: "12",
		`{}`: "",
	} {
		var file map[string]any
		_ = json.Unmarshal([]byte(raw), &file)
		if got := xaiUserID(file); got != want {
			t.Errorf("%s: user = %q, want %q", raw, got, want)
		}
	}
}

func TestXAIMerge(t *testing.T) {
	weekly := xaiBuildSummary(map[string]any{
		"currentPeriod":      map[string]any{"type": "weekly", "start": xaiWeeklyStart, "end": xaiWeeklyEnd},
		"creditUsagePercent": 40.0,
	})
	monthly := xaiBuildSummary(map[string]any{
		"monthlyLimit": 10000.0, "used": map[string]any{"val": "2500"},
		"billingPeriodStart": "2026-09-01T00:00:00Z", "billingPeriodEnd": xaiMonthEnd,
	})
	if weekly == nil || monthly == nil || weekly.periodType != "weekly" || monthly.periodType != "monthly" {
		t.Fatalf("weekly = %+v, monthly = %+v", weekly, monthly)
	}
	m := xaiMerge(weekly, monthly)
	// The weekly period stays whole: the monthly dates do not leak into it.
	if m.periodType != "weekly" || *m.usagePercent != 40 || m.periodEnd != xaiWeeklyEnd || m.billingPeriodEnd != xaiMonthEnd {
		t.Errorf("merged = %+v", m)
	}
	if *m.monthlyLimit != 10000 || *m.includedUsed != 2500 || *m.usedPercent != 25 {
		t.Errorf("merged amounts = %+v", m)
	}
	if got := xaiMerge(nil, monthly); got != monthly {
		t.Error("nothing to merge with")
	}
	if got := xaiMerge(weekly, nil); got != weekly {
		t.Error("nothing to merge with")
	}
	if xaiBuildSummary(nil) != nil || xaiBuildSummary(map[string]any{}) != nil {
		t.Error("an empty config has no data")
	}
	// Spending beyond the limit: only the limit counts as included.
	over := xaiBuildSummary(map[string]any{"monthlyLimit": 100.0, "used": 250.0, "onDemandCap": 300.0})
	if *over.includedUsed != 100 || *over.onDemandUsed != 150 || *over.onDemandUsedPct != 50 || *over.usedPercent != 100 {
		t.Errorf("over = %+v", over)
	}
}

func TestXAIMoney(t *testing.T) {
	for cents, want := range map[float64]string{0: "$0.00", 5: "$0.05", 12345: "$123.45", 123456789: "$1,234,567.89", -250: "-$2.50", 100000: "$1,000.00"} {
		if got := xaiUSD(cents); got != want {
			t.Errorf("xaiUSD(%v) = %q, want %q", cents, got, want)
		}
	}
}

func TestXAIRegisteredForGrok(t *testing.T) {
	p, ok := builtin["xai"]
	if !ok || p.Fetch == nil || p.Act != nil {
		t.Fatalf("xai provider = %+v", p)
	}
	for _, name := range []string{"x-ai", "grok", "xai"} {
		if got := ProviderKey(cpa.Credential{Provider: name}); got != "xai" {
			t.Errorf("ProviderKey(%s) = %q", name, got)
		}
	}
}
