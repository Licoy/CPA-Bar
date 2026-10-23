package view

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

var (
	updaterEnvironmentProxy = http.ProxyFromEnvironment
	updaterSystemProxy      = systemProxyForURL
)

// configureUpdaterHTTP gives mygo's updater an isolated transport. mygo
// currently makes its manifest, release and package requests through
// http.DefaultClient and does not expose a client option.
func configureUpdaterHTTP() error {
	client := *http.DefaultClient
	var transport *http.Transport
	switch current := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport)
	case *http.Transport:
		transport = current
	case *updaterTransport:
		transport = current.Transport
	default:
		return fmt.Errorf("unsupported default HTTP transport %T", current)
	}
	transport = transport.Clone()
	transport.Proxy = updaterProxyForRequest
	client.Transport = &updaterTransport{Transport: transport, userAgent: "cpa-bar/" + Version}
	http.DefaultClient = &client
	return nil
}

// updaterTransport replaces the framework updater's UA without modifying
// requests from other users of http.DefaultClient.
type updaterTransport struct {
	*http.Transport
	userAgent string
}

func (t *updaterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.UserAgent(), "MyGo/") {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.userAgent)
	}
	return t.Transport.RoundTrip(req)
}

func updaterProxyForRequest(req *http.Request) (*url.URL, error) {
	proxy, err := updaterEnvironmentProxy(req)
	if err != nil || proxy != nil || environmentProxyConfigured(req.URL.Scheme) {
		return proxy, err
	}
	if noProxyMatch(req.URL) {
		return nil, nil
	}
	return updaterSystemProxy(req.URL)
}

func environmentProxyConfigured(scheme string) bool {
	names := []string{"HTTP_PROXY", "http_proxy"}
	if scheme == "https" {
		names = []string{"HTTPS_PROXY", "https_proxy"}
	}
	for _, name := range names {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// noProxyMatch covers the special case where NO_PROXY is set without an
// environment proxy. When an environment proxy exists, net/http already
// applies the complete NO_PROXY behavior for us.
func noProxyMatch(target *url.URL) bool {
	value := os.Getenv("NO_PROXY")
	if value == "" {
		value = os.Getenv("no_proxy")
	}
	host := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[target.Scheme]
	}
	ip, isIP := netip.ParseAddr(host)
	for _, raw := range strings.Split(value, ",") {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil && isIP == nil && prefix.Contains(ip) {
			return true
		}
		entryHost, entryPort := splitProxyHostPort(entry)
		if entryPort != "" && entryPort != port {
			continue
		}
		if parsed, err := netip.ParseAddr(entryHost); err == nil {
			if isIP == nil && parsed == ip {
				return true
			}
			continue
		}
		matchHost := !strings.HasPrefix(entryHost, ".") && !strings.HasPrefix(entryHost, "*.")
		entryHost = strings.TrimPrefix(entryHost, "*")
		if !strings.HasPrefix(entryHost, ".") {
			entryHost = "." + entryHost
		}
		if strings.HasSuffix(host, entryHost) || matchHost && host == entryHost[1:] {
			return true
		}
	}
	return false
}

func splitProxyHostPort(value string) (string, string) {
	if host, port, err := net.SplitHostPort(value); err == nil {
		return strings.Trim(host, "[]"), port
	}
	return strings.Trim(value, "[]"), ""
}
