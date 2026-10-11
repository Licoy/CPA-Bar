package quota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Antigravity, as the management center reads it: the quota summary
// (retrieveUserQuotaSummary, tried on each Google endpoint in turn) grouped
// by model family, and the plan from loadCodeAssist.

var antigravityQuotaURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
}

const (
	antigravityCodeAssistURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	antigravityCLIVersion    = "1.0.13"
	antigravityClientName    = "aidev_client"
	antigravityOSType        = "darwin"
	antigravityArch          = "arm64"
)

func antigravityUserAgent() string {
	return "antigravity/cli/" + antigravityCLIVersion + " (" + antigravityClientName +
		"; os_type=" + antigravityOSType + "; arch=" + antigravityArch + ")"
}

func antigravityHeaders() map[string]string {
	return map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    antigravityUserAgent(),
	}
}

func init() {
	register("antigravity", Provider{Fetch: fetchAntigravity})
}

var (
	antigravityNoQuota = Text{"暂无额度数据", "No quota data available"}

	antigravityGroupLabels = map[string]Text{
		"gemini models":         {"Gemini 模型", "Gemini models"},
		"claude and gpt models": {"Claude 和 GPT 模型", "Claude and GPT models"},
	}
	antigravityBucketLabels = map[string]Text{
		"weekly limit":    {"周限额", "Weekly limit"},
		"daily limit":     {"日限额", "Daily limit"},
		"5 hour limit":    {"5 小时限额", "5-hour limit"},
		"5-hour limit":    {"5 小时限额", "5-hour limit"},
		"five hour limit": {"5 小时限额", "5-hour limit"},
		"monthly limit":   {"月限额", "Monthly limit"},
	}
	antigravityModelsNote = regexp.MustCompile(`(?i)^models within this group:\s*(.+)$`)
	antigravitySpaces     = regexp.MustCompile(`\s+`)

	antigravityPlans = map[string]string{
		"free-tier":          "Free",
		"g1-pro-tier":        "Pro",
		"g1-ultra-tier":      "Ultra",
		"g1-ultra-lite-tier": "Ultra Lite",
	}
	antigravityWindowOrder = map[string]int{"5h": 0, "five-hour": 0, "five_hour": 0, "weekly": 1, "week": 1}
)

// antigravityDownloader is the part of the CPA client that reads a
// credential file; the real client has it, the Caller interface does not.
type antigravityDownloader interface {
	DownloadCredential(ctx context.Context, name string) ([]byte, error)
}

func fetchAntigravity(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, msg("认证文件缺少 auth_index", "Auth file missing auth_index")
	}
	project := antigravityProjectID(ctx, c, cr)
	if project == "" {
		return nil, msg("Antigravity 凭证缺少 project_id。请重新登录或刷新凭证以发现项目。",
			"Antigravity credential missing project_id. Re-login or refresh the credential to discover the project.")
	}
	body, _ := json.Marshal(map[string]string{"project": project})

	// The plan is optional: it is read alongside the quota, and a failure
	// only leaves it out.
	plan := make(chan string, 1)
	go func() { plan <- antigravityPlan(ctx, c, cr) }()

	var (
		lastErr   error
		lastStat  int
		priStat   int
		hadOK     bool
		emptyNote = []Text{antigravityNoQuota}
	)
	fail := func(err error, status int) {
		lastErr = err
		if status == 0 {
			return
		}
		lastStat = status
		if (status == http.StatusForbidden || status == http.StatusNotFound) && priStat == 0 {
			priStat = status
		}
	}
	for _, url := range antigravityQuotaURLs {
		resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodPost,
			URL: url, Header: antigravityHeaders(), Data: string(body)})
		if err != nil {
			var ae *cpa.APIError
			status := 0
			if errors.As(err, &ae) {
				status = ae.Status
			}
			fail(err, status)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if !resp.OK() {
			fail(&UpstreamError{Status: resp.StatusCode, Message: errorMessage(parseBody(resp.Body), resp.Body)}, resp.StatusCode)
			continue
		}
		hadOK = true
		payload := antigravityPayload(resp.Body)
		if _, ok := get(payload, "groups").([]any); !ok {
			lastErr = msg(antigravityNoQuota.ZH, antigravityNoQuota.EN)
			continue
		}
		groups := antigravityGroups(payload)
		if len(groups) == 0 {
			lastErr = msg(antigravityNoQuota.ZH, antigravityNoQuota.EN)
			continue
		}
		return &Result{Plan: <-plan, Groups: groups}, nil
	}

	if hadOK {
		return &Result{Plan: <-plan, Notes: emptyNote}, nil
	}
	status := priStat
	if status == 0 {
		status = lastStat
	}
	var ue *UpstreamError
	if status != 0 && errors.As(lastErr, &ue) {
		return nil, &UpstreamError{Status: status, Message: ue.Message}
	}
	if lastErr == nil {
		lastErr = errors.New("unknown error")
	}
	return nil, lastErr
}

// antigravityProjectID finds the credential's Google project: the listed
// fields, then the credential file, as the management center does.
func antigravityProjectID(ctx context.Context, c Caller, cr cpa.Credential) string {
	if p := strings.TrimSpace(cr.ProjectID); p != "" {
		return p
	}
	var raw map[string]any
	if len(cr.Raw) > 0 {
		_ = json.Unmarshal(cr.Raw, &raw)
	}
	if p := antigravityString(get(raw, "project_id|projectId")); p != "" {
		return p
	}
	if p := antigravityString(get(raw, "metadata", "project_id|projectId")); p != "" {
		return p
	}
	if p := antigravityString(get(raw, "attributes", "project_id|projectId|gemini_virtual_project")); p != "" {
		return p
	}
	d, ok := c.(antigravityDownloader)
	if !ok || strings.TrimSpace(cr.Name) == "" {
		return ""
	}
	b, err := d.DownloadCredential(ctx, cr.Name)
	if err != nil {
		return ""
	}
	var file map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(string(b))), &file) != nil {
		return ""
	}
	for _, path := range [][]string{{"project_id|projectId"}, {"installed", "project_id|projectId"}, {"web", "project_id|projectId"}} {
		if p := antigravityString(get(file, path...)); p != "" {
			return p
		}
	}
	return ""
}

// antigravityString reads text the way the management center's
// normalizeStringValue does: non-blank text, or a finite number.
func antigravityString(v any) string {
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

// antigravityNumber reads a finite number from a number or numeric text.
func antigravityNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// antigravityFraction reads a remaining fraction: a number, or text like
// "65%".
func antigravityFraction(v any) (float64, bool) {
	if f, ok := antigravityNumber(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok {
		if t := strings.TrimSpace(s); strings.HasSuffix(t, "%") {
			if f, ok := antigravityNumber(strings.TrimSuffix(t, "%")); ok {
				return f / 100, true
			}
		}
	}
	return 0, false
}

// antigravityPayload reads an answer's JSON object; one without "models"
// that wraps another object in "body" is unwrapped.
func antigravityPayload(body string) map[string]any {
	toMap := func(v any) map[string]any {
		if s, ok := v.(string); ok {
			v = parseBody(s)
		}
		m, _ := v.(map[string]any)
		return m
	}
	// parseBody turns text that is not JSON into a string; only objects
	// count here.
	parsed := toMap(parseBody(body))
	if parsed == nil {
		return nil
	}
	if _, ok := parsed["models"]; ok {
		return parsed
	}
	if nested := toMap(parsed["body"]); nested != nil {
		return nested
	}
	return parsed
}

// antigravityPlan reads the subscription tier from loadCodeAssist; "" when
// it cannot be read.
func antigravityPlan(ctx context.Context, c Caller, cr cpa.Credential) string {
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodPost,
		URL: antigravityCodeAssistURL, Header: antigravityHeaders(), Data: `{"metadata":{"ideType":"ANTIGRAVITY"}}`})
	if err != nil || !resp.OK() {
		return ""
	}
	payload := antigravityPayload(resp.Body)
	tier := func(v any) (id, name string, ok bool) {
		if _, isMap := v.(map[string]any); !isMap {
			return "", "", false
		}
		return antigravityString(get(v, "id")), antigravityString(get(v, "name")), true
	}
	curID, curName, curOK := tier(get(payload, "currentTier|current_tier"))
	paidID, paidName, paidOK := tier(get(payload, "paidTier|paid_tier"))
	id, name := curID, curName
	if paidOK && paidID != "" {
		id, name = paidID, paidName
	} else if !curOK {
		id, name = "", ""
	}
	if id == "" && name == "" {
		return ""
	}
	if plan, ok := antigravityPlans[id]; ok {
		return plan
	}
	if name != "" {
		return name
	}
	return id
}

// antigravityGroups builds the groups of the quota summary: buckets that
// state a remaining fraction, five-hour before weekly, then by label.
func antigravityGroups(payload map[string]any) []Group {
	raw, _ := get(payload, "groups").([]any)
	var out []Group
	for gi, g := range raw {
		title := antigravityString(get(g, "displayName|display_name"))
		if title == "" {
			title = "Quota Group " + strconv.Itoa(gi+1)
		}
		type bucket struct {
			w     Window
			order int
			label string
		}
		var bs []bucket
		buckets, _ := get(g, "buckets").([]any)
		for bi, b := range buckets {
			frac, ok := antigravityFraction(get(b, "remainingFraction|remaining_fraction"))
			if !ok {
				continue
			}
			window := antigravityString(get(b, "window"))
			label := antigravityString(get(b, "displayName|display_name"))
			if label == "" {
				label = antigravityString(get(b, "description"))
			}
			if label == "" {
				label = window
			}
			if label == "" {
				label = antigravityString(get(b, "bucketId|bucket_id"))
			}
			if label == "" {
				label = "bucket-" + strconv.Itoa(bi+1)
			}
			order, known := antigravityWindowOrder[strings.ToLower(window)]
			if !known {
				order = math.MaxInt
			}
			bs = append(bs, bucket{
				w: Window{
					Label:     antigravityTranslate(label, antigravityBucketLabels),
					Remaining: clamp01(frac), Known: true,
					ResetAt: instant(get(b, "resetTime|reset_time")),
					Kind:    orderKind[order],
				},
				order: order, label: label,
			})
		}
		if len(bs) == 0 {
			continue
		}
		slices.SortStableFunc(bs, func(a, b bucket) int {
			if a.order != b.order {
				if a.order < b.order {
					return -1
				}
				return 1
			}
			if c := strings.Compare(strings.ToLower(a.label), strings.ToLower(b.label)); c != 0 {
				return c
			}
			return strings.Compare(a.label, b.label)
		})
		grp := Group{
			Title: antigravityTranslate(title, antigravityGroupLabels),
			Note:  antigravityNote(antigravityString(get(g, "description"))),
		}
		for _, b := range bs {
			grp.Windows = append(grp.Windows, b.w)
		}
		out = append(out, grp)
	}
	return out
}

func antigravityTranslate(label string, known map[string]Text) Text {
	key := strings.ToLower(strings.TrimSpace(antigravitySpaces.ReplaceAllString(label, " ")))
	if t, ok := known[key]; ok {
		return t
	}
	return Same(label)
}

func antigravityNote(desc string) Text {
	if desc == "" {
		return Text{}
	}
	if m := antigravityModelsNote.FindStringSubmatch(desc); m != nil {
		models := strings.TrimSpace(m[1])
		return Text{"此分组包含：" + models, "Models in this group: " + models}
	}
	return Same(desc)
}
