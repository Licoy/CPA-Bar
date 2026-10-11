package view

import (
	"net/url"
	"testing"
)

func TestLinuxProxySettings(t *testing.T) {
	settings := linuxProxySettings{
		mode:       "manual",
		httpProxy:  "http://http-proxy.example:8080",
		httpsProxy: "http://secure-proxy.example:8443",
		socksProxy: "socks5://socks-proxy.example:1080",
		bypass:     []string{"localhost", ".internal.example", "10.0.0.0/8", "api.example:443"},
	}
	tests := []struct {
		target, want string
	}{
		{"http://public.example/file", "http://http-proxy.example:8080"},
		{"https://public.example/file", "http://secure-proxy.example:8443"},
		{"https://api.internal.example/file", ""},
		{"http://10.2.3.4/file", ""},
		{"https://api.example/file", ""},
		{"http://api.example/file", "http://http-proxy.example:8080"},
	}
	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			target, _ := url.Parse(test.target)
			proxy, err := settings.proxyForURL(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := proxyString(proxy); got != test.want {
				t.Fatalf("proxy = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLinuxProxySettingsSOCKSFallbackAndReverseBypass(t *testing.T) {
	settings := linuxProxySettings{mode: "manual", socksProxy: "socks5://localhost:1080", bypass: []string{".corp.example"}, reverseBypass: true}
	for target, want := range map[string]string{
		"https://service.corp.example": "socks5://localhost:1080",
		"https://public.example":       "",
	} {
		parsed, _ := url.Parse(target)
		proxy, err := settings.proxyForURL(parsed)
		if err != nil || proxyString(proxy) != want {
			t.Fatalf("proxyForURL(%q) = %v, %v, want %q", target, proxy, err, want)
		}
	}
}

func TestGNOMEProxySettingsDefaultsAndFallback(t *testing.T) {
	disabled, err := gnomeProxyEndpoint("", "8080", "http", "http")
	if err != nil || disabled != "" {
		t.Fatalf("empty host with default port = %q, %v", disabled, err)
	}
	settings := newGNOMEProxySettings("http://proxy.example:8080", "", "socks5://localhost:1080", nil)
	target, _ := url.Parse("https://example.com")
	proxy, err := settings.proxyForURL(target)
	if err != nil || proxyString(proxy) != "http://proxy.example:8080" {
		t.Fatalf("HTTPS fallback proxy = %v, %v", proxy, err)
	}
	explicit := newGNOMEProxySettings("http://proxy.example:8080", "http://secure.example:8443", "", nil)
	proxy, err = explicit.proxyForURL(target)
	if err != nil || proxyString(proxy) != "http://secure.example:8443" {
		t.Fatalf("explicit HTTPS proxy = %v, %v", proxy, err)
	}
}

func TestLinuxProxySettingsRejectsPAC(t *testing.T) {
	target, _ := url.Parse("https://example.com")
	if _, err := (linuxProxySettings{mode: "auto"}).proxyForURL(target); err == nil {
		t.Fatal("PAC mode did not return an error")
	}
}

func TestLinuxProxyParsing(t *testing.T) {
	values, err := parseGSettingsStrings("['localhost', '.example.com', 'it\\'s.internal']")
	if err != nil || len(values) != 3 || values[2] != "it's.internal" {
		t.Fatalf("parseGSettingsStrings = %#v, %v", values, err)
	}
	empty, err := parseGSettingsStrings("@as []")
	if err != nil || len(empty) != 0 {
		t.Fatalf("parse empty GSettings list = %#v, %v", empty, err)
	}
	for _, malformed := range []string{"not-a-list", "['unterminated]", "['ok'] trailing", "['ok',]"} {
		if _, err := parseGSettingsStrings(malformed); err == nil {
			t.Fatalf("parseGSettingsStrings(%q) accepted malformed input", malformed)
		}
	}
	for raw, want := range map[string]string{
		"proxy.example 8080":      "http://proxy.example:8080",
		"http://proxy.example 80": "http://proxy.example:80",
		"socks://localhost:1080":  "socks5://localhost:1080",
		"[::1]:3128":              "http://[::1]:3128",
	} {
		got, err := linuxProxyURL(raw, "http")
		if err != nil || got != want {
			t.Fatalf("linuxProxyURL(%q) = %q, %v, want %q", raw, got, err, want)
		}
	}
}

func proxyString(proxy *url.URL) string {
	if proxy == nil {
		return ""
	}
	return proxy.String()
}
