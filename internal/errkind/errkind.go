// Package errkind classifies failures into a small vocabulary that the UI, metrics and alerts share.
package errkind

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// Kind is one cause of failure. The values are part of the API (check_error_kind, repo_errors[].kind) and of the error_kind metric label.
// +kubebuilder:validation:MaxLength=63
type Kind string

// The failure kinds.
const (
	Auth           Kind = "auth"            // credentials missing, wrong or refused (401/403, git auth)
	NotFound       Kind = "not_found"       // repository, chart, tag or image does not exist (404)
	Unreachable    Kind = "unreachable"     // DNS, connection refused/reset, TLS — never got an answer
	Timeout        Kind = "timeout"         // a deadline expired (per request, per git command, scan_timeout)
	RateLimited    Kind = "rate_limited"    // the registry throttled us (429)
	ServerError    Kind = "server_error"    // the registry answered 5xx
	HostKey        Kind = "host_key"        // ssh host key could not be verified
	InvalidVersion Kind = "invalid_version" // the pinned version is unparsable or matches nothing
	TooLarge       Kind = "too_large"       // a response exceeded its size cap
	Unsupported    Kind = "unsupported"     // a protocol or reference form the checker cannot handle
	Other          Kind = "other"           // anything not classified above
)

// All lists every kind, for tests and documentation.
var All = []Kind{Auth, NotFound, Unreachable, Timeout, RateLimited, ServerError, HostKey, InvalidVersion, TooLarge, Unsupported, Other}

// Error is an error with a known kind.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// Wrap attaches kind to err.
func Wrap(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	return &Error{Kind: kind, Err: err}
}

// Of returns the kind of err: the one attached by Wrap, else what a network error or an expired context says, else Other. Of(nil) is "".
func Of(err error) Kind {
	if err == nil {
		return ""
	}
	var known *Error
	if errors.As(err, &known) {
		return known.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return Timeout
		}
		return Unreachable
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) {
		return Unreachable
	}
	return Other
}

// FromHTTPStatus maps a non-200 HTTP status to a kind.
func FromHTTPStatus(code int) Kind {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return Auth
	case code == http.StatusNotFound:
		return NotFound
	case code == http.StatusTooManyRequests:
		return RateLimited
	case code >= 500:
		return ServerError
	default:
		return Other
	}
}
