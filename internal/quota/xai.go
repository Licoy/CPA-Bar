package quota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// xAI (Grok), as the management center reads it: the weekly and monthly
// billing of the Grok CLI proxy and the subscription plan beside them.
//
// The management center also has a "paid health" fallback for paid OAuth
// credentials that sends a one-token chat completion to api.x.ai. That
// spends usage, so it is not ported: a paid credential whose billing cannot
// be read gets a plan and a note instead, from a read-only profile request.

const (
	xaiBillingWeeklyURL  = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	xaiBillingMonthlyURL = "https://cli-chat-proxy.grok.com/v1/billing"
	xaiUserURL           = "https://cli-chat-proxy.grok.com/v1/user?include=subscription"
	xaiSettingsURL       = "https://cli-chat-proxy.grok.com/v1/settings"
	xaiMeURL             = "https://api.x.ai/v1/me"
	// xaiClientVersion is the one the backend falls back to; Grok rejects
	// versions older than 1.0.13.
	xaiClientVersion = "1.0.46"
	xaiUserAgent     = "grok-pager/" + xaiClientVersion + " grok-shell/" + xaiClientVersion + " (macos; aarch64)"
	// xaiOptionalTimeout bounds the requests whose failure only drops a
	// detail.
	xaiOptionalTimeout = 8 * time.Second

	xaiSuperGrokLimitCents      = 15000
	xaiSuperGrokHeavyLimitCents = 150000
)

func xaiHeaders(userID string) map[string]string {
	h := map[string]string{
		"Authorization":         "Bearer $TOKEN$",
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": xaiClientVersion,
		"accept":                "*/*",
		"user-agent":            xaiUserAgent,
	}
	if userID != "" {
		h["x-userid"] = userID
	}
	return h
}

func init() {
	register("xai", Provider{Fetch: fetchXAI})
}

func fetchXAI(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, errors.New("auth file missing auth_index")
	}
	var raw map[string]any
	if len(cr.Raw) > 0 {
		_ = json.Unmarshal(cr.Raw, &raw)
	}
	header := xaiHeaders(xaiUserID(raw))

	var (
		wg                    sync.WaitGroup
		weekly, monthly       *xaiSummary
		weeklyErr, monthlyErr error
		tier, display         string
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		weekly, weeklyErr = xaiBilling(ctx, c, cr, xaiBillingWeeklyURL, header)
	}()
	go func() {
		defer wg.Done()
		monthly, monthlyErr = xaiBilling(ctx, c, cr, xaiBillingMonthlyURL, header)
	}()
	go func() {
		defer wg.Done()
		tier, display = xaiSubscription(ctx, c, cr, header)
	}()
	wg.Wait()

	plan := xaiPlan(tier, display)
	b := xaiMerge(weekly, monthly)
	if b == nil {
		err := errors.New("no quota data available")
		if weeklyErr != nil && monthlyErr != nil {
			err = weeklyErr
		}
		// A paid OAuth credential has no billing to read. Confirm it works
		// with a read-only request, and say what is not known.
		if xaiIsPaid(raw) {
			if _, meErr := call(ctx, c, cr, http.MethodGet, xaiMeURL, map[string]string{"Authorization": "Bearer $TOKEN$", "accept": "application/json"}, ""); meErr == nil {
				if plan == "" {
					plan = "Paid"
				}
				return &Result{Plan: plan, Notes: []Text{{
					"xAI 暂不为此 OAuth 凭证提供额度总量数据；未发送对话探测请求。",
					"xAI does not expose quota totals for this OAuth credential; no chat probe was sent.",
				}}}, nil
			}
		}
		return nil, err
	}
	return xaiResult(b, plan), nil
}

// ---- Credential fields ----

func xaiUserID(file map[string]any) string {
	if file == nil {
		return ""
	}
	metadata, attributes := codexRecord(file["metadata"]), codexRecord(file["attributes"])
	pick := func(roots ...map[string]any) map[string]any {
		for _, r := range roots {
			if m := codexRecord(r["oauth"]); m != nil {
				return m
			}
		}
		return nil
	}
	oauth := pick(file, metadata, attributes)
	var user map[string]any
	for _, r := range []map[string]any{file, metadata, attributes} {
		if m := codexRecord(r["user"]); m != nil {
			user = m
			break
		}
	}
	for _, root := range []map[string]any{file, metadata, attributes} {
		if id := codexString(get(root, "sub|subject|user_id|userId")); id != "" {
			return id
		}
	}
	for _, c := range []any{get(oauth, "sub|subject"), get(user, "sub|id")} {
		if id := codexString(c); id != "" {
			return id
		}
	}
	return ""
}

// xaiIsPaid recognizes a paid OAuth credential: the official API with the
// "paid" prefix, or a token whose tier claim is at least 1.
func xaiIsPaid(file map[string]any) bool {
	var records []map[string]any
	var visit func(v any, depth int)
	visit = func(v any, depth int) {
		m := codexRecord(v)
		if m == nil || depth > 2 {
			return
		}
		records = append(records, m)
		for _, k := range []string{"metadata", "attributes", "oauth", "raw", "credential", "auth"} {
			visit(m[k], depth+1)
		}
	}
	visit(file, 0)

	usesAPI, paidPrefix := false, false
	for _, r := range records {
		if v := get(r, "using_api|usingApi"); xaiTruthy(v) {
			usesAPI = true
		}
		if strings.ToLower(str(r["prefix"])) == "paid" {
			paidPrefix = true
		}
	}
	if usesAPI && paidPrefix {
		return true
	}
	for _, r := range records {
		for _, k := range []string{"access_token", "accessToken", "id_token", "idToken", "token"} {
			if tier, ok := xaiJWTTier(str(r[k])); ok && tier >= 1 {
				return true
			}
		}
	}
	return false
}

func xaiTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			return true
		}
	}
	return false
}

func xaiJWTTier(token string) (float64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return 0, false
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return 0, false
	}
	for k, v := range claims {
		k = strings.ToLower(k)
		if k == "tier" || strings.HasSuffix(k, "/tier") || strings.HasSuffix(k, ":tier") {
			return codexNumber(v)
		}
	}
	return 0, false
}

// ---- Billing ----

// xaiSummary is what the billing config says, in cents and percents.
type xaiSummary struct {
	periodType         string // weekly, monthly or unknown
	usagePercent       *float64
	periodStart        string
	periodEnd          string
	productUsage       []xaiProduct
	monthlyLimit       *float64
	used               *float64
	includedUsed       *float64
	onDemandCap        *float64
	onDemandUsed       *float64
	onDemandUsedPct    *float64
	prepaid            *float64
	billingPeriodStart string
	billingPeriodEnd   string
	usedPercent        *float64
	resetAt            time.Time
}

type xaiProduct struct {
	name    string
	percent *float64
}

func xaiNum(v any) *float64 {
	if f, ok := codexNumber(v); ok {
		return &f
	}
	return nil
}

// xaiCents reads an amount that is a number, numeric text or {"val": …}.
func xaiCents(v any) *float64 {
	if m := codexRecord(v); m != nil {
		return xaiNum(m["val"])
	}
	return xaiNum(v)
}

func xaiBilling(ctx context.Context, c Caller, cr cpa.Credential, url string, header map[string]string) (*xaiSummary, error) {
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodGet, URL: url, Header: header})
	if err != nil {
		return nil, err
	}
	body := parseBody(resp.Body)
	if !resp.OK() {
		return nil, &UpstreamError{Status: resp.StatusCode, Message: errorMessage(body, resp.Body)}
	}
	return xaiBuildSummary(codexRecord(get(body, "config"))), nil
}

func xaiBuildSummary(config map[string]any) *xaiSummary {
	if config == nil {
		return nil
	}
	period := codexRecord(get(config, "currentPeriod|current_period"))
	periodType := "unknown"
	switch t := strings.ToLower(codexString(period["type"])); {
	case strings.Contains(t, "weekly"):
		periodType = "weekly"
	case strings.Contains(t, "monthly"):
		periodType = "monthly"
	}
	credit := xaiNum(get(config, "creditUsagePercent|credit_usage_percent"))
	billingStart := codexString(get(config, "billingPeriodStart|billing_period_start"))
	billingEnd := codexString(get(config, "billingPeriodEnd|billing_period_end"))
	periodStart := codexString(period["start"])
	if periodStart == "" {
		periodStart = billingStart
	}
	periodEnd := codexString(period["end"])
	if periodEnd == "" {
		periodEnd = billingEnd
	}

	var products []xaiProduct
	list, _ := get(config, "productUsage|product_usage").([]any)
	for i, item := range list {
		m := codexRecord(item)
		if m == nil {
			continue
		}
		name := codexString(m["product"])
		if name == "" {
			name = fmt.Sprintf("Product %d", i+1)
		}
		products = append(products, xaiProduct{name, xaiNum(get(m, "usagePercent|usage_percent"))})
	}

	limit := xaiCents(get(config, "monthlyLimit|monthly_limit"))
	used := xaiCents(config["used"])
	onDemandCap := xaiCents(get(config, "onDemandCap|on_demand_cap"))
	explicitOnDemand := xaiCents(get(config, "onDemandUsed|on_demand_used"))

	var included *float64
	if used != nil {
		v := *used
		if limit != nil && *limit > 0 {
			v = math.Min(*used, *limit)
		}
		included = &v
	}
	onDemandUsed := explicitOnDemand
	if onDemandUsed == nil && used != nil && limit != nil {
		v := math.Max(0, *used-*limit)
		onDemandUsed = &v
	}
	var usedPercent, onDemandPct *float64
	if limit != nil && *limit > 0 && included != nil {
		v := *included / *limit * 100
		usedPercent = &v
	}
	if onDemandCap != nil && *onDemandCap > 0 && onDemandUsed != nil {
		v := *onDemandUsed / *onDemandCap * 100
		onDemandPct = &v
	}

	hasWeekly := credit != nil || periodType == "weekly" || len(products) > 0
	hasMonthly := limit != nil || used != nil || (!hasWeekly && (onDemandCap != nil || billingEnd != ""))
	if !hasWeekly && !hasMonthly {
		return nil
	}

	s := &xaiSummary{
		productUsage: products, monthlyLimit: limit, used: used, includedUsed: included,
		onDemandCap: onDemandCap, onDemandUsed: onDemandUsed, onDemandUsedPct: onDemandPct,
		prepaid: xaiCents(get(config, "prepaidBalance|prepaid_balance")), usedPercent: usedPercent,
	}
	if hasWeekly {
		s.periodType = periodType
		if periodType == "unknown" {
			s.periodType = "weekly"
		}
		s.usagePercent, s.periodStart, s.periodEnd = credit, periodStart, periodEnd
	} else {
		s.periodType = "monthly"
		s.usagePercent, s.periodStart, s.periodEnd = usedPercent, billingStart, billingEnd
	}
	if hasMonthly {
		s.billingPeriodStart, s.billingPeriodEnd = billingStart, billingEnd
	}
	s.resetAt = instant(s.periodEnd)
	return s
}

// xaiMerge combines the weekly and monthly answers. The period stays whole:
// its dates and usage come from one answer, so a billing rollover is never
// shown as a quota reset.
func xaiMerge(primary, fallback *xaiSummary) *xaiSummary {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	period := primary
	if primary.periodType == "unknown" && fallback.periodType != "unknown" {
		period = fallback
	}
	first := func(a, b *float64) *float64 {
		if a != nil {
			return a
		}
		return b
	}
	text := func(a, b string) string {
		if a != "" {
			return a
		}
		return b
	}
	out := &xaiSummary{
		periodType: period.periodType, usagePercent: period.usagePercent,
		periodStart: period.periodStart, periodEnd: period.periodEnd,
		productUsage:       primary.productUsage,
		monthlyLimit:       first(primary.monthlyLimit, fallback.monthlyLimit),
		used:               first(primary.used, fallback.used),
		includedUsed:       first(primary.includedUsed, fallback.includedUsed),
		onDemandCap:        first(primary.onDemandCap, fallback.onDemandCap),
		onDemandUsed:       first(primary.onDemandUsed, fallback.onDemandUsed),
		onDemandUsedPct:    first(primary.onDemandUsedPct, fallback.onDemandUsedPct),
		prepaid:            first(primary.prepaid, fallback.prepaid),
		billingPeriodStart: text(primary.billingPeriodStart, fallback.billingPeriodStart),
		billingPeriodEnd:   text(primary.billingPeriodEnd, fallback.billingPeriodEnd),
		usedPercent:        first(primary.usedPercent, fallback.usedPercent),
	}
	if len(out.productUsage) == 0 {
		out.productUsage = fallback.productUsage
	}
	out.resetAt = instant(out.periodEnd)
	return out
}

// ---- Subscription plan ----

// xaiSubscription reads the plan's tier and display name; blank when either
// request fails, which only drops the plan.
func xaiSubscription(ctx context.Context, c Caller, cr cpa.Credential, header map[string]string) (tier, display string) {
	ctx, cancel := context.WithTimeout(ctx, xaiOptionalTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var user, settings map[string]any
	read := func(url string, out *map[string]any) {
		defer wg.Done()
		resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodGet, URL: url, Header: header})
		if err != nil || !resp.OK() {
			return
		}
		*out = codexRecord(parseBody(resp.Body))
	}
	wg.Add(2)
	go read(xaiUserURL, &user)
	go read(xaiSettingsURL, &settings)
	wg.Wait()
	return xaiField(user, "subscriptionTier", "subscription_tier"), xaiField(settings, "subscription_tier_display", "subscriptionTierDisplay")
}

func xaiField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := codexString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// xaiPlan is the plan's display name, else its tier.
func xaiPlan(tier, display string) string {
	if display != "" {
		return display
	}
	return tier
}

// ---- Display ----

func xaiUSD(cents float64) string {
	neg := cents < 0
	s := fmt.Sprintf("%.2f", math.Abs(cents)/100)
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, d := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	out := "$" + b.String() + "." + frac
	if neg {
		out = "-" + out
	}
	return out
}

// xaiAmount is "remaining / total" in dollars; "--" for what is not known.
func xaiAmount(total, used *float64) string {
	remaining := "--"
	if total != nil && used != nil {
		remaining = xaiUSD(math.Max(0, *total-*used))
	}
	if total == nil {
		return remaining
	}
	return remaining + " / " + xaiUSD(*total)
}

func xaiClampPercent(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	return max(0, min(100, *p)), true
}

func xaiResult(b *xaiSummary, plan string) *Result {
	r := &Result{Plan: plan}
	if r.Plan == "" && b.monthlyLimit != nil {
		switch *b.monthlyLimit {
		case xaiSuperGrokLimitCents:
			r.Plan = "SuperGrok"
		case xaiSuperGrokHeavyLimitCents:
			r.Plan = "SuperGrok Heavy"
		}
	}

	weeklyUsed, weeklyKnown := 0.0, false
	if b.periodType == "weekly" {
		weeklyUsed, weeklyKnown = xaiClampPercent(b.usagePercent)
	}
	hasWeekly := b.periodType == "weekly" && (weeklyKnown || b.periodEnd != "" || len(b.productUsage) > 0)
	hasMonthly := (b.monthlyLimit != nil || b.used != nil || b.billingPeriodEnd != "") &&
		!(hasWeekly && b.monthlyLimit != nil && *b.monthlyLimit == 0 && b.used != nil && *b.used == 0)

	headerReset := instant(b.billingPeriodEnd)
	if hasWeekly {
		headerReset = b.resetAt
	}
	if !headerReset.IsZero() {
		r.Facts = append(r.Facts, Fact{Text{"重置", "Resets"}, headerReset.Local().Format("01/02 15:04")})
	}
	if b.prepaid != nil && *b.prepaid > 0 {
		r.Facts = append(r.Facts, Fact{Text{"预付", "Prepaid"}, xaiUSD(*b.prepaid)})
	}

	var windows []Window
	if hasWeekly {
		w := usedWindow(Text{"周限额", "Weekly limit"}, weeklyUsed, weeklyKnown, b.resetAt)
		w.Kind = KindLong
		windows = append(windows, w)
		if !weeklyKnown {
			r.Notes = append(r.Notes, Text{"xAI 用量暂不可用", "Usage unavailable from xAI"})
		}
	}
	if b.onDemandCap != nil && *b.onDemandCap > 0 {
		used, known := xaiClampPercent(b.onDemandUsedPct)
		w := usedWindow(Text{"按量付费", "Pay as you go"}, used, known, time.Time{})
		w.Detail = xaiAmount(b.onDemandCap, b.onDemandUsed)
		windows = append(windows, w)
	} else {
		r.Facts = append(r.Facts, Fact{Text{"按量付费（未启用）", "Pay as you go (disabled)"}, "—"})
	}
	if hasMonthly {
		used, known := xaiClampPercent(b.usedPercent)
		w := usedWindow(Text{"月度积分", "Monthly credits"}, used, known, instant(b.billingPeriodEnd))
		w.Detail = xaiAmount(b.monthlyLimit, b.includedUsed)
		windows = append(windows, w)
	}
	if len(windows) > 0 {
		r.Groups = append(r.Groups, Group{Windows: windows})
	}

	if len(b.productUsage) > 0 {
		g := Group{Title: Text{"用量明细（共享配额）", "Usage breakdown (shared quota)"}}
		for _, p := range b.productUsage {
			used, known := xaiClampPercent(p.percent)
			g.Windows = append(g.Windows, usedWindow(Text{p.name + " 使用", p.name + " usage"}, used, known, time.Time{}))
		}
		r.Groups = append(r.Groups, g)
	}
	return r
}
