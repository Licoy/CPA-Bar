package quota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Codex, as the management center reads it: the usage windows of
// /wham/usage, the live subscription renewal, the manual rate-limit resets
// ("reset credits") the account may spend, and the flow that spends one.

const (
	codexUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexSubscriptionURL = "https://chatgpt.com/backend-api/subscriptions"
	codexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexConsumeURL      = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	codexUserAgent       = "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)"
	// codexOptionalTimeout bounds the requests whose failure only drops a
	// detail, as in the management center.
	codexOptionalTimeout = 8 * time.Second

	codexFiveHourSeconds = 18000
	codexWeekSeconds     = 604800
	codexMinMonthSeconds = 28 * 24 * 60 * 60
	codexMaxMonthSeconds = 31 * 24 * 60 * 60
)

func codexHeaders(accountID string) map[string]string {
	h := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    codexUserAgent,
	}
	if accountID != "" {
		h["Chatgpt-Account-Id"] = accountID
	}
	return h
}

func init() {
	register("codex", Provider{Fetch: fetchCodex, Act: actCodex})
}

// ---- Reading a credential's own fields ----

// codexFile is the credential as the server listed it: the fields the
// management center looks the plan, account and renewal up in.
type codexFile struct {
	top, metadata, attributes map[string]any
}

func codexFileOf(cr cpa.Credential) codexFile {
	var top map[string]any
	if len(cr.Raw) > 0 {
		_ = json.Unmarshal(cr.Raw, &top)
	}
	return codexFile{top: top, metadata: codexRecord(top["metadata"]), attributes: codexRecord(top["attributes"])}
}

func (f codexFile) roots() []map[string]any {
	return []map[string]any{f.top, f.metadata, f.attributes}
}

func codexRecord(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// codexNumber reads a finite number, or text holding exactly one.
func codexNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// codexString reads non-blank text; a finite number reads as its digits.
func codexString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
	}
	return ""
}

// codexTruthy is JavaScript's Boolean() for the values a payload holds.
func codexTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case nil:
		return false
	}
	return true
}

// codexIDToken reads an id_token the way the management center does: an
// object as it is, JSON text, or a JWT's payload.
func codexIDToken(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil && m != nil {
			return m
		}
		parts := strings.Split(s, ".")
		if len(parts) < 2 {
			return nil
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			return nil
		}
		if json.Unmarshal(raw, &m) == nil {
			return m
		}
	}
	return nil
}

// codexAuthInfo is an id_token's OpenAI claim when it has one.
func codexAuthInfo(v any) map[string]any {
	p := codexIDToken(v)
	if p == nil {
		return nil
	}
	if nested := codexRecord(p["https://api.openai.com/auth"]); nested != nil {
		return nested
	}
	return p
}

func (f codexFile) accountID() string {
	for _, root := range f.roots() {
		if id := codexString(get(root, "chatgpt_account_id|chatgptAccountId")); id != "" {
			return id
		}
	}
	for _, root := range f.roots() {
		p := codexIDToken(root["id_token"])
		if id := codexString(get(p, "chatgpt_account_id|chatgptAccountId")); id != "" {
			return id
		}
		if id := codexString(get(codexAuthInfo(root["id_token"]), "chatgpt_account_id|chatgptAccountId")); id != "" {
			return id
		}
	}
	return ""
}

// planType is the plan the credential file records, lower case.
func (f codexFile) planType() string {
	for _, root := range f.roots() {
		if p := strings.ToLower(codexString(get(root, "plan_type|planType"))); p != "" {
			return p
		}
		if p := strings.ToLower(codexString(get(codexRecord(root["id_token"]), "plan_type|planType"))); p != "" {
			return p
		}
	}
	return ""
}

// activeUntil is the subscription end the credential file records.
func (f codexFile) activeUntil() any {
	keys := "chatgpt_subscription_active_until|chatgptSubscriptionActiveUntil"
	for _, root := range f.roots() {
		for _, c := range []any{
			get(root, "chatgpt_subscription_active_until|chatgptSubscriptionActiveUntil|subscription_active_until|subscriptionActiveUntil"),
			get(codexRecord(root["subscription"]), "active_until|activeUntil"),
			get(codexAuthInfo(root["id_token"]), keys),
		} {
			if v := codexDateLike(c); v != nil {
				return v
			}
		}
	}
	return nil
}

// codexDateLike keeps a non-zero number or non-blank text.
func codexDateLike(v any) any {
	if n, ok := codexNumber(v); ok {
		if n == 0 {
			return nil
		}
		return n
	}
	if s := codexString(v); s != "" && s != "0" {
		return s
	}
	return nil
}

// ---- Fetch ----

func fetchCodex(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, errors.New("auth file missing auth_index")
	}
	file := codexFileOf(cr)
	accountID := file.accountID()
	header := codexHeaders(accountID)

	var (
		wg      sync.WaitGroup
		liveEnd any
		credits codexResetCredits
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		liveEnd = codexSubscription(ctx, c, cr, accountID, header)
	}()
	go func() {
		defer wg.Done()
		credits = codexFetchResetCredits(ctx, c, cr, header)
	}()
	usage, err := call(ctx, c, cr, http.MethodGet, codexUsageURL, header, "")
	wg.Wait()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	r := &Result{}
	plan := strings.ToLower(codexString(get(usage, "plan_type|planType")))
	if plan == "" {
		plan = file.planType()
	}
	r.Plan = codexPlanLabel(plan)

	activeUntil := liveEnd
	if activeUntil == nil {
		activeUntil = file.activeUntil()
	}
	if activeUntil != nil {
		value := fmt.Sprint(activeUntil)
		if t := instant(activeUntil); !t.IsZero() {
			value = t.Local().Format("2006-01-02")
		} else if s, ok := activeUntil.(string); ok {
			value = s
		}
		r.Facts = append(r.Facts, Fact{Text{"续期时间", "Renewal time"}, value})
	}

	balance, unlimited := codexAccountCredits(get(usage, "credits"))
	switch {
	case unlimited:
		r.Facts = append(r.Facts, Fact{Text{"Credit 余额", "Credit balance"}, "∞"})
	case balance != "":
		r.Facts = append(r.Facts, Fact{Text{"Credit 余额", "Credit balance"}, balance})
	}

	fromUsage := codexNormalizeResetCredits(get(usage, "rate_limit_reset_credits|rateLimitResetCredits"))
	count, hasCount := credits.summary.available, credits.summary.hasAvailable
	if !hasCount && len(credits.summary.credits) > 0 {
		count, hasCount = float64(len(credits.summary.credits)), true
	}
	if !hasCount {
		count, hasCount = fromUsage.available, fromUsage.hasAvailable
	}
	if hasCount {
		r.Facts = append(r.Facts, Fact{Text{"主动重置次数", "Manual resets"}, strconv.FormatFloat(count, 'f', -1, 64)})
	}
	for i, cred := range credits.summary.credits {
		value := cred.expiresAt
		if t := instant(cred.expiresAt); !t.IsZero() {
			value = t.Local().Format("01/02 15:04")
		}
		r.Facts = append(r.Facts, Fact{
			Text{fmt.Sprintf("主动重置过期时间 · 第 %d 次", i+1), fmt.Sprintf("Manual reset expiry · Reset %d", i+1)},
			value,
		})
	}
	if len(credits.summary.credits) == 0 && credits.err != "" {
		r.Notes = append(r.Notes, Text{"主动重置过期时间获取失败：" + credits.err, "Failed to load manual reset expiry: " + credits.err})
	}

	windows := codexUsageWindows(usage, now)
	if len(windows) == 0 {
		r.Notes = append(r.Notes, Text{"暂无额度数据", "No quota data available"})
	} else {
		r.Groups = []Group{{Windows: windows}}
	}
	r.Actions = []Action{codexResetAction(cr, count, hasCount)}
	return r, nil
}

// codexPlanLabel names a plan the way the management center does; unknown
// plans keep the server's word.
func codexPlanLabel(plan string) string {
	switch plan {
	case "":
		return ""
	case "self_serve_business_prolite":
		return "Business Premium"
	case "pro":
		return "Pro 200"
	case "prolite", "pro-lite", "pro_lite":
		return "Pro 100"
	case "plus":
		return "Plus"
	case "team":
		return "Team"
	case "free":
		return "Free"
	}
	return strings.ToUpper(plan[:1]) + plan[1:]
}

var codexBalancePattern = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// codexAccountCredits reads the credit balance (digits only, else none) and
// whether the credits are unlimited.
func codexAccountCredits(v any) (balance string, unlimited bool) {
	m := codexRecord(v)
	if m == nil {
		return "", false
	}
	switch x := m["balance"].(type) {
	case float64:
		balance = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		balance = strings.TrimSpace(x)
	}
	if !codexBalancePattern.MatchString(balance) {
		balance = ""
	}
	return balance, m["unlimited"] == true
}

// ---- Usage windows ----

func codexMonthly(w map[string]any) bool {
	s, ok := codexNumber(get(w, "limit_window_seconds|limitWindowSeconds"))
	return ok && s >= codexMinMonthSeconds && s <= codexMaxMonthSeconds
}

// codexClassify picks the five-hour and the weekly (or monthly) window of a
// rate limit: by their length, then, for payloads without one, by order.
func codexClassify(limit map[string]any) (five, weekly map[string]any) {
	wins := [2]map[string]any{codexRecord(get(limit, "primary_window|primaryWindow")), codexRecord(get(limit, "secondary_window|secondaryWindow"))}
	fi, wi := -1, -1
	for i, w := range wins {
		if w == nil {
			continue
		}
		s, ok := codexNumber(get(w, "limit_window_seconds|limitWindowSeconds"))
		if ok && s == codexFiveHourSeconds && fi < 0 {
			fi = i
		} else if (ok && s == codexWeekSeconds || codexMonthly(w)) && wi < 0 {
			wi = i
		}
	}
	if fi < 0 && wins[0] != nil && wi != 0 {
		fi = 0
	}
	if wi < 0 && wins[1] != nil && fi != 1 {
		wi = 1
	}
	if fi >= 0 {
		five = wins[fi]
	}
	if wi >= 0 {
		weekly = wins[wi]
	}
	return
}

func codexUsageWindows(usage map[string]any, now time.Time) []Window {
	var out []Window
	add := func(label Text, w map[string]any, limit map[string]any, kind WindowKind) {
		if w == nil {
			return
		}
		reached := codexTruthy(get(limit, "limit_reached|limitReached")) || get(limit, "allowed") == false
		used, known := codexNumber(get(w, "used_percent|usedPercent"))
		reset := codexResetAt(w, now)
		if !known && reached && codexHasReset(w) {
			used, known = 100, true
		}
		win := usedWindow(label, used, known, reset)
		win.Kind = kind
		out = append(out, win)
	}
	secondary := func(w map[string]any, weekly, monthly Text) Text {
		if codexMonthly(w) {
			return monthly
		}
		return weekly
	}

	limit := codexRecord(get(usage, "rate_limit|rateLimit"))
	five, weekly := codexClassify(limit)
	add(Text{"5 小时限额", "5-hour limit"}, five, limit, KindShort)
	add(secondary(weekly, Text{"周限额", "Weekly limit"}, Text{"月度限额", "Monthly limit"}), weekly, limit, KindLong)

	review := codexRecord(get(usage, "code_review_rate_limit|codeReviewRateLimit"))
	five, weekly = codexClassify(review)
	add(Text{"代码审查 5 小时限额", "Code review 5-hour limit"}, five, review, KindOther)
	add(secondary(weekly, Text{"代码审查周限额", "Code review weekly limit"}, Text{"代码审查月度限额", "Code review monthly limit"}), weekly, review, KindOther)

	extra, _ := get(usage, "additional_rate_limits|additionalRateLimits").([]any)
	for i, item := range extra {
		info := codexRecord(get(item, "rate_limit|rateLimit"))
		if info == nil {
			continue
		}
		name := codexString(get(item, "limit_name|limitName"))
		if name == "" {
			name = codexString(get(item, "metered_feature|meteredFeature"))
		}
		if name == "" {
			name = fmt.Sprintf("additional-%d", i+1)
		}
		five, weekly = codexClassify(info)
		add(Text{name + " 5 小时限额", name + " 5-hour limit"}, five, info, KindOther)
		add(secondary(weekly, Text{name + " 周限额", name + " weekly limit"}, Text{name + " 月度限额", name + " monthly limit"}), weekly, info, KindOther)
	}
	return out
}

// codexHasReset says whether a window states when it resets.
func codexHasReset(w map[string]any) bool {
	if n, ok := codexNumber(get(w, "reset_at|resetAt")); ok && n > 0 {
		return true
	}
	n, ok := codexNumber(get(w, "reset_after_seconds|resetAfterSeconds"))
	return ok && n > 0
}

func codexResetAt(w map[string]any, now time.Time) time.Time {
	for _, k := range []string{"reset_at", "resetAt"} {
		if t := instant(w[k]); !t.IsZero() {
			return t
		}
	}
	if n, ok := codexNumber(get(w, "reset_after_seconds|resetAfterSeconds")); ok && n > 0 {
		return now.Add(time.Duration(n * float64(time.Second)))
	}
	return time.Time{}
}

// ---- Subscription and reset credits ----

// codexSubscription reads the live subscription end; nil when the account is
// unknown or the request fails, which only drops the date.
func codexSubscription(ctx context.Context, c Caller, cr cpa.Credential, accountID string, header map[string]string) any {
	if accountID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, codexOptionalTimeout)
	defer cancel()
	// encodeURIComponent: a space is %20, not +.
	q := strings.ReplaceAll(url.QueryEscape(accountID), "+", "%20")
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodGet, URL: codexSubscriptionURL + "?account_id=" + q, Header: header})
	if err != nil || !resp.OK() {
		return nil
	}
	return codexDateLike(get(parseBody(resp.Body), "active_until|activeUntil"))
}

type codexResetCredit struct {
	id, expiresAt string
}

type codexResetSummary struct {
	available    float64
	hasAvailable bool
	credits      []codexResetCredit
	invalid      bool
}

type codexResetCredits struct {
	summary codexResetSummary
	err     string
}

// codexNormalizeResetCredits reads a reset-credits block: the counts and the
// available codex_rate_limits grants that say when they expire.
func codexNormalizeResetCredits(v any) codexResetSummary {
	if s, ok := v.(string); ok {
		v = parseBody(s)
	}
	m := codexRecord(v)
	if m == nil {
		return codexResetSummary{invalid: true}
	}
	var out codexResetSummary
	out.invalid = true
	for _, k := range []string{"credits", "available_count", "availableCount", "applicable_available_count", "applicableAvailableCount"} {
		if _, ok := m[k]; ok {
			out.invalid = false
		}
	}
	out.available, out.hasAvailable = codexNumber(get(m, "available_count|availableCount"))
	list, _ := m["credits"].([]any)
	for _, item := range list {
		if codexString(get(item, "reset_type|resetType")) != "codex_rate_limits" || codexString(get(item, "status")) != "available" {
			continue
		}
		expires := codexString(get(item, "expires_at|expiresAt"))
		if expires == "" {
			continue
		}
		out.credits = append(out.credits, codexResetCredit{id: codexString(get(item, "id")), expiresAt: expires})
	}
	return out
}

// codexFetchResetCredits reads the manual resets and when they expire; a
// failure is reported beside the empty answer, never as an error.
func codexFetchResetCredits(ctx context.Context, c Caller, cr cpa.Credential, header map[string]string) codexResetCredits {
	h := map[string]string{"Accept": "application/json", "OpenAI-Beta": "codex-1", "Originator": "Codex Desktop"}
	for k, v := range header {
		h[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, codexOptionalTimeout)
	defer cancel()
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodGet, URL: codexResetCreditsURL, Header: h})
	if err != nil {
		return codexResetCredits{err: err.Error()}
	}
	body := parseBody(resp.Body)
	if !resp.OK() {
		return codexResetCredits{err: (&UpstreamError{Status: resp.StatusCode, Message: errorMessage(body, resp.Body)}).Error()}
	}
	sum := codexNormalizeResetCredits(body)
	if sum.invalid {
		return codexResetCredits{err: "invalid manual reset expiry response"}
	}
	return codexResetCredits{summary: sum}
}

// ---- Spending a manual reset ----

func codexResetAction(cr cpa.Credential, count float64, hasCount bool) Action {
	name := cr.Name
	if name == "" {
		name = cr.DisplayName()
	}
	a := Action{
		ID:    "codex-reset",
		Label: Text{"重置额度", "Reset quota"},
		Confirm: Text{
			fmt.Sprintf("将消耗 1 次主动重置次数来重置 \"%s\" 的 Codex 额度。是否继续？", name),
			fmt.Sprintf("This will consume 1 manual reset to reset the Codex quota for \"%s\". Continue?", name),
		},
	}
	switch {
	case cr.RuntimeOnly:
		a.Disabled, a.Reason = true, Text{"仅运行时的凭据无法重置。", "Runtime-only credentials cannot be reset."}
	case cr.Disabled:
		a.Disabled, a.Reason = true, Text{"已停用的凭据无法重置，请先启用。", "A disabled credential cannot be reset; enable it first."}
	case !hasCount || count <= 0:
		a.Disabled, a.Reason = true, Text{"没有可用的主动重置次数。", "No manual resets available."}
	}
	return a
}

type codexKey struct {
	c         Caller
	authIndex string
}

var (
	codexBusyMu sync.Mutex
	codexBusy   = map[codexKey]bool{}
)

// codexCooldownClearer is what clears the gateway's cooldown for a
// credential after its quota was reset; *cpa.Client does.
type codexCooldownClearer interface {
	ResetCooldown(ctx context.Context, authIndex string) error
}

var codexNotConfirmed = Text{"未确认额度重置成功，请刷新额度后再决定是否重试。", "Quota reset was not confirmed. Refresh the quota before trying again."}

// actCodex spends one manual reset. Like the management center it sends the
// request once with a fresh redeem ID and never retries: when no final
// answer comes back the outcome is unknown, and the user is told to refresh
// the quota before deciding.
func actCodex(ctx context.Context, c Caller, cr cpa.Credential, action string) (Text, error) {
	if action != "codex-reset" {
		return Text{}, fmt.Errorf("unknown action %q", action)
	}
	if cr.AuthIndex == "" {
		return Text{}, msg("认证文件缺少 auth_index", "Auth file missing auth_index")
	}
	if cr.RuntimeOnly || cr.Disabled {
		return Text{}, msg("此凭据无法重置。", "This credential cannot be reset.")
	}
	key := codexKey{c, cr.AuthIndex}
	codexBusyMu.Lock()
	if codexBusy[key] {
		codexBusyMu.Unlock()
		return Text{}, msg("正在重置，请稍候。", "A reset is already running.")
	}
	codexBusy[key] = true
	codexBusyMu.Unlock()
	defer func() {
		codexBusyMu.Lock()
		delete(codexBusy, key)
		codexBusyMu.Unlock()
	}()

	body, _ := json.Marshal(map[string]string{"redeem_request_id": codexRequestID()})
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodPost, URL: codexConsumeURL,
		Header: codexHeaders(codexFileOf(cr).accountID()), Data: string(body)})
	if err != nil {
		return Text{}, &Msg{Text: codexNotConfirmed, Unknown: true}
	}
	answer := parseBody(resp.Body)
	if !resp.OK() {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return Text{}, &Msg{Text: codexNotConfirmed, Unknown: true}
		}
		detail := (&UpstreamError{Status: resp.StatusCode, Message: errorMessage(answer, resp.Body)}).Error()
		return Text{}, msg("重置失败："+detail, "Reset failed: "+detail)
	}
	switch code := str(get(answer, "code")); code {
	case "reset", "already_redeemed":
	case "no_credit", "nothing_to_reset":
		return Text{}, &Msg{Text: codexNotConfirmed}
	default:
		return Text{}, &Msg{Text: codexNotConfirmed, Unknown: true}
	}

	// The gateway keeps its own cooldown for the credential; clear it now
	// that the account's limits are back.
	if clearer, ok := c.(codexCooldownClearer); ok {
		if err := clearer.ResetCooldown(ctx, cr.AuthIndex); err != nil {
			return Text{}, msg(
				"额度已重置，但代理冷却清除失败。请在原连接的认证文件页面使用“清除冷却”，不要再次消耗重置卡。",
				"Quota was reset, but the gateway cooldown could not be cleared. On the original connection, use Clear cooldown on the credential page; do not redeem another reset.")
		}
	}
	name := cr.Name
	if name == "" {
		name = cr.DisplayName()
	}
	return Text{fmt.Sprintf("已重置 \"%s\" 的 Codex 额度", name), fmt.Sprintf("Reset Codex quota for \"%s\"", name)}, nil
}

// codexRequestID makes a redeem request ID: a random UUID.
func codexRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
