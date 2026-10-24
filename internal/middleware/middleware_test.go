package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestRecoveryReturns500OnPanic(t *testing.T) {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := Recovery(inner)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Errorf("body = %q, want it to contain %q", w.Body.String(), "internal server error")
	}
}

func TestRecoveryPassesThroughWithoutPanic(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Recovery(inner)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTeapot)
	}
}

func TestHeadersSetsSecurityHeaders(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Headers(inner)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "DENY")
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	// S6: the dashboard has no inline handlers and no external origins, so the policy stays default-deny; a relaxation would be a regression.
	csp := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'", "script-src 'self'", "style-src 'self'",
		"connect-src 'self'", "frame-ancestors 'none'", "object-src 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("Content-Security-Policy %q is missing %q", csp, directive)
		}
	}
	for _, unsafe := range []string{"'unsafe-inline'", "'unsafe-eval'", "*"} {
		if strings.Contains(csp, unsafe) {
			t.Errorf("Content-Security-Policy %q must not contain %q", csp, unsafe)
		}
	}
}

func TestLoggerLogsRequest(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(original) })

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Logger(inner)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/status", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	logged := buf.String()
	for _, want := range []string{"HTTP request", "GET", "/api/status"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output missing %q: %s", want, logged)
		}
	}
}

func TestChainAppliesFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	mkMW := func(label string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, label)
				next.ServeHTTP(w, r)
			})
		}
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "inner")
		w.WriteHeader(http.StatusOK)
	})

	h := Chain(inner, mkMW("A"), mkMW("B"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))

	if !slices.Equal(order, []string{"A", "B", "inner"}) {
		t.Errorf("middleware order = %v, want [A B inner]", order)
	}
}

func TestRecoveryDoesNotRewriteAStartedResponse(t *testing.T) {
	h := Recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Errorf("got %d %q, want the started 200 response left alone", rec.Code, rec.Body.String())
	}
}

func TestRecoveryReraisesErrAbortHandler(t *testing.T) {
	h := Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint // sentinel compared as panicked
			t.Errorf("recovered %v, want http.ErrAbortHandler re-raised", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
}
