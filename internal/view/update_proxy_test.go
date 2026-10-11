package view

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigureUpdaterHTTPUsesSystemProxy(t *testing.T) {
	prepareUpdaterProxyTest(t)

	proxied := make(chan *http.Request, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied <- r
		_, _ = io.WriteString(w, "manifest")
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	updaterEnvironmentProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	updaterSystemProxy = func(*url.URL) (*url.URL, error) { return proxyURL, nil }
	http.DefaultClient = &http.Client{Timeout: 3 * time.Second}

	if err := configureUpdaterHTTP(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Get("http://updates.example/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if http.DefaultClient.Timeout != 3*time.Second {
		t.Fatalf("client timeout = %v", http.DefaultClient.Timeout)
	}
	select {
	case req := <-proxied:
		if req.URL.String() != "http://updates.example/manifest.json" {
			t.Fatalf("proxy request URL = %q", req.URL)
		}
	default:
		t.Fatal("request did not pass through the system proxy")
	}
}

func TestUpdaterSystemProxyHTTPSConnect(t *testing.T) {
	prepareUpdaterProxyTest(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "release")
	}))
	defer target.Close()
	connected := make(chan struct{}, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method = %s", r.Method)
			return
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		connected <- struct{}{}
		go func() { _, _ = io.Copy(upstream, client) }()
		_, _ = io.Copy(client, upstream)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)

	http.DefaultClient = target.Client()
	updaterSystemProxy = func(*url.URL) (*url.URL, error) { return proxyURL, nil }
	if err := configureUpdaterHTTP(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-connected:
	default:
		t.Fatal("HTTPS request did not use CONNECT")
	}
}

func TestUpdaterSystemProxySOCKS(t *testing.T) {
	prepareUpdaterProxyTest(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "package")
	}))
	defer target.Close()
	proxyURL, used := newSOCKSProxy(t)
	updaterSystemProxy = func(*url.URL) (*url.URL, error) { return proxyURL, nil }

	if err := configureUpdaterHTTP(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-used:
	default:
		t.Fatal("request did not use SOCKS")
	}
}

func TestUpdaterProxyBypassAndDynamicSettings(t *testing.T) {
	t.Run("NO_PROXY bypass", func(t *testing.T) {
		prepareUpdaterProxyTest(t)
		t.Setenv("NO_PROXY", "127.0.0.1")
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		defer target.Close()
		var systemCalls atomic.Int32
		updaterSystemProxy = func(*url.URL) (*url.URL, error) {
			systemCalls.Add(1)
			return nil, errors.New("must not be called")
		}
		if err := configureUpdaterHTTP(); err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Get(target.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if systemCalls.Load() != 0 {
			t.Fatal("system proxy was resolved for a NO_PROXY target")
		}
	})

	t.Run("settings change between requests", func(t *testing.T) {
		prepareUpdaterProxyTest(t)
		first, firstURL := labeledHTTPProxy(t, "first")
		defer first.Close()
		second, secondURL := labeledHTTPProxy(t, "second")
		defer second.Close()
		var calls atomic.Int32
		updaterSystemProxy = func(*url.URL) (*url.URL, error) {
			if calls.Add(1) == 1 {
				return firstURL, nil
			}
			return secondURL, nil
		}
		if err := configureUpdaterHTTP(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"first", "second"} {
			resp, err := http.DefaultClient.Get("http://updates.example/file")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != want {
				t.Fatalf("proxy response = %q, want %q", body, want)
			}
		}
	})
}

func TestUpdaterProxyErrorsAreVisible(t *testing.T) {
	prepareUpdaterProxyTest(t)
	updaterSystemProxy = func(*url.URL) (*url.URL, error) { return nil, errors.New("proxy settings unavailable") }
	if err := configureUpdaterHTTP(); err != nil {
		t.Fatal(err)
	}
	_, err := http.DefaultClient.Get("http://updates.example/manifest")
	if err == nil || !strings.Contains(err.Error(), "proxy settings unavailable") {
		t.Fatalf("request error = %v", err)
	}
}

func TestUpdaterEnvironmentProxyTakesPriority(t *testing.T) {
	prepareUpdaterProxyTest(t)
	proxy, proxyURL := labeledHTTPProxy(t, "environment")
	defer proxy.Close()
	updaterEnvironmentProxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	updaterSystemProxy = func(*url.URL) (*url.URL, error) {
		return nil, errors.New("system proxy must not be read")
	}
	if err := configureUpdaterHTTP(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Get("http://updates.example/manifest")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "environment" {
		t.Fatalf("proxy response = %q", body)
	}
}

func TestConfigureUpdaterHTTPRejectsUnknownTransport(t *testing.T) {
	prepareUpdaterProxyTest(t)
	http.DefaultClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })}
	if err := configureUpdaterHTTP(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("configure error = %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func prepareUpdaterProxyTest(t *testing.T) {
	t.Helper()
	clearUpdaterProxyEnvironment(t)
	oldClient := http.DefaultClient
	oldEnvironmentProxy := updaterEnvironmentProxy
	oldSystemProxy := updaterSystemProxy
	t.Cleanup(func() {
		http.DefaultClient = oldClient
		updaterEnvironmentProxy = oldEnvironmentProxy
		updaterSystemProxy = oldSystemProxy
	})
	http.DefaultClient = &http.Client{}
	updaterEnvironmentProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
}

func clearUpdaterProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
}

func labeledHTTPProxy(t *testing.T, label string) (*httptest.Server, *url.URL) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, label) }))
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server, proxyURL
}
