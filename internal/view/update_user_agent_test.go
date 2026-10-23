package view

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestUpdaterUserAgent(t *testing.T) {
	prepareUpdaterProxyTest(t)
	originalVersion := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = originalVersion })
	updaterSystemProxy = func(*url.URL) (*url.URL, error) { return nil, nil }

	agents := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents <- r.UserAgent()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/package", http.StatusFound)
		}
	}))
	defer server.Close()

	base := &http.Transport{MaxIdleConns: 7}
	http.DefaultClient = &http.Client{Transport: base, Timeout: 3 * time.Second}
	// Reconfiguration must unwrap our transport, preserving the client's options.
	for range 2 {
		if err := configureUpdaterHTTP(); err != nil {
			t.Fatal(err)
		}
	}
	defer http.DefaultClient.CloseIdleConnections()
	configured := http.DefaultClient.Transport.(*updaterTransport)
	if configured.Transport == base || configured.MaxIdleConns != 7 || http.DefaultClient.Timeout != 3*time.Second {
		t.Fatal("configuration did not isolate the transport or preserve client options")
	}

	for _, test := range []struct {
		path, agent string
		want        []string
	}{
		{"/manifest", "MyGo/0.6.2 CPA-Bar/1.2.3", []string{"cpa-bar/1.2.3"}},
		{"/redirect", "MyGo/0.6.2 CPA-Bar/1.2.3", []string{"cpa-bar/1.2.3", "cpa-bar/1.2.3"}},
		{"/other", "custom-client/2", []string{"custom-client/2"}},
		{"/other", "", []string{"Go-http-client/1.1"}},
	} {
		req, err := http.NewRequest(http.MethodGet, server.URL+test.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if test.agent != "" {
			req.Header.Set("User-Agent", test.agent)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.UserAgent() != test.agent {
			t.Fatalf("input request UA changed to %q", req.UserAgent())
		}
		var got []string
		for range test.want {
			got = append(got, <-agents)
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("%s (%q): UAs = %q, want %q", test.path, test.agent, got, test.want)
		}
	}
}
