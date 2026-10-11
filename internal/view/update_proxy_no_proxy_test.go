package view

import (
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestUpdaterNOProxyOnlyPatterns(t *testing.T) {
	tests := []struct {
		name    string
		noProxy string
		target  string
		bypass  bool
	}{
		{"wildcard", "*", "http://updates.example/file", true},
		{"CIDR", "10.0.0.0/8", "https://10.2.3.4/file", true},
		{"domain", "example.com", "https://updates.example.com/file", true},
		{"exact domain", "example.com", "https://example.com/file", true},
		{"matching port", "example.com:8080", "http://example.com:8080/file", true},
		{"different port", "example.com:8080", "http://example.com:8081/file", false},
		{"IPv6", "[2001:db8::1]", "https://[2001:db8::1]/file", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepareUpdaterProxyTest(t)
			t.Setenv("NO_PROXY", test.noProxy)
			var calls atomic.Int32
			systemProxy, _ := url.Parse("http://system-proxy.example:8080")
			updaterSystemProxy = func(*url.URL) (*url.URL, error) {
				calls.Add(1)
				return systemProxy, nil
			}
			req, _ := http.NewRequest(http.MethodGet, test.target, nil)
			got, err := updaterProxyForRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			if test.bypass {
				if got != nil || calls.Load() != 0 {
					t.Fatalf("proxy = %v, system calls = %d", got, calls.Load())
				}
				return
			}
			if got == nil || got.String() != systemProxy.String() || calls.Load() != 1 {
				t.Fatalf("proxy = %v, system calls = %d", got, calls.Load())
			}
		})
	}
}

func TestUpdaterEnvironmentNOProxyBypassTakesPriority(t *testing.T) {
	prepareUpdaterProxyTest(t)
	t.Setenv("HTTP_PROXY", "http://environment-proxy.example:8080")
	updaterEnvironmentProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	updaterSystemProxy = func(*url.URL) (*url.URL, error) {
		return nil, errors.New("system proxy must not be read after environment NO_PROXY matched")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://bypass.example/file", nil)
	proxy, err := updaterProxyForRequest(req)
	if err != nil || proxy != nil {
		t.Fatalf("proxy = %v, error = %v", proxy, err)
	}
}
