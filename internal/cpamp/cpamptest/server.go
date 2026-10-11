// Package cpamptest is a fake CPA-Manager-Plus server for tests.
package cpamptest

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// Server answers the routes CPA-Bar reads, with the events it is given.
type Server struct {
	*httptest.Server
	Key        string
	CPAAddress string

	mu     sync.Mutex
	events []map[string]any // newest first
	paths  []string
}

// New starts a server taking key as its admin key.
func New(t testing.TB, key string) *Server {
	s := &Server{Key: key}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Add puts events in front, newest first.
func (s *Server) Add(events ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(slices.Clone(events), s.events...)
}

// Paths are the paths requested, in order.
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/health" {
		_, _ = w.Write([]byte(`{"ok":true,"service":"cpa-manager-plus"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid admin key","code":"unauthorized"}`))
		return
	}
	switch r.URL.Path {
	case "/status":
		_, _ = w.Write([]byte(`{"events":1,"collector":{"mode":"auto","transport":"subscribe"}}`))
	case "/usage-service/config":
		_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"cpaConnection": map[string]any{"cpaBaseUrl": s.CPAAddress, "managementKeyConfigured": true}}})
	case "/v0/management/monitoring/analytics":
		var req struct {
			FromMS  int64 `json:"from_ms"`
			ToMS    int64 `json:"to_ms"`
			Include struct {
				EventsPage *struct {
					Limit    int    `json:"limit"`
					BeforeMS *int64 `json:"before_ms"`
					BeforeID *int64 `json:"before_id"`
				} `json:"events_page"`
			} `json:"include"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FromMS >= req.ToMS {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		var in []map[string]any
		for i, e := range s.events {
			ts := int64(e["timestamp_ms"].(int))
			if ts < req.FromMS || ts > req.ToMS {
				continue
			}
			p := req.Include.EventsPage
			if p != nil && p.BeforeID != nil && int64(i) <= *p.BeforeID {
				continue
			}
			e = maps.Clone(e)
			e["_i"] = i
			in = append(in, e)
		}
		s.mu.Unlock()
		limit := req.Include.EventsPage.Limit
		page := map[string]any{"items": []any{}, "has_more": false}
		if len(in) > limit {
			last := in[limit-1]
			page["has_more"] = true
			page["next_before_ms"] = last["timestamp_ms"]
			page["next_before_id"] = last["_i"]
			in = in[:limit]
		}
		items := make([]any, len(in))
		for i, e := range in {
			delete(e, "_i")
			items[i] = e
		}
		page["items"] = items
		_ = json.NewEncoder(w).Encode(map[string]any{"generated_at_ms": 1, "events": page})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found"))
	}
}
