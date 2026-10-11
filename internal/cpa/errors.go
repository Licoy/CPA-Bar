package cpa

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// Kind classifies an APIError for the UI and the refresh scheduler.
type Kind int

const (
	KindUnknown Kind = iota
	// KindNetwork: the server could not be reached.
	KindNetwork
	// KindTimeout: a read timed out.
	KindTimeout
	// KindIndeterminate: a write was sent but its outcome is unknown
	// (timeout or connection loss after sending). Never retried
	// automatically.
	KindIndeterminate
	// KindUnauthorized: 401, a missing or wrong management key.
	KindUnauthorized
	// KindForbidden: 403, remote management disabled or the address banned.
	KindForbidden
	// KindManagementUnavailable: 404 on a management route, which means an
	// older CPA without v8, management disabled, or a wrong address.
	KindManagementUnavailable
	// KindNotFound: 404 for a specific resource, such as a credential.
	KindNotFound
	// KindRateLimited: 429.
	KindRateLimited
	// KindInvalid: 400/422, the server rejected the request.
	KindInvalid
	// KindConflict: 409.
	KindConflict
	// KindServer: 5xx.
	KindServer
	// KindDecode: the response could not be parsed.
	KindDecode
	// KindCanceled: the caller canceled the request.
	KindCanceled
)

func (k Kind) String() string {
	switch k {
	case KindNetwork:
		return "network"
	case KindTimeout:
		return "timeout"
	case KindIndeterminate:
		return "indeterminate"
	case KindUnauthorized:
		return "unauthorized"
	case KindForbidden:
		return "forbidden"
	case KindManagementUnavailable:
		return "management-unavailable"
	case KindNotFound:
		return "not-found"
	case KindRateLimited:
		return "rate-limited"
	case KindInvalid:
		return "invalid"
	case KindConflict:
		return "conflict"
	case KindServer:
		return "server"
	case KindDecode:
		return "decode"
	case KindCanceled:
		return "canceled"
	}
	return "unknown"
}

// APIError keeps the instance, the operation, the HTTP status and the
// server's own message of a failed management call.
type APIError struct {
	InstanceID   string
	InstanceName string
	Op           string // e.g. "list credentials"
	Method       string
	Path         string // relative to the management base
	Status       int    // 0 when no response arrived
	Kind         Kind
	// Message is the server's "error"/"message" text, or a description of
	// the local failure.
	Message string
	Err     error
}

func (e *APIError) Error() string {
	who := e.InstanceName
	if who == "" {
		who = e.InstanceID
	}
	var s string
	if who != "" {
		s = who + ": "
	}
	s += e.Op
	if e.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Message != "" {
		s += ": " + e.Message
	} else if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *APIError) Unwrap() error { return e.Err }

// IsAuth reports whether the error means the key or access policy must be
// fixed before more automatic requests are sent.
func (e *APIError) IsAuth() bool {
	return e.Kind == KindUnauthorized || e.Kind == KindForbidden
}

// KindOf returns the Kind of an error, KindUnknown for non-API errors.
func KindOf(err error) Kind {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return KindUnknown
}

// IsAuthError reports whether err is a 401/403 APIError.
func IsAuthError(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsAuth()
}

func kindForStatus(status int, resourceRoute bool) Kind {
	switch {
	case status == http.StatusUnauthorized:
		return KindUnauthorized
	case status == http.StatusForbidden:
		return KindForbidden
	case status == http.StatusNotFound:
		if resourceRoute {
			return KindNotFound
		}
		return KindManagementUnavailable
	case status == http.StatusTooManyRequests:
		return KindRateLimited
	case status == http.StatusConflict:
		return KindConflict
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return KindInvalid
	case status >= 500:
		return KindServer
	}
	return KindUnknown
}

// kindForTransport classifies a failure where no response arrived.
func kindForTransport(ctx context.Context, err error, write bool) Kind {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return KindCanceled
	}
	timeout := errors.Is(err, context.DeadlineExceeded)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		timeout = true
	}
	if write && (timeout || !isDialError(err)) {
		// The request may have reached the server.
		return KindIndeterminate
	}
	if timeout {
		return KindTimeout
	}
	return KindNetwork
}

func isDialError(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return true
	}
	var de *net.DNSError
	return errors.As(err, &de)
}
