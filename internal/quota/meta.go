package quota

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Meta (Muse), as the management center reads it: the Muse key endpoint,
// asked with the DCA token kept in the credential file (the LLM key does not
// work there). The file is the only place that token lives, so it is read
// through the CPA client's credential download.

const metaQuotaURL = "https://api.meta.ai/muse-code/key"

var metaDCAToken = regexp.MustCompile(`^dca:[^\s]+$`)

func init() {
	register("meta", Provider{Fetch: fetchMeta})
}

// metaDownloader is the part of the CPA client that reads a credential file;
// the real client has it, the Caller interface does not.
type metaDownloader interface {
	DownloadCredential(ctx context.Context, name string) ([]byte, error)
}

func fetchMeta(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, msg("认证文件缺少 auth_index", "Auth file missing auth_index")
	}
	if strings.TrimSpace(cr.Name) == "" || cr.RuntimeOnly {
		return nil, msg("查询额度需要可下载的 Meta 认证文件。", "Quota requires a downloadable Meta authentication file.")
	}
	d, ok := c.(metaDownloader)
	if !ok {
		return nil, msg("无法下载用于查询额度的认证文件。", "Could not download the authentication file for quota lookup.")
	}
	file, err := d.DownloadCredential(ctx, cr.Name)
	if err != nil {
		return nil, msg("无法下载用于查询额度的认证文件。", "Could not download the authentication file for quota lookup.")
	}
	token, err := metaReadToken(file)
	if err != nil {
		return nil, err
	}

	// Errors never carry the body: the endpoint can echo the API key and
	// personal details even in a failure.
	failed := msg("Muse 额度请求失败，请检查 DCA 凭据是否有效。", "Muse quota request failed. Check that the DCA credential is valid.")
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: http.MethodPost, URL: metaQuotaURL,
		Header: map[string]string{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + token,
			"x-api-version": "1.0.0",
		}, Data: "{}"})
	if err != nil || !resp.OK() {
		return nil, failed
	}
	body, ok := parseBody(resp.Body).(map[string]any)
	if !ok {
		return nil, msg("Muse 返回的额度响应格式无效。", "Muse returned an invalid quota response.")
	}
	return metaResult(body), nil
}

// metaReadToken takes the DCA token from the credential file.
func metaReadToken(file []byte) (string, error) {
	var v any
	if json.Unmarshal(file, &v) != nil {
		return "", msg("下载的认证文件不是有效的 JSON。", "The downloaded authentication file is not valid JSON.")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", msg("下载的认证文件不是有效的 JSON。", "The downloaded authentication file is not valid JSON.")
	}
	token, _ := m["dca_token"].(string)
	token = strings.TrimSpace(token)
	if !metaDCAToken.MatchString(token) {
		return "", msg("认证文件缺少有效的 dca_token，请重新使用 Muse OAuth 登录。", "The authentication file has no valid dca_token. Sign in with Muse OAuth again.")
	}
	return token, nil
}

// metaResult reads the plan, the subscription state and the two usage
// windows. A valid answer without usage is an account with no quota known
// yet (no request made), not an error.
func metaResult(body map[string]any) *Result {
	usage := get(body, "subs_usage")
	r := &Result{Plan: str(get(body, "subs_tier_name"))}
	if r.Plan == "" {
		r.Plan = str(get(usage, "tier"))
	}
	if active, ok := body["is_subs_active"].(bool); ok {
		if active {
			r.Facts = append(r.Facts, Fact{Text{"订阅有效", "Subscription active"}, "✓"})
		} else {
			r.Facts = append(r.Facts, Fact{Text{"订阅未激活", "Subscription inactive"}, "✗"})
		}
	}

	window := metaWindow(get(usage, "window"), Text{"周期额度", "Usage window"}, true)
	weekly := metaWindow(get(usage, "weekly"), Text{"每周额度", "Weekly quota"}, false)
	window.Kind, weekly.Kind = KindShort, KindLong
	r.Groups = []Group{{Windows: []Window{window, weekly}}}
	if !window.Known && !weekly.Known {
		r.Notes = append(r.Notes, Text{"额度未知，可能尚未发起过模型请求。", "Quota unknown. You may not have made a model request yet."})
	}
	return r
}

func metaWindow(v any, label Text, withDuration bool) Window {
	w := Window{Label: label}
	if used, ok := metaNumber(get(v, "used_percent")); ok {
		w.Remaining, w.Known = clamp01(1-used/100), true
	}
	if at, ok := metaNumber(get(v, "resets_at")); ok && at > 0 && at < 1e14 {
		w.ResetAt = time.Unix(int64(at), 0)
	}
	if withDuration {
		if mins, ok := metaNumber(get(v, "window_duration_mins")); ok && mins > 0 {
			m := strconv.FormatFloat(mins, 'f', -1, 64)
			w.Label = Text{m + " 分钟周期", m + "-minute window"}
		}
	}
	return w
}

// metaNumber reads a finite number from a number or numeric text.
func metaNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}
