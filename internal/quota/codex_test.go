package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// codexStub is a Caller that answers upstream requests from canned replies
// keyed by URL, and records what was sent. It is shared by the Codex, Kimi
// and xAI tests.
type codexStub struct {
	mu       sync.Mutex
	replies  map[string]codexReply
	sent     []cpa.UpstreamRequest
	cooled   []string
	coolErr  error
	hold     chan struct{} // when set, POSTs wait for it to close
	entered  chan struct{} // closed when the first POST arrived
	enterOne sync.Once
}

type codexReply struct {
	status int
	body   string
	err    error
}

func newCodexStub(replies map[string]codexReply) *codexStub {
	return &codexStub{replies: replies}
}

func codexOK(body string) codexReply { return codexReply{status: 200, body: body} }

func (s *codexStub) Upstream(_ context.Context, r cpa.UpstreamRequest) (*cpa.UpstreamResponse, error) {
	s.mu.Lock()
	s.sent = append(s.sent, r)
	rep, found := s.replies[r.URL]
	hold, entered := s.hold, s.entered
	s.mu.Unlock()
	if r.Method == http.MethodPost && hold != nil {
		s.enterOne.Do(func() { close(entered) })
		<-hold
	}
	if !found {
		return &cpa.UpstreamResponse{StatusCode: 404, Body: `{"error":"not stubbed"}`}, nil
	}
	if rep.err != nil {
		return nil, rep.err
	}
	return &cpa.UpstreamResponse{StatusCode: rep.status, Body: rep.body}, nil
}

func (s *codexStub) Plugins(context.Context) (*cpa.PluginList, error) { return nil, nil }
func (s *codexStub) PluginQuota(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func (s *codexStub) ResetCooldown(_ context.Context, authIndex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooled = append(s.cooled, authIndex)
	return s.coolErr
}

// requests returns what was sent to a URL.
func (s *codexStub) requests(url string) []cpa.UpstreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cpa.UpstreamRequest
	for _, r := range s.sent {
		if r.URL == url {
			out = append(out, r)
		}
	}
	return out
}

func codexCred(raw string) cpa.Credential {
	cr := cpa.Credential{Name: "codex.json", AuthIndex: "codex:1", Type: "codex"}
	if raw != "" {
		cr.Raw = json.RawMessage(raw)
	}
	return cr
}

const codexTestUsage = `{
  "plan_type": "pro",
  "rate_limit": {"allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 1, "limit_window_seconds": 604800, "reset_after_seconds": 601888, "reset_at": 1785902974},
    "secondary_window": null},
  "code_review_rate_limit": null,
  "additional_rate_limits": [{"limit_name": "GPT-5.3-Codex-Spark", "metered_feature": "codex_bengalfox",
    "rate_limit": {"allowed": true, "limit_reached": false,
      "primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 602111, "reset_at": 1785903197},
      "secondary_window": null}}],
  "credits": {"has_credits": true, "unlimited": false, "balance": "8.75"},
  "rate_limit_reset_credits": {"available_count": 1, "applicable_available_count": 0}
}`

func TestCodexFetch(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{
		codexUsageURL: codexOK(codexTestUsage),
		codexSubscriptionURL + "?account_id=account%2Fid%20%2B%20space": codexOK(`{"active_until":"2026-10-03T13:27:01Z"}`),
		codexResetCreditsURL: codexOK(`{"available_count":2,"credits":[
			{"id":"a","reset_type":"codex_rate_limits","status":"available","expires_at":"2026-11-01T08:30:00Z"},
			{"id":"b","reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-11-02T08:30:00Z"},
			{"id":"c","reset_type":"other","status":"available","expires_at":"2026-11-03T08:30:00Z"},
			{"id":"d","reset_type":"codex_rate_limits","status":"available","expires_at":"2026-12-01T08:30:00Z"}]}`),
	})
	cr := codexCred(`{"metadata":{"chatgpt_account_id":"account/id + space"},"chatgpt_subscription_active_until":"2026-09-03T13:27:01Z"}`)

	r, err := fetchCodex(context.Background(), stub, cr)
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "Pro 200" {
		t.Errorf("plan = %q", r.Plan)
	}

	if len(r.Groups) != 1 || len(r.Groups[0].Windows) != 2 {
		t.Fatalf("groups = %+v", r.Groups)
	}
	w := r.Groups[0].Windows
	if w[0].Label.EN != "Weekly limit" || w[0].Label.ZH != "周限额" || !w[0].Known || w[0].Remaining != 0.99 {
		t.Errorf("weekly window = %+v", w[0])
	}
	if !w[0].ResetAt.Equal(time.Unix(1785902974, 0)) {
		t.Errorf("weekly reset = %v", w[0].ResetAt)
	}
	if w[1].Label.EN != "GPT-5.3-Codex-Spark weekly limit" || w[1].Label.ZH != "GPT-5.3-Codex-Spark 周限额" || w[1].Remaining != 1 || !w[1].Known {
		t.Errorf("additional window = %+v", w[1])
	}

	want := map[string]string{
		"Renewal time":                  time.Date(2026, 10, 3, 13, 27, 1, 0, time.UTC).Local().Format("2006-01-02"), // the live date wins over the file's
		"Credit balance":                "8.75",
		"Manual resets":                 "2",
		"Manual reset expiry · Reset 1": time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC).Local().Format("01/02 15:04"),
		"Manual reset expiry · Reset 2": time.Date(2026, 12, 1, 8, 30, 0, 0, time.UTC).Local().Format("01/02 15:04"),
	}
	got := map[string]string{}
	for _, f := range r.Facts {
		got[f.Label.EN] = f.Value
	}
	if len(got) != len(want) {
		t.Errorf("facts = %+v", r.Facts)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("fact %q = %q, want %q", k, got[k], v)
		}
	}
	if len(r.Notes) != 0 {
		t.Errorf("notes = %+v", r.Notes)
	}

	if n := len(stub.requests(codexUsageURL)); n != 1 {
		t.Errorf("usage requests = %d", n)
	}
	sub := stub.requests(codexSubscriptionURL + "?account_id=account%2Fid%20%2B%20space")
	if len(sub) != 1 || sub[0].Header["Chatgpt-Account-Id"] != "account/id + space" || sub[0].Header["Authorization"] != "Bearer $TOKEN$" || sub[0].AuthIndex != "codex:1" {
		t.Errorf("subscription request = %+v", sub)
	}
	cred := stub.requests(codexResetCreditsURL)
	if len(cred) != 1 || cred[0].Header["OpenAI-Beta"] != "codex-1" || cred[0].Header["Originator"] != "Codex Desktop" || cred[0].Header["Accept"] != "application/json" {
		t.Errorf("reset credits request = %+v", cred)
	}
	if cred[0].Header["User-Agent"] != codexUserAgent {
		t.Errorf("user agent = %q", cred[0].Header["User-Agent"])
	}

	if len(r.Actions) != 1 || r.Actions[0].ID != "codex-reset" || r.Actions[0].Disabled || r.Actions[0].Confirm.EN == "" {
		t.Errorf("actions = %+v", r.Actions)
	}
}

func TestCodexWindowClassification(t *testing.T) {
	usage := `{
	  "plan_type": "TEAM",
	  "rate_limit": {"allowed": false, "limit_reached": true,
	    "primary_window": {"used_percent": 40, "limit_window_seconds": 18000, "reset_after_seconds": 3600},
	    "secondary_window": {"limit_window_seconds": 2592000, "reset_at": 1785902974}},
	  "code_review_rate_limit": {"limit_reached": false,
	    "primary_window": {"used_percent": "25", "reset_at": 1785902974},
	    "secondary_window": {"used_percent": 75, "reset_at": 1785902974}},
	  "additional_rate_limits": [
	    {"metered_feature": "feature_x", "rate_limit": {"primary_window": {"used_percent": 10, "limit_window_seconds": 18000}, "secondary_window": {"used_percent": 20, "limit_window_seconds": 2678400}}},
	    {"rate_limit": null},
	    {"rate_limit": {"secondary_window": {"used_percent": 5, "limit_window_seconds": 604800}}}
	  ]
	}`
	stub := newCodexStub(map[string]codexReply{codexUsageURL: codexOK(usage), codexResetCreditsURL: codexReply{status: 500, body: `{"error":"boom"}`}})
	before := time.Now()
	r, err := fetchCodex(context.Background(), stub, codexCred(""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "Team" {
		t.Errorf("plan = %q", r.Plan)
	}
	type row struct {
		label     string
		remaining float64
		known     bool
	}
	var got []row
	for _, w := range r.Groups[0].Windows {
		got = append(got, row{w.Label.EN, w.Remaining, w.Known})
	}
	want := []row{
		{"5-hour limit", 0.6, true},
		{"Monthly limit", 0, true}, // limit reached, no percentage, a reset time: fully used
		{"Code review 5-hour limit", 0.75, true},
		{"Code review weekly limit", 0.25, true},
		{"feature_x 5-hour limit", 0.9, true},
		{"feature_x monthly limit", 0.8, true},
		{"additional-3 weekly limit", 0.95, true},
	}
	if len(got) != len(want) {
		t.Fatalf("windows = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("window %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// reset_after_seconds is relative to the fetch.
	five := r.Groups[0].Windows[0].ResetAt
	if d := five.Sub(before); d < time.Hour-time.Second || d > time.Hour+time.Minute {
		t.Errorf("five-hour reset in %v", d)
	}
	// The reset credits read failed: a note, not an error.
	if len(r.Notes) != 1 || r.Notes[0].EN != "Failed to load manual reset expiry: 500 boom" {
		t.Errorf("notes = %+v", r.Notes)
	}
}

func TestCodexLimitReachedWithoutResetStaysUnknown(t *testing.T) {
	usage := `{"rate_limit":{"allowed":false,"primary_window":{"limit_window_seconds":18000}}}`
	stub := newCodexStub(map[string]codexReply{codexUsageURL: codexOK(usage)})
	r, err := fetchCodex(context.Background(), stub, codexCred(""))
	if err != nil {
		t.Fatal(err)
	}
	if w := r.Groups[0].Windows[0]; w.Known || !w.ResetAt.IsZero() {
		t.Errorf("window = %+v", w)
	}
}

func TestCodexOptionalRequestsDoNotFailFetch(t *testing.T) {
	cr := codexCred(`{"chatgpt_account_id":"acct-2","plan_type":"plus","chatgpt_subscription_active_until":"2026-09-03T13:27:01Z"}`)
	usage := `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}},"rate_limit_reset_credits":{"available_count":3}}`
	for name, replies := range map[string]map[string]codexReply{
		"http error":    {codexSubscriptionURL + "?account_id=acct-2": {status: 503, body: `{"error":"temporarily unavailable"}`}, codexResetCreditsURL: {status: 503, body: `{"error":"down"}`}},
		"transport":     {codexSubscriptionURL + "?account_id=acct-2": {err: errors.New("timeout")}, codexResetCreditsURL: {err: errors.New("timeout")}},
		"invalid shape": {codexResetCreditsURL: codexOK(`{"unexpected":true}`)},
	} {
		replies[codexUsageURL] = codexOK(usage)
		stub := newCodexStub(replies)
		r, err := fetchCodex(context.Background(), stub, cr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.Plan != "Plus" {
			t.Errorf("%s: plan = %q (the file's plan fills in)", name, r.Plan)
		}
		got := map[string]string{}
		for _, f := range r.Facts {
			got[f.Label.EN] = f.Value
		}
		// The credential's own renewal date and the usage block's count fill in.
		if got["Renewal time"] != time.Date(2026, 9, 3, 13, 27, 1, 0, time.UTC).Local().Format("2006-01-02") || got["Manual resets"] != "3" {
			t.Errorf("%s: facts = %+v", name, r.Facts)
		}
		if len(r.Notes) != 1 {
			t.Errorf("%s: notes = %+v", name, r.Notes)
		}
		if r.Actions[0].Disabled {
			t.Errorf("%s: reset should stay possible with a count of 3", name)
		}
	}
}

func TestCodexAccountFromIDToken(t *testing.T) {
	// A JWT's payload, base64url: {"chatgpt_account_id":"jwt-acct","plan_type":"free"}.
	jwt := "h.eyJjaGF0Z3B0X2FjY291bnRfaWQiOiJqd3QtYWNjdCIsInBsYW5fdHlwZSI6ImZyZWUifQ.s"
	for name, raw := range map[string]string{
		"object": `{"id_token":{"chatgpt_account_id":"jwt-acct"}}`,
		"jwt":    `{"metadata":{"id_token":"` + jwt + `"}}`,
		"nested": `{"attributes":{"chatgptAccountId":"jwt-acct"}}`,
	} {
		if id := codexFileOf(codexCred(raw)).accountID(); id != "jwt-acct" {
			t.Errorf("%s: account = %q", name, id)
		}
	}
	if id := codexFileOf(codexCred("")).accountID(); id != "" {
		t.Errorf("empty credential: account = %q", id)
	}
}

func TestCodexFetchErrors(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{codexUsageURL: {status: 403, body: `{"error":{"message":"denied"}}`}})
	_, err := fetchCodex(context.Background(), stub, codexCred(""))
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 403 || ue.Message != "denied" {
		t.Fatalf("err = %v", err)
	}
	stub = newCodexStub(map[string]codexReply{codexUsageURL: codexOK("")})
	if _, err := fetchCodex(context.Background(), stub, codexCred("")); err == nil {
		t.Fatal("an empty answer should be an error")
	}
	// No windows at all is a success that says there is nothing to show.
	stub = newCodexStub(map[string]codexReply{codexUsageURL: codexOK(`{"plan_type":"free"}`)})
	r, err := fetchCodex(context.Background(), stub, codexCred(""))
	if err != nil || r.Plan != "Free" || len(r.Groups) != 0 || len(r.Notes) == 0 {
		t.Fatalf("r = %+v, err = %v", r, err)
	}
}

func TestCodexResetActionDisabled(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{
		codexUsageURL:        codexOK(`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}`),
		codexResetCreditsURL: codexOK(`{"available_count":0,"credits":[]}`),
	})
	r, err := fetchCodex(context.Background(), stub, codexCred(""))
	if err != nil {
		t.Fatal(err)
	}
	if a := r.Actions[0]; !a.Disabled || a.Reason.IsZero() {
		t.Errorf("action = %+v", a)
	}
	cr := codexCred("")
	cr.RuntimeOnly = true
	if a := codexResetAction(cr, 2, true); !a.Disabled {
		t.Errorf("a runtime-only credential cannot be reset: %+v", a)
	}
}

var codexUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCodexConsume(t *testing.T) {
	for _, code := range []string{"reset", "already_redeemed"} {
		stub := newCodexStub(map[string]codexReply{codexConsumeURL: codexOK(`{"code":"` + code + `","windows_reset":0}`)})
		cr := codexCred(`{"chatgpt_account_id":"acct-9"}`)
		text, err := actCodex(context.Background(), stub, cr, "codex-reset")
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		if text.EN != `Reset Codex quota for "codex.json"` {
			t.Errorf("%s: text = %+v", code, text)
		}
		sent := stub.requests(codexConsumeURL)
		if len(sent) != 1 {
			t.Fatalf("%s: consume sent %d times", code, len(sent))
		}
		req := sent[0]
		var body map[string]string
		if json.Unmarshal([]byte(req.Data), &body) != nil || !codexUUID.MatchString(body["redeem_request_id"]) || len(body) != 1 {
			t.Errorf("%s: body = %q", code, req.Data)
		}
		if req.Method != http.MethodPost || req.AuthIndex != "codex:1" || req.Header["Chatgpt-Account-Id"] != "acct-9" || req.Header["Authorization"] != "Bearer $TOKEN$" || req.Header["User-Agent"] != codexUserAgent {
			t.Errorf("%s: request = %+v", code, req)
		}
		if len(stub.cooled) != 1 || stub.cooled[0] != "codex:1" {
			t.Errorf("%s: cooldown cleared %v", code, stub.cooled)
		}
	}
}

func TestCodexConsumeEveryAttemptUsesAFreshRequestID(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{codexConsumeURL: codexOK(`{"code":"reset"}`)})
	ids := map[string]bool{}
	for range 3 {
		if _, err := actCodex(context.Background(), stub, codexCred(""), "codex-reset"); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range stub.requests(codexConsumeURL) {
		var body map[string]string
		_ = json.Unmarshal([]byte(r.Data), &body)
		ids[body["redeem_request_id"]] = true
	}
	if len(ids) != 3 {
		t.Errorf("request IDs = %v", ids)
	}
}

func TestCodexConsumeRefusalsAndUnknownOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		reply   codexReply
		unknown bool
	}{
		{"no credit", codexOK(`{"code":"no_credit"}`), false},
		{"nothing to reset", codexOK(`{"code":"nothing_to_reset"}`), false},
		{"unrecognized code", codexOK(`{"code":"unknown"}`), true},
		{"no code", codexOK(`{}`), true},
		{"not json", codexOK(`<html>`), true},
		{"transport error", codexReply{err: errors.New("timeout")}, true},
		{"server error", codexReply{status: 503, body: `{"error":"busy"}`}, true},
		{"rejected", codexReply{status: 403, body: `{"error":"nope"}`}, false},
	}
	for _, tt := range tests {
		stub := newCodexStub(map[string]codexReply{codexConsumeURL: tt.reply})
		_, err := actCodex(context.Background(), stub, codexCred(""), "codex-reset")
		var m *Msg
		if !errors.As(err, &m) {
			t.Fatalf("%s: err = %v", tt.name, err)
		}
		if m.Unknown != tt.unknown {
			t.Errorf("%s: Unknown = %v, want %v (%+v)", tt.name, m.Unknown, tt.unknown, m)
		}
		if n := len(stub.requests(codexConsumeURL)); n != 1 {
			t.Errorf("%s: consume sent %d times; it must never be retried", tt.name, n)
		}
		if len(stub.cooled) != 0 {
			t.Errorf("%s: cooldown cleared without a confirmed reset", tt.name)
		}
		if m.EN == "" || m.ZH == "" {
			t.Errorf("%s: message missing a language: %+v", tt.name, m)
		}
	}
}

func TestCodexConsumeCooldownFailureDoesNotSpendAgain(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{codexConsumeURL: codexOK(`{"code":"reset"}`)})
	stub.coolErr = errors.New("offline")
	_, err := actCodex(context.Background(), stub, codexCred(""), "codex-reset")
	var m *Msg
	if !errors.As(err, &m) || m.Unknown || m.EN == "" {
		t.Fatalf("err = %v", err)
	}
	if n := len(stub.requests(codexConsumeURL)); n != 1 {
		t.Errorf("consume sent %d times", n)
	}
}

func TestCodexConsumeRefusesWhileRunning(t *testing.T) {
	stub := newCodexStub(map[string]codexReply{codexConsumeURL: codexOK(`{"code":"reset"}`)})
	stub.hold, stub.entered = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := actCodex(context.Background(), stub, codexCred(""), "codex-reset")
		done <- err
	}()
	<-stub.entered
	_, err := actCodex(context.Background(), stub, codexCred(""), "codex-reset")
	var m *Msg
	if !errors.As(err, &m) || m.Unknown {
		t.Errorf("a second reset while one runs: %v", err)
	}
	close(stub.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := len(stub.requests(codexConsumeURL)); n != 1 {
		t.Errorf("consume sent %d times", n)
	}
}

func TestCodexActRejectsOtherActions(t *testing.T) {
	stub := newCodexStub(nil)
	if _, err := actCodex(context.Background(), stub, codexCred(""), "claude-reset"); err == nil {
		t.Fatal("unknown action accepted")
	}
	cr := codexCred("")
	cr.AuthIndex = ""
	if _, err := actCodex(context.Background(), stub, cr, "codex-reset"); err == nil || len(stub.sent) != 0 {
		t.Fatal("a credential without an auth index must not send anything")
	}
}

func TestCodexPlanLabels(t *testing.T) {
	for in, want := range map[string]string{
		"pro": "Pro 200", "prolite": "Pro 100", "pro-lite": "Pro 100", "pro_lite": "Pro 100",
		"self_serve_business_prolite": "Business Premium", "plus": "Plus", "team": "Team", "free": "Free",
		"enterprise": "Enterprise", "": "",
	} {
		if got := codexPlanLabel(in); got != want {
			t.Errorf("codexPlanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexCredits(t *testing.T) {
	for _, tt := range []struct {
		in        string
		balance   string
		unlimited bool
	}{
		{`{"has_credits":false,"unlimited":false,"balance":"0"}`, "0", false},
		{`{"balance":" 12.50 "}`, "12.50", false},
		{`{"unlimited":true,"balance":null}`, "", true},
		{`{"balance":"not available"}`, "", false},
		{`{"balance":-1}`, "", false},
		{`{"balance":3}`, "3", false},
		{`null`, "", false},
	} {
		var v any
		_ = json.Unmarshal([]byte(tt.in), &v)
		if b, u := codexAccountCredits(v); b != tt.balance || u != tt.unlimited {
			t.Errorf("%s: balance %q unlimited %v", tt.in, b, u)
		}
	}
}

func TestCodexRegistered(t *testing.T) {
	p, ok := builtin["codex"]
	if !ok || p.Fetch == nil || p.Act == nil {
		t.Fatalf("codex provider = %+v", p)
	}
}
