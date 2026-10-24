package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type noRoutes struct{}

func (noRoutes) RegisterRoutes(*http.ServeMux) {}

func TestRunReturnsErrorWhenPortBusy(t *testing.T) {
	// Occupy the wildcard address: the server binds ":port", and on macOS a 127.0.0.1-specific listener would not conflict with it.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	errCh := make(chan error, 1)
	go func() { errCh <- New(port).Run(context.Background(), noRoutes{}) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil, want listen error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after listen failure")
	}
}

// TestServerTimeoutsAreSet guards the S7 fix: the server must bound whole requests and writes, not only header reads.
func TestServerTimeoutsAreSet(t *testing.T) {
	srv := newServer("0", http.NotFoundHandler())
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout,
		"ReadTimeout":       srv.ReadTimeout,
		"WriteTimeout":      srv.WriteTimeout,
		"IdleTimeout":       srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s = %v, want a positive timeout", name, d)
		}
	}
}

func TestReadyFailsWhileDraining(t *testing.T) {
	s := New("0")
	rec := httptest.NewRecorder()
	s.ready(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/ready before shutdown = %d, want 200", rec.Code)
	}
	s.draining.Store(true)
	rec = httptest.NewRecorder()
	s.ready(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/ready while draining = %d, want 503", rec.Code)
	}
}
