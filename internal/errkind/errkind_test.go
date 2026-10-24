package errkind

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

func TestOf(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"nil", nil, ""},
		{"plain", base, Other},
		{"wrapped", Wrap(Auth, base), Auth},
		{"wrapped deeper", fmt.Errorf("repo x: %w", Wrap(NotFound, base)), NotFound},
		{"inner kind wins", Wrap(Other, Wrap(HostKey, base)), HostKey},
		{"deadline", fmt.Errorf("git clone: %w", context.DeadlineExceeded), Timeout},
		{"dns", &net.DNSError{Err: "no such host", Name: "x.invalid"}, Unreachable},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, Unreachable},
		{"joined keeps first kind", errors.Join(Wrap(RateLimited, base), Wrap(Auth, base)), RateLimited},
	}
	for _, tc := range cases {
		if got := Of(tc.err); got != tc.want {
			t.Errorf("%s: Of = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFromHTTPStatus(t *testing.T) {
	cases := map[int]Kind{
		http.StatusUnauthorized: Auth, http.StatusForbidden: Auth, http.StatusNotFound: NotFound,
		http.StatusTooManyRequests: RateLimited, http.StatusBadGateway: ServerError, http.StatusTeapot: Other,
	}
	for code, want := range cases {
		if got := FromHTTPStatus(code); got != want {
			t.Errorf("FromHTTPStatus(%d) = %q, want %q", code, got, want)
		}
	}
}
