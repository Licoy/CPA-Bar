// Package quota reads how much of a credential's usage limits is left, the
// way CPA's own management center does: built-in providers ask their
// upstream API through CPA's api-call route (CPA puts the credential's
// token in, so it never reaches CPA-Bar), and any other credential a quota
// plugin covers asks that plugin. Every provider maps its answer to Result.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/licoy/cpa-bar/internal/cpa"
)

// Text is a label in both languages the app speaks. Values the upstream
// server names itself (a plan, a plugin's label) use Same.
type Text struct{ ZH, EN string }

// Same is text that reads the same in every language.
func Same(s string) Text { return Text{s, s} }

// IsZero reports empty text.
func (t Text) IsZero() bool { return t.ZH == "" && t.EN == "" }

// Result is a credential's quota.
type Result struct {
	// Plan is the subscription, e.g. "Pro"; empty when unknown.
	Plan string
	// Facts are short label/value pairs shown under the plan, e.g. the
	// resets left or a credit balance.
	Facts []Fact
	// Groups hold the limits. Providers with one set of limits use one
	// group with no title.
	Groups []Group
	// Actions are what the provider lets the user do, e.g. spend a reset.
	Actions []Action
	// Notes are remarks about partial data, e.g. an optional request that
	// failed while the limits themselves were read.
	Notes []Text
}

// Fact is a label and its value.
type Fact struct {
	Label Text
	Value string
}

// Group is a set of limits that apply together, e.g. to some models.
type Group struct {
	Title Text
	Note  Text
	// Windows are the group's limits.
	Windows []Window
}

// Window is one usage limit.
type Window struct {
	Label Text
	// Remaining is the fraction left, 0 to 1; Known is false when the
	// provider did not say, and the limit shows no percentage.
	Remaining float64
	Known     bool
	// ResetAt is when the limit refills; zero when unknown.
	ResetAt time.Time
	// Detail is an optional remark, e.g. a dollar amount.
	Detail string
	// Kind marks the limits that summaries show, such as the tray's.
	Kind WindowKind
}

// WindowKind says which of an account's main limits a window is.
type WindowKind int

const (
	// KindOther is any other limit: of some models, of code review…
	KindOther WindowKind = iota
	// KindShort is the account's short rolling limit, mostly of five
	// hours (a day for some providers).
	KindShort
	// KindLong is its long limit: weekly, or monthly on plans that have
	// that instead.
	KindLong
)

// Summary picks the account's main limits: the first known short and long
// windows. A result that marks neither gives its first known window as
// the long one, so that something shows.
func (r *Result) Summary() (short, long *Window) {
	if r == nil {
		return nil, nil
	}
	var first *Window
	for gi := range r.Groups {
		for wi := range r.Groups[gi].Windows {
			w := &r.Groups[gi].Windows[wi]
			if !w.Known {
				continue
			}
			switch {
			case w.Kind == KindShort && short == nil:
				short = w
			case w.Kind == KindLong && long == nil:
				long = w
			}
			if first == nil {
				first = w
			}
		}
	}
	if short == nil && long == nil {
		long = first
	}
	return short, long
}

// Action is something the provider lets the user do with the quota. It
// changes the account upstream, often spending something limited, so the
// interface asks before running it, and never retries it.
type Action struct {
	ID      string
	Label   Text
	Confirm Text // what the confirmation says
	// Disabled actions show, but cannot run; Reason says why.
	Disabled bool
	Reason   Text
}

// Caller is the part of the CPA client quota needs.
type Caller interface {
	Upstream(ctx context.Context, r cpa.UpstreamRequest) (*cpa.UpstreamResponse, error)
	Plugins(ctx context.Context) (*cpa.PluginList, error)
	PluginQuota(ctx context.Context, id, authIndex string) (json.RawMessage, error)
}

// Provider reads the quota of one kind of credential.
type Provider struct {
	// ID names the provider, e.g. "claude", or "plugin:<id>".
	ID string
	// Fetch reads a credential's quota.
	Fetch func(ctx context.Context, c Caller, cr cpa.Credential) (*Result, error)
	// Act runs one of Result.Actions and says what came of it; nil when
	// there are none. A *Msg error explains a refusal in both languages.
	Act func(ctx context.Context, c Caller, cr cpa.Credential, action string) (Text, error)
}

// Msg is an error worded for the user in both languages.
type Msg struct {
	Text
	// Unknown marks an action that may or may not have happened upstream.
	Unknown bool
}

func (m *Msg) Error() string { return m.EN }

func msg(zh, en string) *Msg { return &Msg{Text: Text{zh, en}} }

var builtin = map[string]Provider{}

// register adds a built-in provider for the credentials ProviderKey maps
// to key.
func register(key string, p Provider) {
	if p.ID == "" {
		p.ID = key
	}
	builtin[key] = p
}

// ProviderKey is the provider a credential's quota is read for, normalized
// as the management center does: kimi-ai shares Kimi's API, Grok is xAI.
func ProviderKey(cr cpa.Credential) string {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(cr.ProviderName())), "_", "-")
	switch key {
	case "x-ai", "grok":
		return "xai"
	case "kimi-ai":
		return "kimi"
	}
	return key
}

// Supported reports whether a credential has a quota to read: a built-in
// provider covers it, or the server says a quota plugin does. A disabled
// credential has one too: what is left matters to whether to turn it on
// again.
func Supported(cr cpa.Credential) bool {
	if cr.AuthIndex == "" {
		return false
	}
	if _, ok := builtin[ProviderKey(cr)]; ok {
		return true
	}
	return cr.SupportsQuota && cr.QuotaProvider != ""
}

// For returns the provider that reads a credential's quota. plugins is the
// server's plugin list, needed only for credentials no built-in provider
// covers (a nil list then finds nothing). Built-in providers win even when
// a plugin covers the same provider, as in the management center.
func For(cr cpa.Credential, plugins []cpa.Plugin) (Provider, bool) {
	if !Supported(cr) {
		return Provider{}, false
	}
	if p, ok := builtin[ProviderKey(cr)]; ok {
		return p, true
	}
	id := pluginFor(plugins, cr.QuotaProvider)
	if id == "" {
		return Provider{}, false
	}
	return pluginProvider(id), true
}

// NeedsPlugins reports whether For needs the plugin list for cr.
func NeedsPlugins(cr cpa.Credential) bool {
	_, ok := builtin[ProviderKey(cr)]
	return Supported(cr) && !ok
}

// pluginFor mirrors the management center's match of a provider to the
// plugin serving its quota: by quota provider, plugin ID, then OAuth
// provider, and the only quota plugin when there is just one.
func pluginFor(plugins []cpa.Plugin, provider string) string {
	target := strings.ToLower(strings.TrimSpace(provider))
	if target == "" {
		return ""
	}
	var cands []cpa.Plugin
	for _, p := range plugins {
		if p.Registered && p.SupportsQuota {
			cands = append(cands, p)
		}
	}
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	for _, by := range []func(cpa.Plugin) string{
		func(p cpa.Plugin) string { return p.QuotaProvider },
		func(p cpa.Plugin) string { return p.ID },
		func(p cpa.Plugin) string { return p.OAuthProvider },
	} {
		for _, p := range cands {
			if norm(by(p)) == target {
				return p.ID
			}
		}
	}
	if len(cands) == 1 {
		return cands[0].ID
	}
	return ""
}

// ---- Helpers for the providers ----

// UpstreamError is a non-2xx answer from the upstream server.
type UpstreamError struct {
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%d %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// call makes an upstream request and decodes its JSON body. A non-2xx
// status is an *UpstreamError carrying the server's message, as the
// management center shows it.
func call(ctx context.Context, c Caller, cr cpa.Credential, method, url string, header map[string]string, data string) (map[string]any, error) {
	resp, err := c.Upstream(ctx, cpa.UpstreamRequest{AuthIndex: cr.AuthIndex, Method: method, URL: url, Header: header, Data: data})
	if err != nil {
		return nil, err
	}
	body := parseBody(resp.Body)
	if !resp.OK() {
		return nil, &UpstreamError{Status: resp.StatusCode, Message: errorMessage(body, resp.Body)}
	}
	m, ok := body.(map[string]any)
	if !ok {
		return nil, errors.New("the upstream answer is not a JSON object")
	}
	return m, nil
}

func parseBody(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	return v
}

func errorMessage(body any, raw string) string {
	if m, ok := body.(map[string]any); ok {
		switch e := m["error"].(type) {
		case map[string]any:
			if s, ok := e["message"].(string); ok && s != "" {
				return s
			}
		case string:
			if e != "" {
				return e
			}
		}
		if s, ok := m["message"].(string); ok && s != "" {
			return s
		}
	}
	raw = strings.TrimSpace(raw)
	if len(raw) > 300 {
		raw = raw[:300] + "…"
	}
	return raw
}

// get walks nested objects: get(m, "a", "b") is m["a"]["b"]. Keys may list
// alternatives separated by "|", e.g. "rate_limit|rateLimit".
func get(v any, path ...string) any {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = nil
		for _, alt := range strings.Split(k, "|") {
			if x, ok := m[alt]; ok && x != nil {
				v = x
				break
			}
		}
	}
	return v
}

// num reads a number, or a string holding one.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscan(strings.TrimSpace(x), &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// str reads a non-blank string.
func str(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// flag reads a boolean the way the management center does: true, 1,
// "yes", "on"…; ok is false when v says nothing.
func flag(v any) (on, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			return true, true
		case "false", "0", "no", "n", "off":
			return false, true
		}
	}
	return false, false
}

// instant reads a point in time: RFC 3339 text, or Unix seconds or
// milliseconds as a number or numeric text. Zero when unreadable.
func instant(v any) time.Time {
	if s := str(v); s != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t
			}
		}
	}
	f, ok := num(v)
	if !ok || f <= 0 {
		return time.Time{}
	}
	if f > 1e12 { // milliseconds
		return time.UnixMilli(int64(f))
	}
	return time.Unix(int64(f), 0)
}

// usedWindow is a window given as the percentage used, as most providers
// report it.
func usedWindow(label Text, usedPercent float64, known bool, reset time.Time) Window {
	w := Window{Label: label, ResetAt: reset, Known: known}
	if known {
		w.Remaining = clamp01(1 - usedPercent/100)
	}
	return w
}

func clamp01(f float64) float64 { return max(0, min(1, f)) }
