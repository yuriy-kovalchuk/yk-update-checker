package api

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

func TestRunReturnsErrorWhenPortBusy(t *testing.T) {
	// Occupy the wildcard address: the server binds ":port", and on macOS a
	// 127.0.0.1-specific listener would not conflict with it.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	svc := scan.NewService(nil, scan.NewRepository())
	errCh := make(chan error, 1)
	go func() { errCh <- New(port).Run(context.Background(), svc) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil, want listen error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after listen failure")
	}
}
