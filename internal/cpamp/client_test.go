package cpamp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
