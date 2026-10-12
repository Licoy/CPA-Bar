package cpamp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/licoy/cpa-bar/internal/cpamp"
	"github.com/licoy/cpa-bar/internal/cpamp/cpamptest"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:18317":                         "http://127.0.0.1:18317",
		"https://mp.example.com/":                 "https://mp.example.com",
		"http://h:18317/management.html":          "http://h:18317",
		"https://h/cpamp/v0/management/usage":     "https://h/cpamp",
		"http://h:18317/usage-service/info?x=1#y": "http://h:18317",
	} {
		if got, err := cpamp.NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://h", "http://u:p@h"} {
		if _, err := cpamp.NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) took it", bad)
		}
	}
}

func TestDetectAndProbe(t *testing.T) {
	srv := cpamptest.New(t, "cpamp_good")
	ctx := context.Background()
	c, _ := cpamp.New(cpamp.Options{BaseURL: srv.URL + "/management.html", AdminKey: "cpamp_good"})
	if err := c.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	bad, _ := cpamp.New(cpamp.Options{BaseURL: srv.URL, AdminKey: "cpamp_bad"})
	if err := bad.Probe(ctx); !cpamp.IsUnauthorized(err) {
		t.Errorf("a wrong key gives %v", err)
	}

	// CPA answers 404 on /health.
	cpa := httptest.NewServer(http.NotFoundHandler())
	defer cpa.Close()
	other, _ := cpamp.New(cpamp.Options{BaseURL: cpa.URL})
	if err := other.Detect(ctx); !errors.Is(err, cpamp.ErrNotManagerPlus) {
		t.Errorf("CPA detected as %v", err)
	}
}

func TestRedirectAuthorizationPolicy(t *testing.T) {
	for _, tt := range []struct {
		name      string
		forward   bool
		crossHost bool
		want      string
	}{
		{name: "default strips authorization"},
		{name: "enabled forwards authorization across hosts", forward: true, crossHost: true, want: "Bearer admin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotMiddle, gotDestination string
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotDestination = r.Header.Get("Authorization")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer destination.Close()
			middle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMiddle = r.Header.Get("Authorization")
				http.Redirect(w, r, destination.URL, http.StatusFound)
			}))
			defer middle.Close()

			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, middle.URL, http.StatusFound)
			}))
			defer origin.Close()
			base := origin.URL
			if tt.crossHost {
				base = strings.Replace(base, "127.0.0.1", "localhost", 1)
			}
			c, err := cpamp.New(cpamp.Options{
				BaseURL:               base,
				AdminKey:              "admin",
				ForwardAuthOnRedirect: tt.forward,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			if gotMiddle != tt.want || gotDestination != tt.want {
				t.Errorf("redirected Authorization = [%q, %q], want %q on every hop", gotMiddle, gotDestination, tt.want)
			}
		})
	}
}

func TestRedirectRejectsHTTPSDowngrade(t *testing.T) {
	requests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer origin.Close()

	for _, tt := range []struct {
		name    string
		forward bool
	}{
		{name: "default"},
		{name: "forward authorization enabled", forward: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := cpamp.New(cpamp.Options{
				BaseURL:               origin.URL,
				AdminKey:              "admin",
				ForwardAuthOnRedirect: tt.forward,
				Transport:             origin.Client().Transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Status(context.Background())
			if err == nil || !strings.Contains(err.Error(), "refusing redirect from HTTPS to HTTP") {
				t.Fatalf("downgrade error = %v", err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP destination received %d requests", requests)
	}
}

func TestRedirectLimit(t *testing.T) {
	requests := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer srv.Close()

	c, err := cpamp.New(cpamp.Options{BaseURL: srv.URL, AdminKey: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect loop error = %v", err)
	}
	if requests != 10 {
		t.Fatalf("server received %d requests, want 10", requests)
	}
}

// Events pages through, newest first, within the range.
func TestEventsPages(t *testing.T) {
	srv := cpamptest.New(t, "k")
	now := time.Now()
	for i := range 5 {
		srv.Add(map[string]any{"request_id": string(rune('a' + i)), "timestamp_ms": int(now.Add(time.Duration(i) * time.Second).UnixMilli())})
	}
	c, _ := cpamp.New(cpamp.Options{BaseURL: srv.URL, AdminKey: "k"})
	var ids []string
	var bms, bid int64
	for {
		p, err := c.Events(context.Background(), now.Add(-time.Hour), now.Add(time.Hour), bms, bid, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Items {
			ids = append(ids, e.RequestID)
		}
		if !p.HasMore {
			break
		}
		bms, bid = p.NextBeforeMS, p.NextBeforeID
	}
	if got := len(ids); got != 5 || ids[0] != "e" || ids[4] != "a" {
		t.Errorf("events %q", ids)
	}
}
