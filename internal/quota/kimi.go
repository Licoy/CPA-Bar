package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Kimi, as the management center reads it: the limits of the coding usage
// endpoint, on kimi.com or, for Kimi International accounts, kimi.ai.

const (
	kimiUsageURL   = "https://api.kimi.com/coding/v1/usages"
	kimiAIUsageURL = "https://api.kimi.ai/coding/v1/usages"
)

func init() {
	register("kimi", Provider{Fetch: fetchKimi})
}

// kimiDownloader is what reads a credential file, which holds the domain
// the account lives on; *cpa.Client does.
type kimiDownloader interface {
	DownloadCredential(ctx context.Context, name string) ([]byte, error)
}

func fetchKimi(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, errors.New("auth file missing auth_index")
	}
	endpoint, err := kimiEndpoint(ctx, c, cr)
	if err != nil {
		return nil, err
	}
	usage, err := call(ctx, c, cr, http.MethodGet, endpoint, map[string]string{"Authorization": "Bearer $TOKEN$"}, "")
	if err != nil {
		return nil, err
	}
	r := &Result{}
	if windows := kimiWindows(usage, time.Now()); len(windows) > 0 {
		r.Groups = []Group{{Windows: windows}}
	} else {
		r.Notes = []Text{{"暂无额度数据", "No quota data available"}}
	}
	return r, nil
}

// ---- Which host ----

// kimiEndpoint picks the usage URL the way the management center does: by
// the credential file's own domain, never guessed after a failure to read
// it, and only ever one of the two allowlisted hosts.
func kimiEndpoint(ctx context.Context, c Caller, cr cpa.Credential) (string, error) {
	dl, ok := c.(kimiDownloader)
	if !ok {
		// Nothing to read the file with: go by what the listing says.
		return kimiEndpointFor(nil, cr), nil
	}
	unavailable := errors.New("could not read the Kimi credential's domain; a downloadable, valid authentication file is required")
	if strings.TrimSpace(cr.Name) == "" || cr.RuntimeOnly {
		return "", unavailable
	}
	data, err := dl.DownloadCredential(ctx, cr.Name)
	if err != nil {
		return "", unavailable // never echo the error: it may carry credential text
	}
	var file map[string]any
	if json.Unmarshal(data, &file) != nil || file == nil {
		return "", unavailable
	}
	return kimiEndpointFor(file, cr), nil
}

// kimiDomain is "ai" or "com" for a domain, type or provider word; "" when
// it names neither.
func kimiDomain(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	d := strings.ToLower(strings.TrimSpace(s))
	switch {
	case d == "ai" || d == "kimi-ai" || d == "kimi.ai" || strings.HasSuffix(d, ".kimi.ai"):
		return "ai"
	case d == "com" || d == "kimi" || d == "kimi.com" || strings.HasSuffix(d, ".kimi.com"):
		return "com"
	}
	return ""
}

func kimiBaseURLDomain(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case h == "kimi.ai" || strings.HasSuffix(h, ".kimi.ai"):
		return "ai"
	case h == "kimi.com" || strings.HasSuffix(h, ".kimi.com"):
		return "com"
	}
	return ""
}

var kimiAIName = regexp.MustCompile(`(?i)kimi-ai|kimi\.ai`)

// kimiEndpointFor mirrors the backend's choice: an explicit domain, the
// base URL, the file's type, the credential's provider, then its name.
func kimiEndpointFor(file map[string]any, cr cpa.Credential) string {
	domain := ""
	if d, ok := file["domain"].(string); ok && strings.TrimSpace(d) != "" {
		if domain = kimiDomain(d); domain == "" {
			domain = "com"
		}
	}
	if domain == "" {
		base, has := file["base_url"]
		if !has {
			base = file["base-url"]
		}
		domain = kimiBaseURLDomain(base)
	}
	if domain == "" {
		domain = kimiDomain(file["type"])
	}
	if domain == "" {
		domain = kimiDomain(strings.ReplaceAll(cr.ProviderName(), "_", "-"))
	}
	if domain == "" {
		domain = "com"
		if kimiAIName.MatchString(cr.ID + " " + cr.Name) {
			domain = "ai"
		}
	}
	if domain == "ai" {
		return kimiAIUsageURL
	}
	return kimiUsageURL
}

// ---- Limits ----

// kimiInt reads a whole number from a number or numeric text, rounding down.
func kimiInt(v any) (int64, bool) {
	f, ok := codexNumber(v)
	if !ok {
		return 0, false
	}
	return int64(math.Floor(f)), true
}

// kimiTimeUnit reads a window's unit, e.g. TIME_UNIT_MINUTE; a missing one
// counts as minutes, an unknown one as nothing.
func kimiTimeUnit(v any) string {
	s, _ := v.(string)
	u := strings.ToUpper(strings.TrimSpace(s))
	u = strings.TrimPrefix(u, "TIME_UNIT_")
	switch u {
	case "SECONDS", "SECOND":
		return "s"
	case "", "MINUTES", "MINUTE":
		return "m"
	case "HOURS", "HOUR":
		return "h"
	case "DAYS", "DAY":
		return "d"
	case "WEEKS", "WEEK":
		return "w"
	}
	return ""
}

func kimiDuration(d int64, unit any) string {
	switch kimiTimeUnit(unit) {
	case "s":
		return fmt.Sprintf("%ds", d)
	case "h":
		return fmt.Sprintf("%dh", d)
	case "d":
		return fmt.Sprintf("%dd", d)
	case "w":
		return fmt.Sprintf("%dw", d)
	}
	if d%60 == 0 {
		return fmt.Sprintf("%dh", d/60)
	}
	return fmt.Sprintf("%dm", d)
}

// kimiLabel names a limit: by its own name, title or scope, else by the
// length of its window, else by its position.
func kimiLabel(item, detail map[string]any, window map[string]any, index int) Text {
	for _, k := range []string{"name", "title", "scope"} {
		v := item[k]
		if v == nil {
			v = detail[k]
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return Same(strings.TrimSpace(s))
		}
	}
	var duration int64
	var have bool
	for _, src := range []map[string]any{window, item, detail} {
		if d, ok := kimiInt(src["duration"]); ok {
			duration, have = d, true
			break
		}
	}
	if have && duration > 0 {
		var unit any
		for _, src := range []map[string]any{window, item, detail} {
			if src["timeUnit"] != nil {
				unit = src["timeUnit"]
				break
			}
		}
		d := kimiDuration(duration, unit)
		return Text{d + " 限额", d + " limit"}
	}
	return Text{fmt.Sprintf("限额 #%d", index+1), fmt.Sprintf("Limit #%d", index+1)}
}

// kimiRow turns one usage block into a window; ok is false when it states
// neither a limit nor a use.
func kimiRow(data map[string]any, fallback Text, now time.Time) (Window, bool) {
	limit, hasLimit := kimiInt(data["limit"])
	used, hasUsed := kimiInt(data["used"])
	if !hasUsed {
		if remaining, ok := kimiInt(data["remaining"]); ok && hasLimit {
			used, hasUsed = limit-remaining, true
		}
	}
	if !hasUsed && !hasLimit {
		return Window{}, false
	}
	label := fallback
	for _, k := range []string{"name", "title"} {
		if s, ok := data[k].(string); ok && strings.TrimSpace(s) != "" {
			label = Same(strings.TrimSpace(s))
			break
		}
	}
	w := Window{Label: label, ResetAt: kimiResetAt(data, now)}
	switch {
	case limit > 0:
		w.Known = true
		w.Remaining = math.Round(clamp01(float64(limit-used)/float64(limit))*100) / 100
	case used > 0:
		w.Known = true // a use against no limit leaves nothing
	}
	return w, true
}

// kimiResetAt reads when a limit resets: an absolute time, else seconds from
// now.
func kimiResetAt(data map[string]any, now time.Time) time.Time {
	for _, k := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		if t := instant(data[k]); !t.IsZero() {
			return t
		}
	}
	for _, k := range []string{"reset_in", "resetIn", "ttl"} {
		if n, ok := kimiInt(data[k]); ok && n > 0 {
			return now.Add(time.Duration(n) * time.Second)
		}
	}
	return time.Time{}
}

// kimiShort reports whether a limit's window is a short one, of a day
// at most.
func kimiShort(item, detail, window map[string]any) bool {
	srcs := []map[string]any{window, item, detail}
	var d int64
	for _, src := range srcs {
		if v, ok := kimiInt(src["duration"]); ok {
			d = v
			break
		}
	}
	var unit any
	for _, src := range srcs {
		if src["timeUnit"] != nil {
			unit = src["timeUnit"]
			break
		}
	}
	seconds := map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 7 * 86400}[kimiTimeUnit(unit)]
	return d > 0 && seconds > 0 && d*seconds <= 86400
}

func kimiWindows(usage map[string]any, now time.Time) []Window {
	var out []Window
	limits, _ := usage["limits"].([]any)
	for i, x := range limits {
		item, ok := x.(map[string]any)
		if !ok {
			continue
		}
		detail := item
		if d, ok := item["detail"].(map[string]any); ok {
			detail = d
		}
		window, _ := item["window"].(map[string]any)
		if w, ok := kimiRow(detail, kimiLabel(item, detail, window, i), now); ok {
			if kimiShort(item, detail, window) {
				w.Kind = KindShort
			}
			out = append(out, w)
		}
	}
	if u, ok := usage["usage"].(map[string]any); ok {
		if w, ok := kimiRow(u, Text{"周限额", "Weekly limit"}, now); ok {
			w.Kind = KindLong
			out = append(out, w)
		}
	}
	// Plans without a weekly limit report their monthly total as a ratio.
	if m, ok := get(usage, "usages", "limit_month_total").(map[string]any); ok {
		if ratio, ok := codexNumber(m["used_ratio"]); ok {
			row := map[string]any{"used": math.Round(ratio * 100), "limit": float64(100), "reset_time": m["reset_time"]}
			if w, ok := kimiRow(row, Text{"月限额", "Monthly limit"}, now); ok {
				out = append(out, w)
			}
		}
	}
	return out
}
