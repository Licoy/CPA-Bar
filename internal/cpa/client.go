// Package cpa is the HTTP client of one CLIProxyAPI instance's v8
// management API. A Client belongs to exactly one instance: its errors carry
// that instance, and nothing is shared between clients but the process.
package cpa

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Default timeouts. Reads poll in the background and must fail fast;
// writes and uploads get longer, separate budgets.
const (
	DefaultReadTimeout   = 10 * time.Second
	DefaultWriteTimeout  = 30 * time.Second
	DefaultUploadTimeout = 2 * time.Minute

	maxResponseBytes = 64 << 20
)

// Options configures a Client.
type Options struct {
	InstanceID   string
	InstanceName string
	// BaseURL is any address NormalizeManagementURL accepts.
	BaseURL       string
	ManagementKey string
	// CACertPEM adds certificate authorities to the system pool, for
	// servers with a certificate of their own CA. Verification is never
	// turned off.
	CACertPEM []byte

	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	UploadTimeout time.Duration

	// Transport replaces the default transport, for tests.
	Transport http.RoundTripper
}

// Client talks to one CPA instance.
type Client struct {
	instanceID   string
	instanceName string
	base         string
	key          string
	http         *http.Client
	readTimeout  time.Duration
	writeTimeout time.Duration
	uploadTO     time.Duration
	// tlsConf has the extra certificate authorities, nil for the system
	// ones; the usage stream dials with it as the HTTP requests do.
	tlsConf *tls.Config

	mu   sync.Mutex
	info ServerInfo
}

// New creates a client. It does not contact the server.
func New(o Options) (*Client, error) {
	base, err := NormalizeManagementURL(o.BaseURL)
	if err != nil {
		return nil, err
	}
	var tlsConf *tls.Config
	rt := o.Transport
	if rt == nil {
		t, err := NewTransport(o.CACertPEM)
		if err != nil {
			return nil, err
		}
		tlsConf = t.TLSClientConfig
		rt = t
	}
	c := &Client{
		instanceID:   o.InstanceID,
		instanceName: o.InstanceName,
		base:         base,
		key:          o.ManagementKey,
		http:         &http.Client{Transport: rt},
		readTimeout:  orDefault(o.ReadTimeout, DefaultReadTimeout),
		writeTimeout: orDefault(o.WriteTimeout, DefaultWriteTimeout),
		uploadTO:     orDefault(o.UploadTimeout, DefaultUploadTimeout),
		tlsConf:      tlsConf,
	}
	return c, nil
}

// NewTransport is the default transport, trusting the certificate
// authorities of caPEM in addition to the system ones; its TLS config is
// nil without them. Verification is never turned off.
func NewTransport(caPEM []byte) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if len(bytes.TrimSpace(caPEM)) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return t, nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// BaseURL is the normalized v8 management URL.
func (c *Client) BaseURL() string { return c.base }

// InstanceID is the instance this client belongs to.
func (c *Client) InstanceID() string { return c.instanceID }

// ServerInfo returns what the last response said about the server.
func (c *Client) ServerInfo() ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

type request struct {
	op       string
	method   string
	path     string // relative to base, starting with "/"
	query    url.Values
	body     io.Reader
	ctype    string
	write    bool
	upload   bool
	resource bool // a 404 means "resource not found", not "no management API"
	accept   string
	timeout  time.Duration // overrides the one the kind of request implies
}

// do performs a request and decodes a JSON response into out, which may be
// nil, a *[]byte for the raw body, or any JSON target.
func (c *Client) do(ctx context.Context, r request, out any) error {
	timeout := c.readTimeout
	switch {
	case r.upload:
		timeout = c.uploadTO
	case r.write:
		timeout = c.writeTimeout
	}
	if r.timeout > 0 {
		timeout = r.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.base + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, r.body)
	if err != nil {
		return c.fail(r, 0, KindInvalid, "", err)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if r.ctype != "" {
		req.Header.Set("Content-Type", r.ctype)
	}
	accept := r.accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "CPA-Bar")

	resp, err := c.http.Do(req)
	if err != nil {
		return c.fail(r, 0, kindForTransport(ctx, err, r.write), "", err)
	}
	defer resp.Body.Close()
	c.recordInfo(resp.Header)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		kind := kindForTransport(ctx, err, r.write)
		return c.fail(r, resp.StatusCode, kind, "", err)
	}
	if len(body) > maxResponseBytes {
		return c.fail(r, resp.StatusCode, KindDecode, "response too large", nil)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		kind := kindForStatus(resp.StatusCode, r.resource)
		if resp.StatusCode == http.StatusNotFound && r.resource && !looksLikeManagementJSON(body) {
			// A bare 404 page, not the API's JSON: the route itself is missing.
			kind = KindManagementUnavailable
		}
		return c.fail(r, resp.StatusCode, kind, serverMessage(body), nil)
	}
	if resp.StatusCode == http.StatusMultiStatus {
		return c.fail(r, resp.StatusCode, KindConflict, partialMessage(body), nil)
	}

	switch dst := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*dst = body
		return nil
	default:
		if err := json.Unmarshal(body, out); err != nil {
			return c.fail(r, resp.StatusCode, KindDecode, "invalid JSON in response", err)
		}
		return nil
	}
}

func (c *Client) fail(r request, status int, kind Kind, msg string, err error) error {
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return &APIError{
		InstanceID:   c.instanceID,
		InstanceName: c.instanceName,
		Op:           r.op,
		Method:       r.method,
		Path:         r.path,
		Status:       status,
		Kind:         kind,
		Message:      msg,
		Err:          err,
	}
}

func (c *Client) recordInfo(h http.Header) {
	v := h.Get("X-CPA-VERSION")
	if v == "" {
		return
	}
	c.mu.Lock()
	c.info = ServerInfo{
		Version:       v,
		Commit:        h.Get("X-CPA-COMMIT"),
		BuildDate:     h.Get("X-CPA-BUILD-DATE"),
		SupportPlugin: h.Get("X-CPA-SUPPORT-PLUGIN"),
	}
	c.mu.Unlock()
}

// serverMessage extracts CPA's error text: {"error": "..."} or
// {"error": "slug", "message": "..."}.
func serverMessage(body []byte) string {
	var v struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &v) == nil {
		errText := ""
		switch e := v.Error.(type) {
		case string:
			errText = e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				errText = m
			}
		}
		switch {
		case errText != "" && v.Message != "":
			return errText + ": " + v.Message
		case v.Message != "":
			return v.Message
		case errText != "":
			return errText
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func looksLikeManagementJSON(body []byte) bool {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	_, ok := v["error"]
	return ok
}

func partialMessage(body []byte) string {
	var v struct {
		Failed []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Failed) == 0 {
		return "partially failed"
	}
	parts := make([]string, 0, len(v.Failed))
	for _, f := range v.Failed {
		parts = append(parts, f.Name+": "+f.Error)
	}
	return "partially failed: " + strings.Join(parts, "; ")
}

func jsonBody(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

// ---- Connection ----

// Probe confirms that the v8 management API answers with this key: it reads
// the configuration and the credential list, as adding an instance does.
func (c *Client) Probe(ctx context.Context) error {
	var cfg json.RawMessage
	if err := c.do(ctx, request{op: "read configuration", method: http.MethodGet, path: "/config"}, &cfg); err != nil {
		return err
	}
	_, err := c.ListCredentials(ctx)
	return err
}

// ---- Credentials ----

// ListCredentials reads every credential with its run state.
func (c *Client) ListCredentials(ctx context.Context) (*CredentialList, error) {
	var out CredentialList
	err := c.do(ctx, request{op: "list credentials", method: http.MethodGet, path: "/credentials"}, &out)
	if err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = []Credential{}
	}
	return &out, nil
}

// SetCredentialDisabled enables or disables a credential.
func (c *Client) SetCredentialDisabled(ctx context.Context, name, authIndex string, disabled bool) error {
	body, _ := jsonBody(map[string]any{"name": name, "auth_index": authIndex, "disabled": disabled})
	op := "enable credential"
	if disabled {
		op = "disable credential"
	}
	return c.do(ctx, request{op: op, method: http.MethodPatch, path: "/credentials/status",
		body: body, ctype: "application/json", write: true, resource: true}, nil)
}

// PatchCredentialFields edits credential metadata. fields must include
// "name" (the credential ID or file name). The server writes every other
// key into the credential file as is, so send only the fields to change:
// never "auth_index", which it does not read.
func (c *Client) PatchCredentialFields(ctx context.Context, fields map[string]any) error {
	body, err := jsonBody(fields)
	if err != nil {
		return err
	}
	return c.do(ctx, request{op: "edit credential", method: http.MethodPatch, path: "/credentials/fields",
		body: body, ctype: "application/json", write: true, resource: true}, nil)
}

// DeleteCredentials deletes credential files on the server.
func (c *Client) DeleteCredentials(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	q := url.Values{"name": names}
	return c.do(ctx, request{op: "delete credentials", method: http.MethodDelete, path: "/credentials",
		query: q, write: true, resource: true}, nil)
}

// RefreshCredential forces an OAuth refresh of one credential, or of all
// when name is empty.
func (c *Client) RefreshCredential(ctx context.Context, name, authIndex string) error {
	payload := map[string]any{"all": true}
	if name != "" {
		payload = map[string]any{"name": name, "auth_index": authIndex}
	}
	body, _ := jsonBody(payload)
	return c.do(ctx, request{op: "refresh credential", method: http.MethodPost, path: "/credentials/refresh",
		body: body, ctype: "application/json", write: true, resource: true}, nil)
}

// CredentialModels lists the models a credential serves.
func (c *Client) CredentialModels(ctx context.Context, name string) ([]CredentialModel, error) {
	var out struct {
		Models []CredentialModel `json:"models"`
	}
	err := c.do(ctx, request{op: "list credential models", method: http.MethodGet, path: "/credentials/models",
		query: url.Values{"name": {name}}, resource: true}, &out)
	return out.Models, err
}

// DownloadCredential returns a credential file's contents.
func (c *Client) DownloadCredential(ctx context.Context, name string) ([]byte, error) {
	var b []byte
	err := c.do(ctx, request{op: "download credential", method: http.MethodGet, path: "/credentials/download",
		query: url.Values{"name": {name}}, resource: true, accept: "*/*"}, &b)
	return b, err
}

// UploadCredential uploads a credential JSON file.
func (c *Client) UploadCredential(ctx context.Context, filename string, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	return c.do(ctx, request{op: "upload credential", method: http.MethodPost, path: "/credentials",
		body: &buf, ctype: mw.FormDataContentType(), write: true, upload: true}, nil)
}

// ResetCooldown clears a credential's cooldown.
func (c *Client) ResetCooldown(ctx context.Context, authIndex string) error {
	body, _ := jsonBody(map[string]any{"auth_index": authIndex})
	return c.do(ctx, request{op: "reset cooldown", method: http.MethodPost, path: "/routing/cooldown/reset",
		body: body, ctype: "application/json", write: true, resource: true}, nil)
}

// ---- Configuration ----

// configPath turns "routing/retry/request-retry" or
// []{"routing","retry","request-retry"} into an escaped route.
func configPath(segments []string) string {
	if len(segments) == 0 {
		return "/config"
	}
	esc := make([]string, 0, len(segments))
	for _, s := range segments {
		for _, part := range strings.Split(s, "/") {
			if part = strings.TrimSpace(part); part != "" {
				esc = append(esc, url.PathEscape(part))
			}
		}
	}
	if len(esc) == 0 {
		return "/config"
	}
	return "/config/" + strings.Join(esc, "/")
}

// GetConfig reads the configuration, or the node at path, as raw JSON.
func (c *Client) GetConfig(ctx context.Context, path ...string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, request{op: "read configuration", method: http.MethodGet, path: configPath(path), resource: len(path) > 0}, &out)
	return out, err
}

// PutConfig replaces the node at path (the whole configuration without
// one) with value, a raw JSON value without an envelope.
func (c *Client) PutConfig(ctx context.Context, value json.RawMessage, path ...string) error {
	return c.do(ctx, request{op: "save configuration", method: http.MethodPut, path: configPath(path),
		body: bytes.NewReader(value), ctype: "application/json", write: true, resource: len(path) > 0}, nil)
}

// PatchConfig merges an object into the node at path. Lists and scalars in
// value replace what is there.
func (c *Client) PatchConfig(ctx context.Context, value json.RawMessage, path ...string) error {
	return c.do(ctx, request{op: "update configuration", method: http.MethodPatch, path: configPath(path),
		body: bytes.NewReader(value), ctype: "application/json", write: true, resource: len(path) > 0}, nil)
}

// DeleteConfig removes the field at path.
func (c *Client) DeleteConfig(ctx context.Context, path ...string) error {
	if configPath(path) == "/config" { // also true for blank segments
		return errors.New("refusing to delete the whole configuration")
	}
	return c.do(ctx, request{op: "delete configuration field", method: http.MethodDelete, path: configPath(path),
		write: true, resource: true}, nil)
}

// GetConfigYAML reads the whole configuration as YAML.
func (c *Client) GetConfigYAML(ctx context.Context) (string, error) {
	var b []byte
	err := c.do(ctx, request{op: "read configuration YAML", method: http.MethodGet, path: "/config.yaml", accept: "application/yaml, text/yaml, */*"}, &b)
	return string(b), err
}

// PutConfigYAML replaces the whole configuration with YAML. The server
// validates it and rejects invalid documents without changing the file.
func (c *Client) PutConfigYAML(ctx context.Context, doc string) error {
	return c.do(ctx, request{op: "save configuration YAML", method: http.MethodPut, path: "/config.yaml",
		body: strings.NewReader(doc), ctype: "application/yaml", write: true}, nil)
}

// ---- Observability ----

// Logs reads application log lines after cursor; an empty cursor tails the
// last limit lines.
func (c *Client) Logs(ctx context.Context, cursor string, limit int) (*LogPage, error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var out LogPage
	err := c.do(ctx, request{op: "read logs", method: http.MethodGet, path: "/observability/logs", query: q, resource: true}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearLogs removes rotated logs and truncates the active one.
func (c *Client) ClearLogs(ctx context.Context) error {
	return c.do(ctx, request{op: "clear logs", method: http.MethodDelete, path: "/observability/logs", write: true, resource: true}, nil)
}

// ErrorLogs lists request error log files, raw.
func (c *Client) ErrorLogs(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, request{op: "list error logs", method: http.MethodGet, path: "/observability/logs/errors", resource: true}, &out)
	return out, err
}

// DownloadErrorLog downloads one error log file.
func (c *Client) DownloadErrorLog(ctx context.Context, name string) ([]byte, error) {
	var b []byte
	err := c.do(ctx, request{op: "download error log", method: http.MethodGet,
		path: "/observability/logs/errors/" + url.PathEscape(name), resource: true, accept: "*/*"}, &b)
	return b, err
}

// RequestLog reads one request log by its ID.
func (c *Client) RequestLog(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := c.do(ctx, request{op: "read request log", method: http.MethodGet,
		path: "/observability/logs/requests/" + url.PathEscape(id), resource: true, accept: "*/*"}, &b)
	return b, err
}

// APICall performs a manual upstream request through CPA, for diagnosis.
// payload is passed as is; the response is returned raw.
func (c *Client) APICall(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, request{op: "diagnostic request", method: http.MethodPost, path: "/requests/api-call",
		body: bytes.NewReader(payload), ctype: "application/json", write: true}, &out)
	return out, err
}

// LatestVersion reads the server's latest release information, raw.
func (c *Client) LatestVersion(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, request{op: "check server version", method: http.MethodGet, path: "/server/latest-version"}, &out)
	return out, err
}

// ---- OAuth ----

// StartOAuth starts a login. extra holds provider-specific parameters.
func (c *Client) StartOAuth(ctx context.Context, provider string, extra url.Values) (*OAuthStart, error) {
	q := url.Values{}
	for k, v := range extra {
		q[k] = v
	}
	q.Set("provider", provider)
	var out OAuthStart
	err := c.do(ctx, request{op: "start login", method: http.MethodGet, path: "/oauth/auth-url", query: q, resource: true}, &out)
	if err != nil {
		return nil, err
	}
	if out.State == "" {
		return nil, c.fail(request{op: "start login", method: http.MethodGet, path: "/oauth/auth-url"}, 200, KindDecode, "response has no state", nil)
	}
	return &out, nil
}

// OAuthStatus polls a login.
func (c *Client) OAuthStatus(ctx context.Context, state string) (*OAuthStatus, error) {
	var out OAuthStatus
	err := c.do(ctx, request{op: "check login", method: http.MethodGet, path: "/oauth/status",
		query: url.Values{"state": {state}}, resource: true}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelOAuth cancels a pending login.
func (c *Client) CancelOAuth(ctx context.Context, state string) error {
	return c.do(ctx, request{op: "cancel login", method: http.MethodDelete, path: "/oauth/session",
		query: url.Values{"state": {state}}, write: true, resource: true}, nil)
}

// SubmitOAuthCallback hands CPA the full callback URL the browser reached,
// for servers the browser cannot redirect to directly.
func (c *Client) SubmitOAuthCallback(ctx context.Context, provider, redirectURL string) error {
	payload := map[string]any{"redirect_url": redirectURL}
	if provider != "" {
		payload["provider"] = provider
	}
	body, _ := jsonBody(payload)
	return c.do(ctx, request{op: "submit login callback", method: http.MethodPost, path: "/oauth/callback",
		body: body, ctype: "application/json", write: true, resource: true}, nil)
}

// ---- Plugins ----

// Plugins lists installed plugins.
func (c *Client) Plugins(ctx context.Context) (*PluginList, error) {
	var out PluginList
	if err := c.do(ctx, request{op: "list plugins", method: http.MethodGet, path: "/plugins"}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PluginStore lists the plugin store.
func (c *Client) PluginStore(ctx context.Context) (*PluginStore, error) {
	var out PluginStore
	if err := c.do(ctx, request{op: "list plugin store", method: http.MethodGet, path: "/plugins/store"}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InstallPlugin installs or updates a plugin from the store.
func (c *Client) InstallPlugin(ctx context.Context, id string) error {
	return c.do(ctx, request{op: "install plugin", method: http.MethodPost,
		path: "/plugins/store/" + url.PathEscape(id) + "/install", write: true, upload: true, resource: true}, nil)
}

// DeletePlugin uninstalls a plugin.
func (c *Client) DeletePlugin(ctx context.Context, id string) error {
	return c.do(ctx, request{op: "delete plugin", method: http.MethodDelete,
		path: "/plugins/" + url.PathEscape(id), write: true, resource: true}, nil)
}

// SetPluginEnabled turns a plugin on or off through its configuration.
func (c *Client) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	b, _ := json.Marshal(map[string]any{"enabled": enabled})
	return c.PatchConfig(ctx, b, "plugins", "configs", id)
}

// UpstreamRequest asks CPA to make an HTTP request upstream on behalf of a
// credential. CPA replaces "$TOKEN$" in the URL, headers and body with the
// credential's token, so the token never reaches CPA-Bar.
type UpstreamRequest struct {
	AuthIndex string            `json:"auth_index,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      string            `json:"data,omitempty"`
}

// UpstreamResponse is what the upstream server answered.
type UpstreamResponse struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       string              `json:"body"`
}

// OK reports a 2xx upstream status.
func (r *UpstreamResponse) OK() bool { return r.StatusCode >= 200 && r.StatusCode < 300 }

// apiCallTimeout leaves room for CPA to reach the upstream server.
const upstreamTimeout = 30 * time.Second

// Upstream makes an upstream request through CPA. Anything but GET counts as
// a write: a timeout leaves its outcome unknown, and it is never retried.
func (c *Client) Upstream(ctx context.Context, r UpstreamRequest) (*UpstreamResponse, error) {
	body, err := jsonBody(r)
	if err != nil {
		return nil, err
	}
	var out UpstreamResponse
	err = c.do(ctx, request{op: "call upstream", method: http.MethodPost, path: "/requests/api-call",
		body: body, ctype: "application/json", write: !strings.EqualFold(r.Method, http.MethodGet), resource: true, timeout: upstreamTimeout}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// PluginQuota reads the quota a plugin last reported for its provider.
func (c *Client) PluginQuota(ctx context.Context, id, authIndex string) (json.RawMessage, error) {
	if authIndex == "" {
		return nil, errors.New("a credential (auth_index) is required to read a plugin quota")
	}
	var out json.RawMessage
	err := c.do(ctx, request{op: "read plugin quota", method: http.MethodGet,
		path: "/plugins/" + url.PathEscape(id) + "/quota", query: url.Values{"auth_index": {authIndex}}, resource: true}, &out)
	return out, err
}
