package view

import (
	"net/url"
	"testing"
)

func TestProxyFromWindowsSettingsSelectsProxy(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		settings windowsProxySettings
		want     string
	}{
		{name: "direct", target: "https://example.com", settings: windowsProxySettings{}, want: ""},
		{name: "HTTP", target: "http://example.com", settings: windowsProxySettings{proxy: "http=proxy.local:8080;https=secure.local:8443"}, want: "http://proxy.local:8080"},
		{name: "HTTPS CONNECT", target: "https://example.com", settings: windowsProxySettings{proxy: "http=proxy.local:8080;https=https://secure.local:8443"}, want: "http://secure.local:8443"},
		{name: "default", target: "https://example.com", settings: windowsProxySettings{proxy: "proxy.local:3128;ignored.local:8080"}, want: "http://proxy.local:3128"},
		{name: "SOCKS", target: "https://example.com", settings: windowsProxySettings{proxy: "socks=socks.local:1080"}, want: "socks5://socks.local:1080"},
		{name: "default SOCKS", target: "https://example.com", settings: windowsProxySettings{proxy: "socks5://socks.local:1080"}, want: "socks5://socks.local:1080"},
		{name: "manual with auto-detect", target: "https://example.com", settings: windowsProxySettings{autoDetect: true, proxy: "proxy.local:8080"}, want: "http://proxy.local:8080"},
		{name: "no matching scheme", target: "https://example.com", settings: windowsProxySettings{proxy: "http=proxy.local:8080"}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxy, err := proxyFromWindowsSettings(mustWindowsProxyURL(t, test.target), test.settings)
			if err != nil {
				t.Fatal(err)
			}
			if got := urlString(proxy); got != test.want {
				t.Fatalf("proxy = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProxyFromWindowsSettingsBypass(t *testing.T) {
	tests := []struct {
		name   string
		target string
		bypass string
		want   bool
	}{
		{name: "local", target: "https://intranet/path", bypass: "<local>", want: true},
		{name: "local with port", target: "https://intranet:8443/path", bypass: "<local>:8443", want: true},
		{name: "IP is not local name", target: "https://[::1]/path", bypass: "<local>", want: false},
		{name: "wildcard", target: "https://api.example.com", bypass: "*.example.com", want: true},
		{name: "matching port", target: "https://example.com:8443", bypass: "example.com:8443", want: true},
		{name: "different port", target: "https://example.com:443", bypass: "example.com:8443", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxy, err := proxyFromWindowsSettings(mustWindowsProxyURL(t, test.target), windowsProxySettings{
				proxy:  "proxy.local:8080",
				bypass: test.bypass,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := proxy == nil; got != test.want {
				t.Fatalf("bypassed = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProxyFromWindowsSettingsRejectsInvalidProxy(t *testing.T) {
	target := mustWindowsProxyURL(t, "https://example.com")
	for _, proxy := range []string{"ftp://proxy.local:21", "proxy.local:70000"} {
		if _, err := proxyFromWindowsSettings(target, windowsProxySettings{proxy: proxy}); err == nil {
			t.Fatalf("proxy %q did not return an error", proxy)
		}
	}
}

func mustWindowsProxyURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func urlString(value *url.URL) string {
	if value == nil {
		return ""
	}
	return value.String()
}
