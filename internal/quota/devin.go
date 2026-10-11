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

// Devin, as the management center reads it: one Connect-RPC call to the
// Windsurf/Codeium seat service (GetUserStatus), which answers with the plan
// and the daily and weekly share of the quota left. The call only reads; it
// is a POST because Connect-RPC unary calls are.

const devinStatusURL = "https://server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus"

var (
	devinPercentText = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	devinDigits      = regexp.MustCompile(`^\d+$`)
	devinISODate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T`)
)

func init() {
	register("devin", Provider{Fetch: fetchDevin})
}

func devinHeaders() map[string]string {
	return map[string]string{
		"Content-Type":             "application/json",
		"Connect-Protocol-Version": "1",
	}
}

// devinRequestBody is the status request; CPA puts the credential's token in
// place of $TOKEN$ in the body as it does in headers.
func devinRequestBody() string {
	b, _ := json.Marshal(map[string]any{"metadata": map[string]string{
		"ideName":          "chisel",
		"ideVersion":       "3000.10.21",
		"apiKey":           "$TOKEN$",
		"locale":           "en",
		"os":               "darwin",
		"extensionVersion": "3000.10.21",
		"clientName":       "chisel",
	}})
	return string(b)
}

func fetchDevin(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error) {
	if cr.AuthIndex == "" {
		return nil, msg("缺少认证索引，请重新加载凭据列表。", "The credential auth index is missing. Reload the credential list.")
	}
	body, err := call(ctx, c, cr, http.MethodPost, devinStatusURL, devinHeaders(), devinRequestBody())
	if err != nil {
		return nil, err
	}
	return devinResult(body)
}

// devinResult reads the live status; an answer with neither a percentage
// nor a reset time carries no quota.
func devinResult(body map[string]any) (*Result, error) {
	status := get(body, "userStatus", "planStatus")
	var grp Group
	observed := false
	for _, w := range []struct {
		id    string
		label Text
		kind  WindowKind
	}{
		{"daily", Text{"每日剩余额度", "Daily remaining"}, KindShort},
		{"weekly", Text{"每周剩余额度", "Weekly remaining"}, KindLong},
	} {
		percent, known := devinPercent(get(status, w.id+"QuotaRemainingPercent"))
		reset := devinUnixSeconds(get(status, w.id+"QuotaResetAtUnix"))
		observed = observed || known || !reset.IsZero()
		win := Window{Label: w.label, Known: known, ResetAt: reset, Kind: w.kind}
		if known {
			win.Remaining = clamp01(percent / 100)
		}
		grp.Windows = append(grp.Windows, win)
	}
	if !observed {
		return nil, msg("Devin 未返回可用的额度数据。", "Devin returned no usable quota data.")
	}
	r := &Result{Plan: str(get(status, "planInfo", "planName")), Groups: []Group{grp}}
	if end := devinInstant(get(status, "planEnd")); !end.IsZero() {
		r.Facts = append(r.Facts, Fact{Text{"订阅到期", "Subscription ends"}, end.Local().Format("2006-01-02 15:04")})
	}
	return r, nil
}

// devinPercent reads a percentage from 0 to 100, as a number or plain
// decimal text; anything else is unknown.
func devinPercent(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		t := strings.TrimSpace(x)
		if !devinPercentText.MatchString(t) {
			return 0, false
		}
		var err error
		if f, err = strconv.ParseFloat(t, 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && f >= 0 && f <= 100
}

// devinUnixSeconds reads a whole number of Unix seconds, as a number or
// digits; zero when unreadable.
func devinUnixSeconds(v any) time.Time {
	var sec float64
	switch x := v.(type) {
	case float64:
		sec = x
	case string:
		t := strings.TrimSpace(x)
		if !devinDigits.MatchString(t) {
			return time.Time{}
		}
		sec, _ = strconv.ParseFloat(t, 64)
	default:
		return time.Time{}
	}
	if sec <= 0 || sec != math.Trunc(sec) || sec > 1<<53 || sec > 253402300799 {
		return time.Time{}
	}
	return time.Unix(int64(sec), 0)
}

// devinInstant reads an ISO 8601 timestamp (date and time).
func devinInstant(v any) time.Time {
	s, ok := v.(string)
	if !ok || !devinISODate.MatchString(s) {
		return time.Time{}
	}
	t := instant(s)
	if t.UnixMilli() <= 0 {
		return time.Time{}
	}
	return t
}
