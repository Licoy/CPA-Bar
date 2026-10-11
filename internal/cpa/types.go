package cpa

import (
	"encoding/json"
	"strings"
	"time"
)

// Credential statuses reported by CPA.
const (
	StatusActive     = "active"
	StatusPending    = "pending"
	StatusRefreshing = "refreshing"
	StatusError      = "error"
	StatusDisabled   = "disabled"
	StatusUnknown    = "unknown"
)

// Credential is one entry of GET /credentials. Fields the client does not
// model stay in Raw, so nothing the server adds is lost when displayed.
type Credential struct {
	ID             string     `json:"id"`
	AuthIndex      string     `json:"auth_index"`
	Name           string     `json:"name"`
	Type           string     `json:"type"`
	Provider       string     `json:"provider"`
	Label          string     `json:"label"`
	Email          string     `json:"email"`
	Account        string     `json:"account"`
	AccountType    string     `json:"account_type"`
	ProjectID      string     `json:"project_id"`
	Status         string     `json:"status"`
	StatusMessage  string     `json:"status_message"`
	Disabled       bool       `json:"disabled"`
	Unavailable    bool       `json:"unavailable"`
	RuntimeOnly    bool       `json:"runtime_only"`
	Source         string     `json:"source"` // "file" or "memory"
	Path           string     `json:"path"`
	Size           int64      `json:"size"`
	Success        int64      `json:"success"`
	Failed         int64      `json:"failed"`
	SupportsQuota  bool       `json:"supports_quota"`
	QuotaProvider  string     `json:"quota_provider"`
	Note           string     `json:"note"`
	Priority       *float64   `json:"priority"`
	CreatedAt      *time.Time `json:"created_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
	LastRefresh    *time.Time `json:"last_refresh"`
	NextRetryAfter *time.Time `json:"next_retry_after"`

	Raw json.RawMessage `json:"-"`
}

func (c *Credential) UnmarshalJSON(b []byte) error {
	type plain Credential
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Credential(p)
	c.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// ProviderName returns the provider, falling back to the type.
func (c Credential) ProviderName() string {
	if c.Provider != "" {
		return c.Provider
	}
	return c.Type
}

// DisplayName is the best human label: label, email, account, then name.
func (c Credential) DisplayName() string {
	for _, s := range []string{c.Label, c.Email, c.Account, c.Name, c.ID} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// Health is CPA's run state of a credential, derived only from what the
// server reports. It never means a model call was tried.
type Health int

const (
	HealthAvailable Health = iota
	HealthCooling          // temporarily unavailable, with a retry time
	HealthError
	HealthDisabled
	HealthPending // pending or refreshing
)

// Health classifies the credential.
func (c Credential) Health() Health {
	switch {
	case c.Disabled || c.Status == StatusDisabled:
		return HealthDisabled
	case c.Status == StatusPending || c.Status == StatusRefreshing:
		return HealthPending
	case c.Unavailable && c.NextRetryAfter != nil && !c.NextRetryAfter.IsZero():
		return HealthCooling
	case c.Unavailable || c.Status == StatusError:
		return HealthError
	}
	return HealthAvailable
}

// CredentialList is the response of GET /credentials.
type CredentialList struct {
	ObservedAt time.Time    `json:"observed_at"`
	Files      []Credential `json:"files"`
}

// CredentialModel is one model of GET /credentials/models.
type CredentialModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	OwnedBy     string `json:"owned_by"`
}

// LogPage is the response of GET /observability/logs.
type LogPage struct {
	Lines           []string `json:"lines"`
	LineCount       int      `json:"line-count"`
	LatestTimestamp int64    `json:"latest-timestamp"`
	NextCursor      string   `json:"next-cursor"`
	CursorReset     bool     `json:"cursor-reset"`
}

// OAuthStart is the response of GET /oauth/auth-url.
type OAuthStart struct {
	Status    string `json:"status"`
	URL       string `json:"url"`
	State     string `json:"state"`
	Flow      string `json:"flow"` // "device" for device-code flows
	UserCode  string `json:"user_code"`
	ExpiresIn int    `json:"expires_in"`
}

// OAuth login status values of GET /oauth/status.
const (
	OAuthWait  = "wait"
	OAuthOK    = "ok"
	OAuthError = "error"
)

// OAuthStatus is the response of GET /oauth/status.
type OAuthStatus struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// OAuthProviders are the built-in login providers of CPA v8; plugins may
// register more.
var OAuthProviders = []string{"claude", "codex", "antigravity", "kimi", "kimi-ai", "xai", "devin", "meta"}

// Plugin is one entry of GET /plugins.
type Plugin struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	Enabled          bool   `json:"enabled"`
	EffectiveEnabled bool   `json:"effective_enabled"`
	Configured       bool   `json:"configured"`
	Registered       bool   `json:"registered"`
	SupportsOAuth    bool   `json:"supports_oauth"`
	OAuthProvider    string `json:"oauth_provider"`
	SupportsQuota    bool   `json:"supports_quota"`
	QuotaProvider    string `json:"quota_provider"`

	Raw json.RawMessage `json:"-"`
}

func (p *Plugin) UnmarshalJSON(b []byte) error {
	type plain Plugin
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Plugin(v)
	p.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// PluginList is the response of GET /plugins.
type PluginList struct {
	PluginsEnabled bool     `json:"plugins_enabled"`
	PluginsDir     string   `json:"plugins_dir"`
	Plugins        []Plugin `json:"plugins"`
}

// PluginStore is the response of GET /plugins/store. Entries are kept raw:
// the store's schema is richer than the client needs to model.
type PluginStore struct {
	PluginsEnabled bool              `json:"plugins_enabled"`
	PluginsDir     string            `json:"plugins_dir"`
	Sources        []json.RawMessage `json:"sources"`
	SourceErrors   []json.RawMessage `json:"source_errors"`
	Plugins        []json.RawMessage `json:"plugins"`
}

// ServerInfo is what each management response says about the server.
type ServerInfo struct {
	Version       string
	Commit        string
	BuildDate     string
	SupportPlugin string
}
