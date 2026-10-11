package quota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Claude, as the management center reads it: the usage windows of
// /api/oauth/usage, the plan from /api/oauth/profile, and the banked
// resets ("cedar_ember" grants) the account may spend to clear its limits.

const (
	claudeOrigin     = "https://api.anthropic.com"
	claudeUsageURL   = claudeOrigin + "/api/oauth/usage"
	claudeProfileURL = claudeOrigin + "/api/oauth/profile"
	claudeGrantsURL  = claudeOrigin + "/api/oauth/usage?cedar_ember=1&skip_spend=1"
	claudeProgram    = "cedar_ember"
	// claudeRetryWindow is how long an unknown claim may be retried with
	// its original request ID.
	claudeRetryWindow = 10 * time.Minute
)

func claudeHeaders() map[string]string {
	return map[string]string{
		"User-Agent":     "claude-cli/2.1.280 (external, cli)",
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}
}

var claudeWindows = []struct {
	key   string
	label Text
	kind  WindowKind
}{
	{"five_hour", Text{"5 小时限额", "5-hour limit"}, KindShort},
	{"seven_day", Text{"7 天限额", "7-day limit"}, KindLong},
	{"seven_day_oauth_apps", Text{"7 天 OAuth 应用", "7-day OAuth apps"}, KindOther},
	{"seven_day_opus", Text{"7 天 Opus", "7-day Opus"}, KindOther},
	{"seven_day_sonnet", Text{"7 天 Sonnet", "7-day Sonnet"}, KindOther},
	{"seven_day_cowork", Text{"7 天 Cowork", "7-day Cowork"}, KindOther},
	{"iguana_necktie", Text{"7 天 Fable 5", "7-day Fable 5"}, KindOther},
}

func init() {
	register("claude", Provider{Fetch: fetchClaude, Act: actClaude})
}

func fetchClaude(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	var (
		wg                 sync.WaitGroup
		profile, grants    map[string]any
		profErr, grantsErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		profile, profErr = call(ctx, c, cr, http.MethodGet, claudeProfileURL, claudeHeaders(), "")
	}()
	go func() {
		defer wg.Done()
		grants, grantsErr = call(ctx, c, cr, http.MethodGet, claudeGrantsURL, claudeHeaders(), "")
	}()
	usage, err := call(ctx, c, cr, http.MethodGet, claudeUsageURL, claudeHeaders(), "")
	wg.Wait()
	if err != nil {
		return nil, err
	}

	r := &Result{Groups: []Group{{Windows: claudeUsageWindows(usage)}}}
	if profErr == nil {
		r.Plan = claudePlan(profile)
	}
	if x := get(usage, "extra_usage"); x != nil {
		if on, _ := flag(get(x, "is_enabled")); on {
			used, _ := num(get(x, "used_credits"))
			limit, _ := num(get(x, "monthly_limit"))
			r.Facts = append(r.Facts, Fact{Text{"额外用量", "Extra usage"}, fmt.Sprintf("$%.2f / $%.2f", used/100, limit/100)})
		}
	}

	var status *claudeGrantStatus
	if grantsErr == nil {
		status = parseClaudeGrants(get(grants, "cedar_ember"))
	}
	if status != nil {
		left := 0
		for _, g := range status.grants {
			left += g.left
		}
		r.Facts = append([]Fact{{Text{"剩余重置次数", "Resets remaining"}, fmt.Sprint(left)}}, r.Facts...)
	}
	r.Actions = []Action{claudeResetAction(c, cr, status, grantsErr != nil, time.Now())}
	return r, nil
}

func claudeUsageWindows(usage map[string]any) []Window {
	fable := claudeFableLimit(usage)
	var out []Window
	for _, w := range claudeWindows {
		win, ok := get(usage, w.key).(map[string]any)
		if !ok {
			continue
		}
		if _, has := win["utilization"]; !has {
			continue
		}
		used, known := num(win["utilization"])
		reset := instant(win["resets_at"])
		if w.key == "iguana_necktie" {
			credits := false
			for _, k := range []string{"limit_dollars", "used_dollars", "remaining_dollars"} {
				if _, ok := num(win[k]); ok {
					credits = true
				}
			}
			if credits {
				out = append(out, usedWindow(Text{"云端会话额度", "Cloud session credits"}, used, known, reset))
				continue
			}
			if fable != nil {
				continue // the scoped limit below says the same, better
			}
		}
		limit := usedWindow(w.label, used, known, reset)
		limit.Kind = w.kind
		out = append(out, limit)
	}
	if fable != nil {
		used, _ := num(get(fable, "percent"))
		out = append(out, usedWindow(Text{"7 天 Fable 5", "7-day Fable 5"}, used, true, instant(get(fable, "resets_at"))))
	}
	return out
}

// claudeFableLimit finds the weekly limit scoped to Fable, the active one
// first.
func claudeFableLimit(usage map[string]any) any {
	limits, _ := get(usage, "limits").([]any)
	var cands []any
	for _, l := range limits {
		name := strings.ToLower(str(get(l, "scope", "model", "display_name")))
		_, hasPercent := num(get(l, "percent"))
		if strings.ToLower(str(get(l, "kind"))) == "weekly_scoped" && (name == "fable" || name == "fable 5") && hasPercent {
			cands = append(cands, l)
		}
	}
	for _, l := range cands {
		if active, ok := get(l, "is_active").(bool); ok && active {
			return l
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return nil
}

func claudePlan(profile map[string]any) string {
	org := get(profile, "organization")
	if strings.ToLower(str(get(org, "organization_type"))) == "claude_team" && strings.ToLower(str(get(org, "subscription_status"))) == "active" {
		return "Team"
	}
	maxOn, maxOK := flag(get(profile, "account", "has_claude_max"))
	if maxOn {
		return "Max"
	}
	proOn, proOK := flag(get(profile, "account", "has_claude_pro"))
	if proOn {
		return "Pro"
	}
	if maxOK && proOK {
		return "Free"
	}
	return ""
}

// ---- Banked resets ----

type claudeGrant struct {
	id             string
	left           int
	startsAt       time.Time
	endsAt         time.Time
	paused         bool
	usableNow      bool
	requiresLimit  bool
	hasStart, hasE bool
}

type claudeGrantStatus struct {
	eligible      bool
	atLimit       bool
	grants        []claudeGrant
	next          string
	cooldownUntil time.Time
}

var (
	claudeGrantID   = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	claudeOrgUUID   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	claudeResults   = []string{"reset", "already_used", "not_limited", "cooldown", "ineligible", "unavailable"}
	claudeOutcomeTx = map[string]Text{
		"reset":        {"已重置用量限制。", "Usage limits reset."},
		"already_used": {"该权益已使用。", "This grant was already used."},
		"not_limited":  {"未重置：账户当前未达到限制。", "No reset: the account is not currently limited."},
		"cooldown":     {"未重置：仍在冷却期。", "No reset: cooldown is active."},
		"ineligible":   {"此账户不符合资格。", "This account is not eligible."},
		"unavailable":  {"重置暂不可用。", "Resets are currently unavailable."},
		"rate_limited": {"请求过于频繁。", "Request rate limited."},
		"auth_error":   {"授权被拒绝。", "Authorization was refused."},
	}
)

// parseClaudeGrants reads the cedar_ember block strictly, as the
// management center does: one malformed grant or timestamp rejects the
// whole block (nil), and a grant that does not say it is usable is not.
func parseClaudeGrants(block any) *claudeGrantStatus {
	m, ok := block.(map[string]any)
	if !ok {
		return nil
	}
	eligible, ok := m["eligible"].(bool)
	if !ok {
		return nil
	}
	st := &claudeGrantStatus{eligible: eligible}
	optBool := func(v any, def bool) (bool, bool) {
		if v == nil {
			return def, true
		}
		b, ok := v.(bool)
		return b, ok
	}
	optTime := func(v any) (time.Time, bool, bool) {
		if v == nil {
			return time.Time{}, false, true
		}
		s, ok := v.(string)
		if !ok {
			return time.Time{}, false, false
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		return t, err == nil, err == nil
	}
	count := func(v any) (int, bool) {
		f, ok := v.(float64)
		return int(f), ok && f >= 0 && f == float64(int(f))
	}
	if st.atLimit, ok = optBool(m["at_limit"], false); !ok {
		return nil
	}
	if r, ok := m["ineligible_reason"]; ok && r != nil {
		if _, isStr := r.(string); !isStr {
			return nil
		}
	}
	var good bool
	if st.cooldownUntil, _, good = optTime(m["cooldown_until"]); !good {
		return nil
	}
	if _, _, good = optTime(m["weekly_resets_at"]); !good {
		return nil
	}
	raw, ok := m["grants"].([]any)
	if m["grants"] != nil && !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, x := range raw {
		g, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		id, _ := g["id"].(string)
		total, okT := count(g["resets_total"])
		left, okL := count(g["resets_left"])
		if !claudeGrantID.MatchString(id) || !okT || !okL || left > total || seen[id] {
			return nil
		}
		gr := claudeGrant{id: id, left: left}
		var okS, okE, okP, okU, okR bool
		gr.startsAt, gr.hasStart, okS = optTime(g["starts_at"])
		gr.endsAt, gr.hasE, okE = optTime(g["ends_at"])
		gr.paused, okP = optBool(g["paused"], false)
		gr.usableNow, okU = optBool(g["usable_now"], false)
		gr.requiresLimit, okR = optBool(g["use_requires_limit"], true)
		if c, present := g["clears"]; present && c != nil {
			if _, isList := c.([]any); !isList {
				return nil
			}
		}
		if !okS || !okE || !okP || !okU || !okR {
			return nil
		}
		seen[id] = true
		st.grants = append(st.grants, gr)
	}
	if next, _ := m["next_grant_id"].(string); seen[next] {
		st.next = next
	}
	return st
}

// blocker says why a grant cannot be spent now; "" when it can.
func (st *claudeGrantStatus) blocker(id string) string {
	if !st.eligible {
		return "ineligible"
	}
	i := slices.IndexFunc(st.grants, func(g claudeGrant) bool { return g.id == id })
	if i < 0 {
		return "unknown_grant"
	}
	g := st.grants[i]
	switch {
	case g.paused:
		return "paused"
	case !g.usableNow:
		return "not_usable"
	case g.left <= 0:
		return "exhausted"
	case g.requiresLimit && !st.atLimit:
		return "not_limited"
	}
	return ""
}

// pick chooses the grant to spend: the one upstream recommends, else the
// first usable by ID; "" when none is usable now.
func (st *claudeGrantStatus) pick(now time.Time) string {
	if now.Before(st.cooldownUntil) {
		return ""
	}
	var usable []string
	for _, g := range st.grants {
		if st.blocker(g.id) == "" && (!g.hasStart || !g.startsAt.After(now)) && (!g.hasE || g.endsAt.After(now)) {
			usable = append(usable, g.id)
		}
	}
	if slices.Contains(usable, st.next) {
		return st.next
	}
	slices.Sort(usable)
	if len(usable) > 0 {
		return usable[0]
	}
	return ""
}

// claudeClaim is a claim that went out without a final answer: it may be
// retried, with the same request ID, within claudeRetryWindow. Claims live
// in memory, per client (a new connection forgets them), never on disk.
type claudeClaim struct {
	grant, org, requestID string
	at                    time.Time
}

type claimKey struct {
	c         Caller
	authIndex string
}

var (
	claudeClaimsMu sync.Mutex
	claudeClaims   = map[claimKey]*claudeClaim{}
	claudeBusy     = map[claimKey]bool{}
)

func claudeResetAction(c Caller, cr cpa.Credential, st *claudeGrantStatus, readFailed bool, now time.Time) Action {
	a := Action{
		ID:      "claude-reset",
		Label:   Text{"重置额度", "Reset limits"},
		Confirm: Text{"确定消耗一次重置，清除这个账号符合条件的用量限制？重置次数有限。", "Spend one reset to clear this account's eligible usage limits? Resets are limited."},
	}
	claudeClaimsMu.Lock()
	pending := claudeClaims[claimKey{c, cr.AuthIndex}]
	claudeClaimsMu.Unlock()
	switch {
	case pending != nil && now.Sub(pending.at) >= claudeRetryWindow:
		a.Disabled, a.Reason = true, Text{"重试窗口已过期，已阻止再次重置。请先检查 Claude 账号状态。", "The retry window has expired; further resets are blocked. Check the Claude account first."}
	case pending != nil:
		a.Label = Text{"重试同一请求", "Retry the same claim"}
		a.Confirm = Text{"使用原请求 ID 重试结果未知的重置？可能已消耗一次重置。请在 10 分钟内重试。", "Retry the unknown reset with its original request ID? A reset may already have been spent. Retry within 10 minutes."}
	case cr.Disabled:
		a.Disabled, a.Reason = true, Text{"已停用的账号无法重置，请先启用。", "A disabled account cannot be reset; enable it first."}
	case readFailed || st == nil:
		a.Disabled, a.Reason = true, Text{"无法读取重置权益。", "Reset grants could not be read."}
	case len(st.grants) == 0:
		a.Disabled, a.Reason = true, Text{"没有储备重置权益。", "No banked reset grants."}
	case st.pick(now) == "":
		a.Disabled, a.Reason = true, Text{"当前无法使用重置（未达到限制、冷却中或不符合资格）。", "No reset can be used now (not limited, cooling down, or not eligible)."}
	}
	return a
}

func actClaude(ctx context.Context, c Caller, cr cpa.Credential, action string) (Text, error) {
	if action != "claude-reset" {
		return Text{}, fmt.Errorf("unknown action %q", action)
	}
	key := claimKey{c, cr.AuthIndex}
	claudeClaimsMu.Lock()
	if claudeBusy[key] {
		claudeClaimsMu.Unlock()
		return Text{}, msg("正在重置，请稍候。", "A reset is already running.")
	}
	claudeBusy[key] = true
	claim := claudeClaims[key]
	claudeClaimsMu.Unlock()
	defer func() {
		claudeClaimsMu.Lock()
		delete(claudeBusy, key)
		claudeClaimsMu.Unlock()
	}()
	blocked := msg("未提交重置：无法验证资格或账号身份。请刷新后检查。", "Reset not submitted: eligibility or account identity could not be verified. Refresh and check again.")

	retry := claim != nil
	if retry && time.Since(claim.at) >= claudeRetryWindow {
		return Text{}, msg("重试窗口已过期，已阻止再次重置。请先检查 Claude 账号状态。", "The retry window has expired; further resets are blocked. Check the Claude account first.")
	}
	profile, err := call(ctx, c, cr, http.MethodGet, claudeProfileURL, claudeHeaders(), "")
	if err != nil {
		return Text{}, blocked
	}
	org := strings.ToLower(str(get(profile, "organization", "uuid")))
	if !claudeOrgUUID.MatchString(org) || (retry && claim.org != org) {
		return Text{}, blocked
	}
	if !retry {
		grants, err := call(ctx, c, cr, http.MethodGet, claudeGrantsURL, claudeHeaders(), "")
		if err != nil {
			return Text{}, blocked
		}
		st := parseClaudeGrants(get(grants, "cedar_ember"))
		if st == nil {
			return Text{}, blocked
		}
		grant := st.pick(time.Now())
		if grant == "" {
			return Text{}, blocked
		}
		claim = &claudeClaim{grant: grant, org: org, requestID: newRequestID(), at: time.Now()}
		claudeClaimsMu.Lock()
		claudeClaims[key] = claim
		claudeClaimsMu.Unlock()
	}

	code := claudeSendClaim(ctx, c, cr, claim)
	// A refusal on a retry cannot prove the earlier claim spent nothing.
	if code != "" && (!retry || code == "reset" || code == "already_used") {
		claudeClaimsMu.Lock()
		delete(claudeClaims, key)
		claudeClaimsMu.Unlock()
	} else {
		return Text{}, &Msg{Text: Text{"结果待确认：可能已消耗一次重置。请在 10 分钟内用同一请求重试，不要在其他地方发起新的重置。", "The outcome is unknown: a reset may have been spent. Retry the same claim within 10 minutes; do not start another one elsewhere."}, Unknown: true}
	}
	text := claudeOutcomeTx[code]
	if code == "reset" || code == "already_used" {
		return text, nil
	}
	return Text{}, &Msg{Text: text}
}

// claudeSendClaim posts the claim once; "" means no final answer came back
// and the reset may or may not have happened.
func claudeSendClaim(ctx context.Context, c Caller, cr cpa.Credential, cl *claudeClaim) string {
	body, _ := json.Marshal(map[string]string{"program": claudeProgram, "grant_id": cl.grant, "request_id": cl.requestID})
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodPost,
		URL: claudeOrigin + "/api/organizations/" + cl.org + "/reset_rate_limits", Header: claudeHeaders(), Data: string(body)})
	if err != nil {
		return ""
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth_error"
	}
	result := str(get(parseBody(resp.Body), "result"))
	if resp.OK() && slices.Contains(claudeResults, result) {
		return result
	}
	return ""
}

// newRequestID makes a claim's idempotency key.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
