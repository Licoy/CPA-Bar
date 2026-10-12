package cpa

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

// rec is what the server saw of one request.
type rec struct {
	Method string
	Path   string // escaped, as sent
	Query  string // raw query
	Header http.Header
	Body   string
}

type recorder struct {
	mu   sync.Mutex
	reqs []rec
}

func (r *recorder) add(req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, rec{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Clone(), string(b)})
}

func (r *recorder) all() []rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rec(nil), r.reqs...)
}

func (r *recorder) only(t *testing.T) rec {
	t.Helper()
	all := r.all()
	if len(all) != 1 {
		t.Fatalf("server saw %d requests, want 1: %+v", len(all), all)
	}
	return all[0]
}

// serve starts a server whose handler first records the request.
func serve(t *testing.T, h http.HandlerFunc) (*httptest.Server, *recorder) {
	t.Helper()
	rc := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc.add(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rc
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func newClient(t *testing.T, srv *httptest.Server, mut ...func(*Options)) *Client {
	t.Helper()
	o := Options{InstanceID: "id-1", InstanceName: "Main", BaseURL: srv.URL, ManagementKey: "k3y"}
	for _, m := range mut {
		m(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func listErr(c *Client) error {
	_, err := c.ListCredentials(bg)
	return err
}

func disableErr(c *Client) error { return c.SetCredentialDisabled(bg, "a.json", "0", true) }

func apiErr(t *testing.T, err error) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v (%T) is not an *APIError", err, err)
	}
	return ae
}

func TestRequestHeaders(t *testing.T) {
	srv, rc := serve(t, reply(200, `{"files":[]}`))
	c := newClient(t, srv)
	if err := listErr(c); err != nil {
		t.Fatal(err)
	}
	r := rc.only(t)
	if got := r.Header.Get("Authorization"); got != "Bearer k3y" {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := r.Header.Get("User-Agent"); got != "CPA-Bar" {
		t.Errorf("User-Agent = %q", got)
	}
	if r.Method != "GET" || r.Path != "/v8/management/credentials" {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
}

func TestNoKeyNoAuthorizationHeader(t *testing.T) {
	srv, rc := serve(t, reply(200, `{}`))
	c := newClient(t, srv, func(o *Options) { o.ManagementKey = "" })
	if err := listErr(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := rc.only(t).Header["Authorization"]; ok {
		t.Error("Authorization header sent without a key")
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
		{name: "enabled forwards authorization across hosts", forward: true, crossHost: true, want: "Bearer k3y"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotMiddle, gotDestination string
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotDestination = r.Header.Get("Authorization")
				_, _ = io.WriteString(w, `{"files":[]}`)
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
			c, err := New(Options{
				BaseURL:               base,
				ManagementKey:         "k3y",
				ForwardAuthOnRedirect: tt.forward,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := listErr(c); err != nil {
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
			c, err := New(Options{
				BaseURL:               origin.URL,
				ManagementKey:         "k3y",
				ForwardAuthOnRedirect: tt.forward,
				Transport:             origin.Client().Transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = listErr(c)
			if err == nil || !strings.Contains(err.Error(), "refusing redirect from HTTPS to HTTP") {
				t.Fatalf("downgrade error = %v", err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP destination received %d requests", requests)
	}
}

func TestRedirectRejectsURLCredentials(t *testing.T) {
	requests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer destination.Close()
	redirectURL := strings.Replace(destination.URL, "://", "://user:password@", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectURL, http.StatusFound)
	}))
	defer origin.Close()

	for _, forward := range []bool{false, true} {
		c := newClient(t, origin, func(o *Options) { o.ForwardAuthOnRedirect = forward })
		err := listErr(c)
		if err == nil || !strings.Contains(err.Error(), "refusing redirect URL with credentials") {
			t.Fatalf("ForwardAuthOnRedirect=%t: userinfo redirect error = %v", forward, err)
		}
	}
	if requests != 0 {
		t.Fatalf("credentialed destination received %d requests", requests)
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

	err := listErr(newClient(t, srv))
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect loop error = %v", err)
	}
	if requests != 10 {
		t.Fatalf("server received %d requests, want 10", requests)
	}
}

func TestBaseURLWithProxyPrefix(t *testing.T) {
	srv, rc := serve(t, reply(200, `{}`))
	c := newClient(t, srv, func(o *Options) { o.BaseURL = srv.URL + "/cpa/" })
	if got, want := c.BaseURL(), srv.URL+"/cpa/v8/management"; got != want {
		t.Fatalf("BaseURL = %q, want %q", got, want)
	}
	if err := listErr(c); err != nil {
		t.Fatal(err)
	}
	if got := rc.only(t).Path; got != "/cpa/v8/management/credentials" {
		t.Errorf("path = %q", got)
	}
	if c.InstanceID() != "id-1" {
		t.Errorf("InstanceID = %q", c.InstanceID())
	}
}

func TestListCredentialsDecodesAndKeepsUnknownFields(t *testing.T) {
	const body = `{
	  "observed_at": "2026-01-02T03:04:05Z",
	  "future_top_level": true,
	  "files": [
	    {"id":"a","auth_index":"0","name":"a.json","type":"codex","provider":"codex","label":"L",
	     "email":"e@x.io","status":"active","unavailable":true,"success":5,"failed":2,"priority":1.5,
	     "next_retry_after":"2026-01-02T04:00:00Z","last_refresh":null,
	     "quota_extra":{"windows":[1,2]},"brand_new":"x"},
	    {"id":"b","name":"b.json"}
	  ]}`
	srv, _ := serve(t, reply(200, body))
	list, err := newClient(t, srv).ListCredentials(bg)
	if err != nil {
		t.Fatal(err)
	}
	if got := list.ObservedAt.UTC().Format(time.RFC3339); got != "2026-01-02T03:04:05Z" {
		t.Errorf("ObservedAt = %s", got)
	}
	if len(list.Files) != 2 {
		t.Fatalf("got %d files", len(list.Files))
	}
	a := list.Files[0]
	if a.ID != "a" || a.AuthIndex != "0" || a.Name != "a.json" || a.Provider != "codex" || a.Email != "e@x.io" ||
		a.Success != 5 || a.Failed != 2 || !a.Unavailable || a.Priority == nil || *a.Priority != 1.5 ||
		a.NextRetryAfter == nil || a.LastRefresh != nil {
		t.Errorf("decoded credential = %+v", a)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(a.Raw, &raw); err != nil {
		t.Fatalf("Raw is not JSON: %v", err)
	}
	for _, k := range []string{"brand_new", "quota_extra", "email"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("Raw lost field %q: %s", k, a.Raw)
		}
	}
	if string(raw["quota_extra"]) != `{"windows":[1,2]}` {
		t.Errorf("quota_extra = %s", raw["quota_extra"])
	}
	if list.Files[1].Raw == nil || list.Files[1].Health() != HealthAvailable {
		t.Errorf("second credential = %+v", list.Files[1])
	}
}

func TestListCredentialsEmptyFilesIsNonNil(t *testing.T) {
	for _, body := range []string{`{}`, `{"files":null}`, `{"files":[]}`} {
		srv, _ := serve(t, reply(200, body))
		list, err := newClient(t, srv).ListCredentials(bg)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if list.Files == nil || len(list.Files) != 0 {
			t.Errorf("%s: Files = %#v, want empty non-nil", body, list.Files)
		}
	}
}

func TestErrorClassification(t *testing.T) {
	const partial = `{"status":"partial","deleted":1,"files":["a.json"],"failed":[` +
		`{"name":"b.json","error":"permission denied"},{"name":"c.json","error":"busy"}]}`
	deleteErr := func(c *Client) error { return c.DeleteCredentials(bg, "a.json", "b.json", "c.json") }
	configErr := func(c *Client) error { _, err := c.GetConfig(bg); return err }
	configNodeErr := func(c *Client) error { _, err := c.GetConfig(bg, "routing"); return err }

	tests := []struct {
		name    string
		call    func(*Client) error
		status  int
		body    string
		want    Kind
		msgHas  []string
		isAuth  bool
		wantMsg string // exact, when set
	}{
		{name: "401 json", call: listErr, status: 401, body: `{"error":"missing management key"}`, want: KindUnauthorized, isAuth: true, wantMsg: "missing management key"},
		{name: "401 empty", call: listErr, status: 401, want: KindUnauthorized, isAuth: true},
		{name: "403", call: listErr, status: 403, body: `{"error":"IP banned due to too many failed attempts"}`, want: KindForbidden, isAuth: true, msgHas: []string{"banned"}},
		{name: "404 empty body is management disabled", call: listErr, status: 404, want: KindManagementUnavailable},
		{name: "404 plain text on non-resource route", call: listErr, status: 404, body: "404 page not found", want: KindManagementUnavailable},
		{name: "404 json on non-resource route", call: listErr, status: 404, body: `{"error":"x"}`, want: KindManagementUnavailable},
		{name: "404 json on resource route", call: disableErr, status: 404, body: `{"error":"auth file not found"}`, want: KindNotFound, wantMsg: "auth file not found"},
		{name: "404 plain text on resource route", call: disableErr, status: 404, body: "404 page not found", want: KindManagementUnavailable},
		{name: "404 empty on resource route", call: disableErr, status: 404, want: KindManagementUnavailable},
		{name: "404 json without error key on resource route", call: disableErr, status: 404, body: `{"detail":"x"}`, want: KindManagementUnavailable},
		{name: "404 on whole config is not a resource", call: configErr, status: 404, body: `{"error":"x"}`, want: KindManagementUnavailable},
		{name: "404 on config node is a resource", call: configNodeErr, status: 404, body: `{"error":"no such field"}`, want: KindNotFound},
		{name: "429", call: listErr, status: 429, body: `{"error":"too many requests"}`, want: KindRateLimited},
		{name: "400 slug and message", call: disableErr, status: 400, body: `{"error":"slug","message":"m"}`, want: KindInvalid, msgHas: []string{"slug", "m"}},
		{name: "400 error only", call: disableErr, status: 400, body: `{"error":"bad name"}`, want: KindInvalid, wantMsg: "bad name"},
		{name: "400 message only", call: disableErr, status: 400, body: `{"message":"only message"}`, want: KindInvalid, wantMsg: "only message"},
		{name: "400 nested error object", call: disableErr, status: 400, body: `{"error":{"message":"nested"}}`, want: KindInvalid, wantMsg: "nested"},
		{name: "422", call: disableErr, status: 422, body: `{"error":"unprocessable"}`, want: KindInvalid},
		{name: "409", call: disableErr, status: 409, body: `{"error":"conflict"}`, want: KindConflict},
		{name: "500", call: listErr, status: 500, body: `{"error":"boom"}`, want: KindServer, wantMsg: "boom"},
		{name: "502 html", call: listErr, status: 502, body: "<html>Bad Gateway</html>", want: KindServer, wantMsg: "<html>Bad Gateway</html>"},
		{name: "503 empty", call: listErr, status: 503, want: KindServer},
		{name: "207 partial delete", call: deleteErr, status: 207, body: partial, want: KindConflict, msgHas: []string{"b.json: permission denied", "c.json: busy"}},
		{name: "207 without detail", call: deleteErr, status: 207, body: `{}`, want: KindConflict, msgHas: []string{"partially failed"}},
		{name: "invalid json", call: listErr, status: 200, body: `{not json`, want: KindDecode, msgHas: []string{"invalid JSON"}},
		{name: "empty 200 body for a typed response", call: listErr, status: 200, want: KindDecode},
		{name: "wrong json type", call: listErr, status: 200, body: `{"files":"nope"}`, want: KindDecode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, reply(tc.status, tc.body))
			err := tc.call(newClient(t, srv))
			ae := apiErr(t, err)
			if ae.Kind != tc.want {
				t.Errorf("Kind = %v, want %v (err: %v)", ae.Kind, tc.want, err)
			}
			if KindOf(err) != tc.want {
				t.Errorf("KindOf = %v, want %v", KindOf(err), tc.want)
			}
			if ae.Status != tc.status {
				t.Errorf("Status = %d, want %d", ae.Status, tc.status)
			}
			if IsAuthError(err) != tc.isAuth || ae.IsAuth() != tc.isAuth {
				t.Errorf("IsAuthError = %v, want %v", IsAuthError(err), tc.isAuth)
			}
			if tc.wantMsg != "" && ae.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", ae.Message, tc.wantMsg)
			}
			for _, s := range tc.msgHas {
				if !strings.Contains(ae.Message, s) {
					t.Errorf("Message %q does not contain %q", ae.Message, s)
				}
			}
			if ae.InstanceID != "id-1" || ae.InstanceName != "Main" || ae.Op == "" || ae.Method == "" || ae.Path == "" {
				t.Errorf("APIError metadata incomplete: %+v", ae)
			}
		})
	}
}

func TestErrorKindOfNonAPIError(t *testing.T) {
	if KindOf(errors.New("x")) != KindUnknown || KindOf(nil) != KindUnknown {
		t.Error("KindOf of a plain error must be KindUnknown")
	}
	if IsAuthError(errors.New("x")) || IsAuthError(nil) {
		t.Error("IsAuthError of a plain error must be false")
	}
}

func TestAPIErrorContext(t *testing.T) {
	srv, _ := serve(t, reply(401, `{"error":"invalid management key"}`))
	err := newClient(t, srv).SetCredentialDisabled(bg, "a.json", "0", true)
	ae := apiErr(t, err)
	if ae.InstanceID != "id-1" || ae.InstanceName != "Main" || ae.Op != "disable credential" ||
		ae.Method != "PATCH" || ae.Path != "/credentials/status" || ae.Status != 401 {
		t.Errorf("APIError = %+v", ae)
	}
	for _, want := range []string{"Main", "disable credential", "HTTP 401", "invalid management key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, missing %q", err.Error(), want)
		}
	}
	// A wrapped error is still found, and Unwrap exposes the cause.
	wrapped := errors.Join(errors.New("ctx"), err)
	if !IsAuthError(wrapped) || KindOf(wrapped) != KindUnauthorized {
		t.Error("wrapped APIError not recognised")
	}
	if got := (&APIError{InstanceID: "only-id", Op: "x"}).Error(); got != "only-id: x" {
		t.Errorf("Error() without a name = %q", got)
	}
	cause := errors.New("cause")
	if !errors.Is(&APIError{Op: "x", Err: cause}, cause) {
		t.Error("Unwrap does not expose Err")
	}
}

func TestLongErrorBodyIsTruncated(t *testing.T) {
	srv, _ := serve(t, reply(502, strings.Repeat("x", 5000)))
	ae := apiErr(t, listErr(newClient(t, srv)))
	if n := len([]rune(ae.Message)); n > 301 {
		t.Errorf("message has %d runes, want at most 301", n)
	}
}

func TestKindString(t *testing.T) {
	seen := map[string]Kind{}
	for k := KindUnknown; k <= KindCanceled; k++ {
		s := k.String()
		if s == "" {
			t.Errorf("Kind %d has an empty name", k)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("Kind %d and %d share the name %q", prev, k, s)
		}
		seen[s] = k
	}
}

func TestServerInfoFromHeaders(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-VERSION", "8.0.23")
		w.Header().Set("X-CPA-COMMIT", "abc123")
		w.Header().Set("X-CPA-BUILD-DATE", "2026-05-01")
		w.Header().Set("X-CPA-SUPPORT-PLUGIN", "true")
		w.WriteHeader(401) // recorded even when the call fails
	})
	c := newClient(t, srv)
	if got := c.ServerInfo(); got != (ServerInfo{}) {
		t.Fatalf("ServerInfo before any call = %+v", got)
	}
	_ = listErr(c)
	want := ServerInfo{Version: "8.0.23", Commit: "abc123", BuildDate: "2026-05-01", SupportPlugin: "true"}
	if got := c.ServerInfo(); got != want {
		t.Errorf("ServerInfo = %+v, want %+v", got, want)
	}

	// A response without the header leaves what was known.
	srv2, _ := serve(t, reply(200, `{}`))
	c2 := newClient(t, srv2)
	_ = listErr(c2)
	if got := c2.ServerInfo(); got != (ServerInfo{}) {
		t.Errorf("ServerInfo without headers = %+v", got)
	}
}

// ---- transport failures ----

func TestReadTimeout(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	c := newClient(t, srv, func(o *Options) { o.ReadTimeout = 50 * time.Millisecond })
	ae := apiErr(t, listErr(c))
	if ae.Kind != KindTimeout || ae.Status != 0 {
		t.Errorf("got Kind=%v Status=%d, want timeout/0 (%v)", ae.Kind, ae.Status, ae)
	}
	if !errors.Is(ae, context.DeadlineExceeded) {
		t.Errorf("cause is not DeadlineExceeded: %v", ae.Err)
	}
}

func TestWriteTimeoutAfterServerReceivedIsIndeterminate(t *testing.T) {
	received := make(chan struct{})
	var once sync.Once
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(received) })
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	c := newClient(t, srv, func(o *Options) { o.WriteTimeout = 300 * time.Millisecond })
	ae := apiErr(t, disableErr(c))
	if ae.Kind != KindIndeterminate {
		t.Errorf("Kind = %v, want indeterminate", ae.Kind)
	}
	select {
	case <-received:
	default:
		t.Error("the server never received the request")
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := newClient(t, srv)
	srv.Close()

	if k := apiErr(t, listErr(c)).Kind; k != KindNetwork {
		t.Errorf("read: Kind = %v, want network", k)
	}
	// Nothing was sent, so even a write is a plain network failure.
	if k := apiErr(t, disableErr(c)).Kind; k != KindNetwork {
		t.Errorf("write: Kind = %v, want network", k)
	}
}

func TestConnectionDroppedMidRequest(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	c := newClient(t, srv)
	if k := apiErr(t, listErr(c)).Kind; k != KindNetwork {
		t.Errorf("read: Kind = %v, want network", k)
	}
	if k := apiErr(t, disableErr(c)).Kind; k != KindIndeterminate {
		t.Errorf("write: Kind = %v, want indeterminate", k)
	}
}

func TestCanceledContext(t *testing.T) {
	t.Run("before the call", func(t *testing.T) {
		srv, rc := serve(t, reply(200, `{}`))
		ctx, cancel := context.WithCancel(bg)
		cancel()
		c := newClient(t, srv)
		_, err := c.ListCredentials(ctx)
		if k := apiErr(t, err).Kind; k != KindCanceled {
			t.Errorf("read: Kind = %v, want canceled", k)
		}
		if k := apiErr(t, c.SetCredentialDisabled(ctx, "a", "0", true)).Kind; k != KindCanceled {
			t.Errorf("write: Kind = %v, want canceled", k)
		}
		if n := len(rc.all()); n != 0 {
			t.Errorf("server saw %d requests", n)
		}
	})
	t.Run("while in flight", func(t *testing.T) {
		started := make(chan struct{}, 2)
		srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		})
		c := newClient(t, srv)
		for name, call := range map[string]func(context.Context) error{
			"read":  func(ctx context.Context) error { _, err := c.ListCredentials(ctx); return err },
			"write": func(ctx context.Context) error { return c.SetCredentialDisabled(ctx, "a", "0", true) },
		} {
			ctx, cancel := context.WithCancel(bg)
			done := make(chan error, 1)
			go func() { done <- call(ctx) }()
			<-started
			cancel()
			if k := apiErr(t, <-done).Kind; k != KindCanceled {
				t.Errorf("%s: Kind = %v, want canceled", name, k)
			}
		}
	})
}

func TestCACert(t *testing.T) {
	srv := httptest.NewTLSServer(reply(200, `{"files":[]}`))
	defer srv.Close()

	// Without the CA the certificate is rejected: verification is never off.
	plain, err := New(Options{BaseURL: srv.URL, ManagementKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if k := apiErr(t, listErr(plain)).Kind; k != KindNetwork {
		t.Errorf("untrusted CA: Kind = %v, want network", k)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	trusted, err := New(Options{BaseURL: srv.URL, ManagementKey: "k", CACertPEM: caPEM})
	if err != nil {
		t.Fatal(err)
	}
	if err := listErr(trusted); err != nil {
		t.Errorf("trusted CA: %v", err)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	for name, o := range map[string]Options{
		"invalid CA PEM":          {BaseURL: "https://h", CACertPEM: []byte("this is not PEM")},
		"PEM block of wrong kind": {BaseURL: "https://h", CACertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")})},
		"empty address":           {BaseURL: ""},
		"v0 address":              {BaseURL: "http://h/v0/management"},
	} {
		if c, err := New(o); err == nil {
			t.Errorf("%s: New succeeded: %+v", name, c)
		}
	}
	if _, err := New(Options{BaseURL: "https://h", CACertPEM: []byte("  \n")}); err != nil {
		t.Errorf("blank CA PEM should be ignored: %v", err)
	}
}

// ---- operations ----

func TestRoutes(t *testing.T) {
	type call func(c *Client) error
	drop := func(_ any, err error) error { return err }
	tests := []struct {
		name   string
		call   call
		method string
		path   string // escaped, relative to /v8/management
		query  string
		body   string
		ctype  string
		accept string
	}{
		{name: "ListCredentials", call: func(c *Client) error { return listErr(c) }, method: "GET", path: "/credentials"},
		{name: "SetCredentialDisabled", call: disableErr, method: "PATCH", path: "/credentials/status", ctype: "application/json",
			body: `{"auth_index":"0","disabled":true,"name":"a.json"}`},
		{name: "SetCredentialDisabled enable", call: func(c *Client) error { return c.SetCredentialDisabled(bg, "a.json", "0", false) },
			method: "PATCH", path: "/credentials/status", ctype: "application/json", body: `{"auth_index":"0","disabled":false,"name":"a.json"}`},
		{name: "PatchCredentialFields", call: func(c *Client) error {
			return c.PatchCredentialFields(bg, map[string]any{"name": "a.json", "note": "hi"})
		}, method: "PATCH", path: "/credentials/fields", ctype: "application/json", body: `{"name":"a.json","note":"hi"}`},
		{name: "RefreshCredential one", call: func(c *Client) error { return c.RefreshCredential(bg, "a.json", "3") },
			method: "POST", path: "/credentials/refresh", ctype: "application/json", body: `{"auth_index":"3","name":"a.json"}`},
		{name: "RefreshCredential all", call: func(c *Client) error { return c.RefreshCredential(bg, "", "") },
			method: "POST", path: "/credentials/refresh", ctype: "application/json", body: `{"all":true}`},
		{name: "DeleteCredentials repeats name", call: func(c *Client) error { return c.DeleteCredentials(bg, "a.json", "b c.json") },
			method: "DELETE", path: "/credentials", query: "name=a.json&name=b+c.json"},
		{name: "CredentialModels", call: func(c *Client) error { return drop(c.CredentialModels(bg, "a b.json")) },
			method: "GET", path: "/credentials/models", query: "name=a+b.json"},
		{name: "DownloadCredential", call: func(c *Client) error { return drop(c.DownloadCredential(bg, "a.json")) },
			method: "GET", path: "/credentials/download", query: "name=a.json", accept: "*/*"},
		{name: "ResetCooldown", call: func(c *Client) error { return c.ResetCooldown(bg, "3") },
			method: "POST", path: "/routing/cooldown/reset", ctype: "application/json", body: `{"auth_index":"3"}`},
		{name: "GetConfig", call: func(c *Client) error { return drop(c.GetConfig(bg)) }, method: "GET", path: "/config"},
		{name: "GetConfig node", call: func(c *Client) error { return drop(c.GetConfig(bg, "routing", "retry")) },
			method: "GET", path: "/config/routing/retry"},
		{name: "PutConfig whole", call: func(c *Client) error { return c.PutConfig(bg, json.RawMessage(`{"debug":true}`)) },
			method: "PUT", path: "/config", ctype: "application/json", body: `{"debug":true}`},
		{name: "PutConfig node", call: func(c *Client) error { return c.PutConfig(bg, json.RawMessage(`"x"`), "host") },
			method: "PUT", path: "/config/host", ctype: "application/json", body: `"x"`},
		{name: "PatchConfig nested path, raw body", call: func(c *Client) error {
			return c.PatchConfig(bg, json.RawMessage(`3`), "routing", "retry", "request-retry")
		}, method: "PATCH", path: "/config/routing/retry/request-retry", ctype: "application/json", body: `3`},
		{name: "PatchConfig slash-separated and escaped", call: func(c *Client) error {
			return c.PatchConfig(bg, json.RawMessage(`{}`), "routing/retry", " a b ", "q?#", "", "/")
		}, method: "PATCH", path: "/config/routing/retry/a%20b/q%3F%23", ctype: "application/json", body: `{}`},
		{name: "PatchConfig blank path is the whole config", call: func(c *Client) error {
			return c.PatchConfig(bg, json.RawMessage(`{}`), " ", "/")
		}, method: "PATCH", path: "/config", ctype: "application/json", body: `{}`},
		{name: "DeleteConfig", call: func(c *Client) error { return c.DeleteConfig(bg, "routing", "retry") },
			method: "DELETE", path: "/config/routing/retry"},
		{name: "GetConfigYAML", call: func(c *Client) error { return drop(c.GetConfigYAML(bg)) },
			method: "GET", path: "/config.yaml", accept: "application/yaml, text/yaml, */*"},
		{name: "PutConfigYAML", call: func(c *Client) error { return c.PutConfigYAML(bg, "debug: true\n") },
			method: "PUT", path: "/config.yaml", ctype: "application/yaml", body: "debug: true\n"},
		{name: "Logs tail", call: func(c *Client) error { return drop(c.Logs(bg, "", 0)) }, method: "GET", path: "/observability/logs"},
		{name: "Logs cursor", call: func(c *Client) error { return drop(c.Logs(bg, "c1", 50)) },
			method: "GET", path: "/observability/logs", query: "cursor=c1&limit=50"},
		{name: "ClearLogs", call: func(c *Client) error { return c.ClearLogs(bg) }, method: "DELETE", path: "/observability/logs"},
		{name: "ErrorLogs", call: func(c *Client) error { return drop(c.ErrorLogs(bg)) }, method: "GET", path: "/observability/logs/errors"},
		{name: "DownloadErrorLog escapes", call: func(c *Client) error { return drop(c.DownloadErrorLog(bg, "a b.log")) },
			method: "GET", path: "/observability/logs/errors/a%20b.log", accept: "*/*"},
		{name: "RequestLog escapes slash", call: func(c *Client) error { return drop(c.RequestLog(bg, "id/1")) },
			method: "GET", path: "/observability/logs/requests/id%2F1", accept: "*/*"},
		{name: "APICall passes payload as is", call: func(c *Client) error { return drop(c.APICall(bg, json.RawMessage(`{"method":"GET"}`))) },
			method: "POST", path: "/requests/api-call", ctype: "application/json", body: `{"method":"GET"}`},
		{name: "LatestVersion", call: func(c *Client) error { return drop(c.LatestVersion(bg)) }, method: "GET", path: "/server/latest-version"},
		{name: "OAuthStatus", call: func(c *Client) error { return drop(c.OAuthStatus(bg, "s1")) },
			method: "GET", path: "/oauth/status", query: "state=s1"},
		{name: "CancelOAuth", call: func(c *Client) error { return c.CancelOAuth(bg, "s1") },
			method: "DELETE", path: "/oauth/session", query: "state=s1"},
		{name: "Plugins", call: func(c *Client) error { return drop(c.Plugins(bg)) }, method: "GET", path: "/plugins"},
		{name: "PluginStore", call: func(c *Client) error { return drop(c.PluginStore(bg)) }, method: "GET", path: "/plugins/store"},
		{name: "InstallPlugin", call: func(c *Client) error { return c.InstallPlugin(bg, "my plugin") },
			method: "POST", path: "/plugins/store/my%20plugin/install"},
		{name: "DeletePlugin", call: func(c *Client) error { return c.DeletePlugin(bg, "p1") }, method: "DELETE", path: "/plugins/p1"},
		{name: "SetPluginEnabled", call: func(c *Client) error { return c.SetPluginEnabled(bg, "p1", false) },
			method: "PATCH", path: "/config/plugins/configs/p1", ctype: "application/json", body: `{"enabled":false}`},
		{name: "PluginQuota", call: func(c *Client) error { return drop(c.PluginQuota(bg, "p1", "7")) }, method: "GET", path: "/plugins/p1/quota", query: "auth_index=7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, rc := serve(t, reply(200, `{}`))
			if err := tc.call(newClient(t, srv)); err != nil {
				t.Fatal(err)
			}
			r := rc.only(t)
			if r.Method != tc.method || r.Path != "/v8/management"+tc.path || r.Query != tc.query {
				t.Errorf("request = %s %s?%s, want %s /v8/management%s?%s", r.Method, r.Path, r.Query, tc.method, tc.path, tc.query)
			}
			if r.Body != tc.body {
				t.Errorf("body = %q, want %q", r.Body, tc.body)
			}
			if got := r.Header.Get("Content-Type"); got != tc.ctype {
				t.Errorf("Content-Type = %q, want %q", got, tc.ctype)
			}
			if tc.accept != "" && r.Header.Get("Accept") != tc.accept {
				t.Errorf("Accept = %q, want %q", r.Header.Get("Accept"), tc.accept)
			}
			if r.Header.Get("Authorization") != "Bearer k3y" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
		})
	}
}

func TestDeleteConfigRefusesWholeConfiguration(t *testing.T) {
	srv, rc := serve(t, reply(200, `{}`))
	c := newClient(t, srv)
	// No path, or only blank segments (which resolve to /config), must never
	// reach the server as a DELETE of the whole configuration.
	for _, segs := range [][]string{nil, {}, {""}, {" ", "/"}} {
		err := c.DeleteConfig(bg, segs...)
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("DeleteConfig(%q) error = %v, want a refusal", segs, err)
		}
	}
	if n := len(rc.all()); n != 0 {
		t.Errorf("server saw %d requests: %+v", n, rc.all())
	}
}

func TestDeleteCredentialsWithoutNamesSendsNothing(t *testing.T) {
	srv, rc := serve(t, reply(200, `{}`))
	if err := newClient(t, srv).DeleteCredentials(bg); err != nil {
		t.Fatal(err)
	}
	if n := len(rc.all()); n != 0 {
		t.Errorf("server saw %d requests", n)
	}
}

func TestDeleteCredentialsServerSeesRepeatedNames(t *testing.T) {
	var got []string
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()["name"]
		w.WriteHeader(200)
	})
	if err := newClient(t, srv).DeleteCredentials(bg, "a.json", "b&c.json", "d=e.json"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a.json", "b&c.json", "d=e.json"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("server parsed names %q, want %q", got, want)
	}
}

func TestUploadCredentialMultipart(t *testing.T) {
	var field, filename, content, ctype, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ctype = r.Method+" "+r.URL.Path, r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for k, fh := range r.MultipartForm.File {
			field, filename = k, fh[0].Filename
			f, _ := fh[0].Open()
			b, _ := io.ReadAll(f)
			content = string(b)
		}
	}))
	defer srv.Close()

	data := []byte(`{"type":"codex","email":"a@b.c"}`)
	if err := newClient(t, srv).UploadCredential(bg, "my cred.json", data); err != nil {
		t.Fatal(err)
	}
	if method != "POST /v8/management/credentials" {
		t.Errorf("request = %s", method)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q", ctype)
	}
	if field != "file" || filename != "my cred.json" || content != string(data) {
		t.Errorf("field=%q filename=%q content=%q", field, filename, content)
	}
}

func TestStartOAuth(t *testing.T) {
	t.Run("passes provider and extra query, returns state", func(t *testing.T) {
		srv, rc := serve(t, reply(200, `{"status":"ok","url":"https://auth.example/x","state":"s1","flow":"device","user_code":"AB-12","expires_in":600}`))
		extra := url.Values{"is_webui": {"true"}, "provider": {"ignored"}}
		out, err := newClient(t, srv).StartOAuth(bg, "codex", extra)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "s1" || out.URL != "https://auth.example/x" || out.Flow != "device" || out.UserCode != "AB-12" || out.ExpiresIn != 600 {
			t.Errorf("OAuthStart = %+v", out)
		}
		r := rc.only(t)
		q, _ := url.ParseQuery(r.Query)
		if r.Method != "GET" || !strings.HasSuffix(r.Path, "/oauth/auth-url") || q.Get("provider") != "codex" || q.Get("is_webui") != "true" {
			t.Errorf("request = %s %s?%s", r.Method, r.Path, r.Query)
		}
		if extra.Get("provider") != "ignored" {
			t.Error("the caller's url.Values was modified")
		}
	})
	t.Run("nil extra", func(t *testing.T) {
		srv, rc := serve(t, reply(200, `{"state":"s"}`))
		if _, err := newClient(t, srv).StartOAuth(bg, "claude", nil); err != nil {
			t.Fatal(err)
		}
		if got := rc.only(t).Query; got != "provider=claude" {
			t.Errorf("query = %q", got)
		}
	})
	t.Run("response without state is a decode error", func(t *testing.T) {
		srv, _ := serve(t, reply(200, `{"status":"ok","url":"https://x"}`))
		_, err := newClient(t, srv).StartOAuth(bg, "claude", nil)
		if ae := apiErr(t, err); ae.Kind != KindDecode || !strings.Contains(ae.Message, "state") {
			t.Errorf("err = %v (kind %v)", err, ae.Kind)
		}
	})
	t.Run("server error passes through", func(t *testing.T) {
		srv, _ := serve(t, reply(400, `{"error":"unsupported provider"}`))
		_, err := newClient(t, srv).StartOAuth(bg, "nope", nil)
		if ae := apiErr(t, err); ae.Kind != KindInvalid {
			t.Errorf("Kind = %v", ae.Kind)
		}
	})
}

func TestSubmitOAuthCallbackBody(t *testing.T) {
	const redirect = "http://localhost:1455/auth/callback?code=abc&state=s1"
	for _, provider := range []string{"codex", ""} {
		srv, rc := serve(t, reply(200, `{}`))
		if err := newClient(t, srv).SubmitOAuthCallback(bg, provider, redirect); err != nil {
			t.Fatal(err)
		}
		r := rc.only(t)
		var body map[string]string
		if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
			t.Fatalf("body %q: %v", r.Body, err)
		}
		if r.Method != "POST" || !strings.HasSuffix(r.Path, "/oauth/callback") || body["redirect_url"] != redirect {
			t.Errorf("request = %s %s %v", r.Method, r.Path, body)
		}
		if got, ok := body["provider"]; got != provider || (provider == "" && ok) {
			t.Errorf("provider = %q (present=%v), want %q", got, ok, provider)
		}
	}
}

func TestProbe(t *testing.T) {
	t.Run("reads config then credentials", func(t *testing.T) {
		srv, rc := serve(t, reply(200, `{"files":[]}`))
		if err := newClient(t, srv).Probe(bg); err != nil {
			t.Fatal(err)
		}
		all := rc.all()
		if len(all) != 2 || !strings.HasSuffix(all[0].Path, "/config") || !strings.HasSuffix(all[1].Path, "/credentials") {
			t.Errorf("requests = %+v", all)
		}
	})
	t.Run("stops at the first failure", func(t *testing.T) {
		srv, rc := serve(t, reply(401, `{"error":"invalid management key"}`))
		err := newClient(t, srv).Probe(bg)
		if !IsAuthError(err) {
			t.Errorf("err = %v", err)
		}
		if n := len(rc.all()); n != 1 {
			t.Errorf("server saw %d requests, want 1", n)
		}
	})
	t.Run("credentials failure surfaces", func(t *testing.T) {
		srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/credentials") {
				w.WriteHeader(500)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		})
		if k := KindOf(newClient(t, srv).Probe(bg)); k != KindServer {
			t.Errorf("Kind = %v", k)
		}
	})
}

func TestDownloadReturnsRawBody(t *testing.T) {
	srv, _ := serve(t, reply(200, "not json at all \x00\x01"))
	b, err := newClient(t, srv).DownloadCredential(bg, "a.json")
	if err != nil || string(b) != "not json at all \x00\x01" {
		t.Errorf("body = %q, err = %v", b, err)
	}
}

func TestCredentialModelsDecodes(t *testing.T) {
	srv, _ := serve(t, reply(200, `{"models":[{"id":"gpt-5","display_name":"GPT 5","type":"chat","owned_by":"openai"}]}`))
	models, err := newClient(t, srv).CredentialModels(bg, "a.json")
	if err != nil || len(models) != 1 || models[0].ID != "gpt-5" || models[0].OwnedBy != "openai" {
		t.Errorf("models = %+v, err = %v", models, err)
	}
}

// ---- Credential helpers ----

func TestCredentialHealth(t *testing.T) {
	retry := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var zero time.Time
	tests := []struct {
		name string
		c    Credential
		want Health
	}{
		{"active", Credential{Status: StatusActive}, HealthAvailable},
		{"empty status", Credential{}, HealthAvailable},
		{"unknown status", Credential{Status: StatusUnknown}, HealthAvailable},
		{"disabled flag", Credential{Disabled: true, Status: StatusActive}, HealthDisabled},
		{"disabled status", Credential{Status: StatusDisabled}, HealthDisabled},
		{"disabled beats error", Credential{Disabled: true, Status: StatusError, Unavailable: true}, HealthDisabled},
		{"disabled beats pending", Credential{Disabled: true, Status: StatusPending}, HealthDisabled},
		{"pending", Credential{Status: StatusPending}, HealthPending},
		{"refreshing", Credential{Status: StatusRefreshing}, HealthPending},
		{"pending beats cooling", Credential{Status: StatusPending, Unavailable: true, NextRetryAfter: &retry}, HealthPending},
		{"cooling", Credential{Status: StatusActive, Unavailable: true, NextRetryAfter: &retry}, HealthCooling},
		{"cooling with error status", Credential{Status: StatusError, Unavailable: true, NextRetryAfter: &retry}, HealthCooling},
		{"unavailable without retry time", Credential{Status: StatusActive, Unavailable: true}, HealthError},
		{"unavailable with zero retry time", Credential{Unavailable: true, NextRetryAfter: &zero}, HealthError},
		{"error status", Credential{Status: StatusError}, HealthError},
		{"retry time without unavailable", Credential{Status: StatusActive, NextRetryAfter: &retry}, HealthAvailable},
		{"error status with retry time but available", Credential{Status: StatusError, NextRetryAfter: &retry}, HealthError},
	}
	for _, tc := range tests {
		if got := tc.c.Health(); got != tc.want {
			t.Errorf("%s: Health() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCredentialNames(t *testing.T) {
	if got := (Credential{Provider: "codex", Type: "x"}).ProviderName(); got != "codex" {
		t.Errorf("ProviderName = %q", got)
	}
	if got := (Credential{Type: "claude"}).ProviderName(); got != "claude" {
		t.Errorf("ProviderName falls back to type, got %q", got)
	}
	tests := []struct {
		c    Credential
		want string
	}{
		{Credential{Label: "L", Email: "e", Account: "a", Name: "n", ID: "i"}, "L"},
		{Credential{Label: "  ", Email: "e", Name: "n"}, "e"},
		{Credential{Account: "a", Name: "n"}, "a"},
		{Credential{Name: "n", ID: "i"}, "n"},
		{Credential{ID: "i"}, "i"},
		{Credential{}, ""},
	}
	for _, tc := range tests {
		if got := tc.c.DisplayName(); got != tc.want {
			t.Errorf("DisplayName(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}
