package cpa

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CPA answers the Redis protocol (RESP) on the same port as HTTP, telling
// them apart by the first byte. A client that has sent AUTH with the
// management key can SUBSCRIBE to the "usage" channel and receive each
// usage record as JSON as it is produced. While anyone is subscribed, CPA
// publishes the records instead of queueing them, so the HTTP usage queue
// stays empty.

// StreamReason says why the usage stream ended.
type StreamReason int

const (
	// StreamNetwork: the connection failed or dropped; trying again later
	// may work.
	StreamNetwork StreamReason = iota
	// StreamRejected: the server refused the key, has remote management
	// off, or banned the address. Each refused key counts towards the ban,
	// so do not try again with the same key.
	StreamRejected
	// StreamHomeMode: the server is in home mode, which has no usage
	// output.
	StreamHomeMode
	// StreamUnsupported: the address does not answer the protocol, as an
	// HTTP-only reverse proxy in front of CPA does not.
	StreamUnsupported
)

// StreamError is why SubscribeUsage returned.
type StreamError struct {
	Reason StreamReason
	// Msg is the server's message, when it sent one.
	Msg string
	Err error
}

func (e *StreamError) Error() string {
	switch {
	case e.Msg != "":
		return e.Msg
	case e.Err != nil:
		return e.Err.Error()
	}
	return "usage stream closed"
}

func (e *StreamError) Unwrap() error { return e.Err }

const (
	// streamPing is how often the subscription is pinged; streamIdle is how
	// long it may stay silent, pongs included, before it counts as dead.
	streamPing = 30 * time.Second
	streamIdle = 75 * time.Second
	// streamMaxBulk bounds one message.
	streamMaxBulk = 16 << 20
)

// SubscribeUsage subscribes to the usage channel and passes each message to
// fn, on the calling goroutine, until ctx ends (nil) or the stream fails
// (a *StreamError). ready runs once the subscription is confirmed. The
// messages include control payloads such as {"support_refresh":true}.
func (c *Client) SubscribeUsage(ctx context.Context, ready func(), fn func([]byte)) error {
	conn, err := c.dialStream(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return &StreamError{Reason: StreamNetwork, Err: err}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()

	r := bufio.NewReader(conn)
	var wmu sync.Mutex
	send := func(args ...string) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
		_, err := conn.Write(respCommand(args...))
		return err
	}
	fail := func(err error) error {
		if ctx.Err() != nil {
			return nil
		}
		var se *StreamError
		if errors.As(err, &se) {
			return se
		}
		return &StreamError{Reason: StreamNetwork, Err: err}
	}
	// reply reads one answer to a command; the server may also have
	// refused the connection before reading anything (home mode).
	reply := func() (any, error) {
		_ = conn.SetReadDeadline(time.Now().Add(c.readTimeout))
		return readRESP(r)
	}

	if err := send("AUTH", c.key); err != nil {
		return fail(err)
	}
	v, err := reply()
	if err != nil {
		return fail(err)
	}
	if s, ok := v.(string); !ok || s != "OK" {
		return fail(unexpected(v))
	}
	if err := send("SUBSCRIBE", "usage"); err != nil {
		return fail(err)
	}
	v, err = reply()
	if err != nil {
		return fail(err)
	}
	if a, ok := v.([]any); !ok || len(a) < 2 || fmt.Sprint(a[0]) != "subscribe" {
		return fail(unexpected(v))
	}
	if ready != nil {
		ready()
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(streamPing)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if send("PING") != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(streamIdle))
		v, err := readRESP(r)
		if err != nil {
			return fail(err)
		}
		a, ok := v.([]any)
		if !ok || len(a) == 0 {
			if e, isErr := v.(respError); isErr {
				return fail(classify(string(e)))
			}
			continue
		}
		if kind, _ := a[0].(string); kind == "message" && len(a) == 3 {
			if payload, ok := a[2].(string); ok {
				fn([]byte(payload))
			}
		}
	}
}

// dialStream connects to the server's listener with the TLS the HTTP
// requests use.
func (c *Client) dialStream(ctx context.Context) (net.Conn, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	d := &net.Dialer{Timeout: c.readTimeout, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return conn, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.tlsConf != nil {
		cfg = c.tlsConf.Clone()
	}
	cfg.ServerName = u.Hostname()
	tc := tls.Client(conn, cfg)
	hctx, cancel := context.WithTimeout(ctx, c.readTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// classify turns a server error reply into a StreamError.
func classify(msg string) *StreamError {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "home mode"):
		return &StreamError{Reason: StreamHomeMode, Msg: msg}
	case strings.HasPrefix(low, "err unknown command"), strings.HasPrefix(low, "err unsupported channel"):
		return &StreamError{Reason: StreamUnsupported, Msg: msg}
	}
	return &StreamError{Reason: StreamRejected, Msg: msg}
}

func unexpected(v any) error {
	if e, ok := v.(respError); ok {
		return classify(string(e))
	}
	return &StreamError{Reason: StreamUnsupported, Msg: "the server did not answer the usage stream protocol"}
}

// respError is an error reply ("-ERR ...").
type respError string

var errProtocol = &StreamError{Reason: StreamUnsupported, Msg: "the server did not answer the usage stream protocol"}

func respCommand(args ...string) []byte {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	return []byte(b.String())
}

// readRESP reads one reply: a string (simple or bulk; nil bulk is nil), a
// respError, an int64 or a []any.
func readRESP(r *bufio.Reader) (any, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+':
		return line, nil
	case '-':
		return respError(line), nil
	case ':':
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, errProtocol
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil || n > streamMaxBulk {
			return nil, errProtocol
		}
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line)
		if err != nil || n > 1024 {
			return nil, errProtocol
		}
		if n < 0 {
			return nil, nil
		}
		out := make([]any, 0, n)
		for range n {
			v, err := readRESP(r)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	// "HTTP/1.1 400 ..." from a proxy, or anything else.
	return nil, errProtocol
}
