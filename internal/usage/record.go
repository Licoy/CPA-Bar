// Package usage collects the usage records CPA publishes on its usage
// stream, keeps them on disk per instance and sums them up for display.
//
// A record arrives with what identifies the client: the API key it used,
// its address, its user agent and the upstream response headers. Only a
// fingerprint and a masked form of the key are kept; the rest is dropped
// before anything is stored.
package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Record is one request as kept on disk.
type Record struct {
	Time      time.Time `json:"t"`
	RequestID string    `json:"id,omitempty"`
	Provider  string    `json:"p,omitempty"`
	Executor  string    `json:"ex,omitempty"`
	// Model is what the request was routed to; Alias what the client asked
	// for, when it differs; ResponseModel what the upstream answered with.
	Model         string `json:"m,omitempty"`
	Alias         string `json:"al,omitempty"`
	ResponseModel string `json:"rm,omitempty"`
	Endpoint      string `json:"ep,omitempty"`
	AuthType      string `json:"at,omitempty"`
	AuthIndex     string `json:"ai,omitempty"`
	// Source is the account that served the request, usually its email.
	// For an upstream API key CPA sends the key itself: only its masked form
	// is kept.
	Source string `json:"src,omitempty"`
	// KeyID is a fingerprint of the client's API key and KeyMask a masked
	// form of it ("••••9f2c"); the key itself is never kept.
	KeyID   string `json:"k,omitempty"`
	KeyMask string `json:"km,omitempty"`

	Failed   bool `json:"f,omitempty"`
	Canceled bool `json:"c,omitempty"`
	Status   int  `json:"s,omitempty"`
	// Error is the start of the failure body.
	Error  string `json:"e,omitempty"`
	Stream bool   `json:"st,omitempty"`

	LatencyMs int64 `json:"l,omitempty"`
	TTFTMs    int64 `json:"tf,omitempty"`

	Reasoning   string `json:"re,omitempty"`
	ServiceTier string `json:"tier,omitempty"`

	Tokens Tokens `json:"tk"`
}

// Tokens are the tokens of a request. Input includes the cached tokens and
// Output the reasoning ones, so Total is Input + Output.
type Tokens struct {
	Input       int64 `json:"i,omitempty"`
	Output      int64 `json:"o,omitempty"`
	Reasoning   int64 `json:"r,omitempty"`
	CacheRead   int64 `json:"cr,omitempty"`
	CacheCreate int64 `json:"cw,omitempty"`
	Total       int64 `json:"t,omitempty"`
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.CacheRead += o.CacheRead
	t.CacheCreate += o.CacheCreate
	t.Total += o.Total
}

// DisplayModel is the model a request is counted under: the one the client
// asked for.
func (r Record) DisplayModel() string {
	if r.Alias != "" {
		return r.Alias
	}
	if r.Model != "" {
		return r.Model
	}
	return "unknown"
}

// maxErrorLen bounds the failure body kept with a record.
const maxErrorLen = 300

// payload is the JSON CPA publishes. Fields that identify the client
// (client_ip, x_forwarded_for, user_agent, response_headers) are left out
// so they are never decoded.
type payload struct {
	Timestamp     string `json:"timestamp"`
	RequestID     string `json:"request_id"`
	Provider      string `json:"provider"`
	ExecutorType  string `json:"executor_type"`
	Model         string `json:"model"`
	Alias         string `json:"alias"`
	ResponseModel string `json:"response_model"`
	Endpoint      string `json:"endpoint"`
	AuthType      string `json:"auth_type"`
	AuthIndex     string `json:"auth_index"`
	Source        string `json:"source"`
	APIKey        string `json:"api_key"`
	Failed        bool   `json:"failed"`
	Stream        bool   `json:"stream"`
	LatencyMs     int64  `json:"latency_ms"`
	TTFTMs        int64  `json:"ttft_ms"`
	Reasoning     string `json:"reasoning_effort"`
	ServiceTier   string `json:"service_tier"`
	Fail          struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	} `json:"fail"`
	Tokens struct {
		Input       int64 `json:"input_tokens"`
		Output      int64 `json:"output_tokens"`
		Reasoning   int64 `json:"reasoning_tokens"`
		Cached      int64 `json:"cached_tokens"`
		CacheRead   int64 `json:"cache_read_tokens"`
		CacheCreate int64 `json:"cache_creation_tokens"`
		Total       int64 `json:"total_tokens"`
	} `json:"tokens"`
	Breakdown *struct {
		Quality string `json:"quality"`
		Total   int64  `json:"total_tokens"`
		Input   struct {
			Total      int64 `json:"total_tokens"`
			CacheRead  int64 `json:"cache_read_tokens"`
			CacheWrite int64 `json:"cache_write_tokens"`
		} `json:"input"`
		Output struct {
			Total     int64 `json:"total_tokens"`
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"output"`
	} `json:"token_breakdown"`
}

// Parse turns a published message into a record. Control messages such as
// {"refresh":true} and anything without a request id are not records.
func Parse(msg []byte, now time.Time) (Record, bool) {
	var p payload
	if err := json.Unmarshal(msg, &p); err != nil || strings.TrimSpace(p.RequestID) == "" {
		return Record{}, false
	}
	r := Record{
		RequestID:     p.RequestID,
		Provider:      strings.TrimSpace(p.Provider),
		Executor:      p.ExecutorType,
		Model:         strings.TrimSpace(p.Model),
		ResponseModel: strings.TrimSpace(p.ResponseModel),
		Endpoint:      p.Endpoint,
		AuthType:      p.AuthType,
		AuthIndex:     p.AuthIndex,
		Source:        source(p.Source, p.AuthType),
		Failed:        p.Failed,
		Stream:        p.Stream,
		LatencyMs:     max(0, p.LatencyMs),
		TTFTMs:        max(0, p.TTFTMs),
		Reasoning:     p.Reasoning,
		ServiceTier:   p.ServiceTier,
	}
	if a := strings.TrimSpace(p.Alias); a != "" && a != r.Model {
		r.Alias = a
	}
	if r.ResponseModel == r.Model {
		r.ResponseModel = ""
	}
	r.Time = now
	if t, err := time.Parse(time.RFC3339Nano, p.Timestamp); err == nil {
		r.Time = t
	}
	r.KeyID, r.KeyMask = keyFingerprint(p.APIKey)
	if p.Failed {
		r.Status = p.Fail.StatusCode
		r.Error = failBody(p.Fail.Body)
		low := strings.ToLower(r.Error)
		r.Canceled = r.Status == 499 || strings.Contains(low, "context canceled") || strings.Contains(low, "client closed request")
	}
	r.Tokens = tokensOf(p)
	return r, true
}

// tokensOf prefers CPA's canonical breakdown (input with the cache, output
// with the reasoning) and falls back to the raw counters.
func tokensOf(p payload) Tokens {
	if b := p.Breakdown; b != nil && (b.Quality == "complete" || b.Quality == "inconsistent") {
		t := Tokens{
			Input:       max(0, b.Input.Total),
			Output:      max(0, b.Output.Total),
			Reasoning:   max(0, b.Output.Reasoning),
			CacheRead:   max(0, b.Input.CacheRead),
			CacheCreate: max(0, b.Input.CacheWrite),
			Total:       max(0, b.Total),
		}
		if t.Total == 0 {
			t.Total = t.Input + t.Output
		}
		return t
	}
	k := p.Tokens
	t := Tokens{
		Input:       max(0, k.Input),
		Output:      max(0, k.Output),
		Reasoning:   max(0, k.Reasoning),
		CacheRead:   max(0, k.CacheRead),
		CacheCreate: max(0, k.CacheCreate),
		Total:       max(0, k.Total),
	}
	if t.CacheRead == 0 {
		t.CacheRead = max(0, k.Cached)
	}
	if t.Total == 0 {
		t.Total = t.Input + t.Output
	}
	return t
}

// keyFingerprint is a short id of a key, stable across restarts, and the
// key with all but its last four characters hidden (all of a short one).
func keyFingerprint(key string) (id, mask string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(key))
	id = hex.EncodeToString(sum[:6])
	mask = "••••"
	if r := []rune(key); len(r) > 12 {
		mask += string(r[len(r)-4:])
	}
	return id, mask
}

// source keeps an account name or email; an upstream API key, which CPA
// sends as the source of API-key credentials, is masked like client keys.
func source(s, authType string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "@") {
		return s
	}
	if strings.EqualFold(authType, "apikey") || strings.EqualFold(authType, "api-key") || looksLikeKey(s) {
		_, mask := keyFingerprint(s)
		return mask
	}
	return s
}

// looksLikeKey reports whether s looks like a secret rather than a name:
// long, without spaces, or with a common key prefix.
func looksLikeKey(s string) bool {
	for _, p := range []string{"sk-", "sk_", "AIza", "xai-", "gsk_", "pk-", "rk-", "ya29."} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return len(s) >= 32 && !strings.ContainsAny(s, " .")
}

func failBody(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxErrorLen {
		s = string(r[:maxErrorLen]) + "…"
	}
	return s
}
