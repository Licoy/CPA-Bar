// Package cpamp reads the usage history CPA-Manager-Plus keeps.
//
// CPA-Manager-Plus (github.com/seakee/CPA-Manager-Plus) is a management
// panel with a server of its own that collects CPA's usage records into a
// database. CPA-Bar keeps managing CPA directly; from Manager-Plus it only
// reads the collected records, with the panel's admin key. It never calls
// what writes or what takes records off CPA's queue
// (/v0/management/usage-queue).
package cpamp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// ServiceID is what Manager-Plus names itself as on /health.
const ServiceID = "cpa-manager-plus"

// DefaultPort is where Manager-Plus listens unless told otherwise.
const DefaultPort = "18317"

const (
	requestTimeout = 60 * time.Second
	maxResponse    = 64 << 20
)

// Options configures a Client.
type Options struct {
	// BaseURL is the address of the Manager-Plus server; NormalizeURL
	// says which forms it takes.
	BaseURL  string
	AdminKey string
	// CACertPEM adds certificate authorities to the system ones.
	CACertPEM []byte
	// ForwardAuthOnRedirect forwards the original Authorization header on
	// redirects, including redirects to another host.
	ForwardAuthOnRedirect bool
	// Transport replaces the default transport, for tests.
	Transport http.RoundTripper
}

// Client talks to one Manager-Plus server.
type Client struct {
	base string
	key  string
	http *http.Client
}

// NormalizeURL is the root of a Manager-Plus server: the address with its
// scheme (http by default) and without the page or API path it may have
// been copied with.
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("address is empty")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid address: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("address has no host")
	}
	if u.User != nil {
		return "", errors.New("credentials in the address are not supported")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	for _, cut := range []string{"/management.html", "/v0/management", "/usage-service", "/health", "/status"} {
		if i := strings.Index(path, cut); i >= 0 {
			path = path[:i]
		}
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// New creates a client. It does not contact the server.
func New(o Options) (*Client, error) {
	base, err := NormalizeURL(o.BaseURL)
	if err != nil {
		return nil, err
	}
	rt := o.Transport
	if rt == nil {
		t, err := cpa.NewTransport(o.CACertPEM)
		if err != nil {
			return nil, err
		}
		rt = t
	}
	return &Client{
		base: base,
		key:  strings.TrimSpace(o.AdminKey),
		http: &http.Client{
			Transport:     rt,
			CheckRedirect: cpa.RedirectPolicy(o.ForwardAuthOnRedirect),
		},
	}, nil
}

// BaseURL is the normalized root of the server.
func (c *Client) BaseURL() string { return c.base }

// Error is a response other than success.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("CPA-Manager-Plus: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("CPA-Manager-Plus: HTTP %d", e.Status)
}

// IsUnauthorized reports whether the server refused the admin key.
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// ErrNotManagerPlus means the address answers but is not Manager-Plus.
var ErrNotManagerPlus = errors.New("this address is not a CPA-Manager-Plus server")

func (c *Client) do(ctx context.Context, method, path string, body, out any, auth bool) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if c.key == "" {
			return errors.New("the CPA-Manager-Plus admin key is required")
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("CPA-Manager-Plus: unexpected response: %w", err)
	}
	return nil
}

// Detect reports whether the server is Manager-Plus, from /health, which
// needs no key.
func (c *Client) Detect(ctx context.Context) error {
	var h struct {
		Service string `json:"service"`
	}
	err := c.do(ctx, http.MethodGet, "/health", nil, &h, false)
	var e *Error
	if errors.As(err, &e) || (err != nil && strings.Contains(err.Error(), "unexpected response")) {
		return ErrNotManagerPlus
	}
	if err != nil {
		return err
	}
	if h.Service != ServiceID {
		return ErrNotManagerPlus
	}
	return nil
}

// Status is what the server says of its collection.
type Status struct {
	Events    int64 `json:"events"`
	Collector struct {
		Mode           string `json:"mode"`
		Transport      string `json:"transport"`
		LastConsumedAt int64  `json:"lastConsumedAt"`
		LastError      string `json:"lastError"`
	} `json:"collector"`
}

// Status reads the collection status; it checks the admin key.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, "/status", nil, &s, true)
	return s, err
}

// Probe checks that the server is Manager-Plus and takes the admin key.
func (c *Client) Probe(ctx context.Context) error {
	if err := c.Detect(ctx); err != nil {
		return err
	}
	_, err := c.Status(ctx)
	return err
}

// CPAAddress is the address of the CPA the server collects from; the CPA
// management key is never given out.
func (c *Client) CPAAddress(ctx context.Context) (string, error) {
	var r struct {
		Config struct {
			CPAConnection struct {
				CPABaseURL string `json:"cpaBaseUrl"`
			} `json:"cpaConnection"`
		} `json:"config"`
	}
	err := c.do(ctx, http.MethodGet, "/usage-service/config", nil, &r, true)
	return r.Config.CPAConnection.CPABaseURL, err
}

// Event is a usage record as Manager-Plus keeps it. What identifies the
// client (its address, user agent, the response headers) is left out so
// it is never decoded.
type Event struct {
	RequestID            string `json:"request_id"`
	EventHash            string `json:"event_hash"`
	TimestampMS          int64  `json:"timestamp_ms"`
	Model                string `json:"model"`
	RequestedModel       string `json:"requested_model"`
	ResolvedModel        string `json:"resolved_model"`
	ResponseModel        string `json:"response_model"`
	Stream               *bool  `json:"stream"`
	Endpoint             string `json:"endpoint"`
	Method               string `json:"method"`
	Path                 string `json:"path"`
	AuthIndex            string `json:"auth_index"`
	Source               string `json:"source"`
	APIKeyHash           string `json:"api_key_hash"`
	AccountSnapshot      string `json:"account_snapshot"`
	AuthProviderSnapshot string `json:"auth_provider_snapshot"`
	ReasoningEffort      string `json:"reasoning_effort"`
	ServiceTier          string `json:"service_tier"`
	ExecutorType         string `json:"executor_type"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	CachedTokens         int64  `json:"cached_tokens"`
	CacheReadTokens      int64  `json:"cache_read_tokens"`
	CacheCreationTokens  int64  `json:"cache_creation_tokens"`
	ReasoningTokens      int64  `json:"reasoning_tokens"`
	TotalTokens          int64  `json:"total_tokens"`
	LatencyMS            *int64 `json:"latency_ms"`
	TTFTMS               *int64 `json:"ttft_ms"`
	Failed               bool   `json:"failed"`
	FailStatusCode       *int64 `json:"fail_status_code"`
	FailSummary          string `json:"fail_summary"`
}

// Page is a page of events, newest first. The next page is the one before
// NextBeforeMS and NextBeforeID.
type Page struct {
	Items        []Event `json:"items"`
	NextBeforeMS int64   `json:"next_before_ms"`
	NextBeforeID int64   `json:"next_before_id"`
	HasMore      bool    `json:"has_more"`
}

// MaxPage is the most events a page holds.
const MaxPage = 5000

// Events reads a page of the events from from to to, newest first, before
// the cursor of the previous page (zeros for the first).
func (c *Client) Events(ctx context.Context, from, to time.Time, beforeMS, beforeID int64, limit int) (Page, error) {
	type eventsPage struct {
		Limit    int    `json:"limit"`
		BeforeMS *int64 `json:"before_ms,omitempty"`
		BeforeID *int64 `json:"before_id,omitempty"`
	}
	page := eventsPage{Limit: min(max(limit, 1), MaxPage)}
	if beforeMS > 0 {
		page.BeforeMS, page.BeforeID = &beforeMS, &beforeID
	}
	req := map[string]any{
		"from_ms": from.UnixMilli(),
		"to_ms":   to.UnixMilli(),
		"now_ms":  time.Now().UnixMilli(),
		"include": map[string]any{"events_page": page},
	}
	var resp struct {
		Events *Page `json:"events"`
	}
	if err := c.do(ctx, http.MethodPost, "/v0/management/monitoring/analytics", req, &resp, true); err != nil {
		return Page{}, err
	}
	if resp.Events == nil {
		return Page{}, nil
	}
	return *resp.Events, nil
}
