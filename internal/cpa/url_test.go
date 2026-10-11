package cpa

import (
	"strings"
	"testing"
)

func TestNormalizeManagementURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"root", "http://127.0.0.1:8317", "http://127.0.0.1:8317/v8/management"},
		{"root trailing slash", "http://127.0.0.1:8317/", "http://127.0.0.1:8317/v8/management"},
		{"several trailing slashes", "https://h///", "https://h/v8/management"},
		{"https default port", "https://cpa.example.com", "https://cpa.example.com/v8/management"},
		{"surrounding whitespace", "  http://h:1  ", "http://h:1/v8/management"},
		{"reverse proxy prefix kept", "https://h/cpa/", "https://h/cpa/v8/management"},
		{"nested prefix kept", "https://h/a/b", "https://h/a/b/v8/management"},
		{"full v8 url", "http://h:1/v8/management", "http://h:1/v8/management"},
		{"full v8 url trailing slash", "http://h:1/v8/management/", "http://h:1/v8/management"},
		{"v8 sub-path", "https://h/cpa/v8/management/config", "https://h/cpa/v8/management"},
		{"v8 deep sub-path", "http://h/v8/management/credentials/status", "http://h/v8/management"},
		{"scheme-less host:port", "127.0.0.1:8317", "http://127.0.0.1:8317/v8/management"},
		{"scheme-less host", "cpa.local", "http://cpa.local/v8/management"},
		{"scheme-less with prefix", "cpa.local/proxy", "http://cpa.local/proxy/v8/management"},
		{"query dropped", "http://h:1/?key=secret", "http://h:1/v8/management"},
		{"fragment dropped", "http://h:1/#frag", "http://h:1/v8/management"},
		{"query and fragment on full url", "http://h:1/v8/management?x=1#y", "http://h:1/v8/management"},
		{"uppercase scheme is lowered by parser", "HTTP://h:1", "http://h:1/v8/management"},
		{"ipv6", "http://[::1]:8317", "http://[::1]:8317/v8/management"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeManagementURL(tc.in)
			if err != nil {
				t.Fatalf("NormalizeManagementURL(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeManagementURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Normalizing is idempotent.
			again, err := NormalizeManagementURL(got)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %q -> %q, %v", got, again, err)
			}
		})
	}
}

func TestNormalizeManagementURLRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"empty", "", "empty"},
		{"blank", "   ", "empty"},
		{"v0 management", "http://h:1/v0/management", "only /v8/management"},
		{"v0 management sub-path", "http://h:1/v0/management/config", "only /v8/management"},
		{"v1 management behind a prefix", "https://h/cpa/v1/management", "only /v8/management"},
		{"ftp", "ftp://h/", "unsupported scheme"},
		{"file", "file:///etc/passwd", "unsupported scheme"},
		{"userinfo", "http://user:pass@h:1", "credentials"},
		{"user only", "http://user@h:1", "credentials"},
		{"no host", "http://", "no host"},
		{"no host with path", "http:///v8/management", "no host"},
		{"invalid port", "http://h:notaport", "invalid address"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeManagementURL(tc.in)
			if err == nil {
				t.Fatalf("NormalizeManagementURL(%q) = %q, want error", tc.in, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRootURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://h:1/v8/management", "http://h:1"},
		{"https://h/cpa/v8/management", "https://h/cpa"},
		{"http://h:1", "http://h:1"},
	}
	for _, tc := range tests {
		if got := RootURL(tc.in); got != tc.want {
			t.Errorf("RootURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// RootURL inverts NormalizeManagementURL for a prefixed address.
	base, _ := NormalizeManagementURL("https://h/cpa/")
	if got := RootURL(base); got != "https://h/cpa" {
		t.Errorf("RootURL(%q) = %q", base, got)
	}
}
