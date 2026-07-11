package trigger

import (
	"context"
	"errors"
	"log/slog"
)

// Inline runs the scan in-process by calling the provided function.
// Used in binary and Docker deployments.
type Inline struct {
	// base scopes triggered scans to the server lifetime instead of the HTTP
	// request that triggered them; a disconnecting client must not cancel a
	// scan halfway through.
	base context.Context
	fn   func(ctx context.Context) error
}

// NewInline creates an Inline trigger that calls fn in a background goroutine
// scoped to the base context.
func NewInline(base context.Context, fn func(ctx context.Context) error) *Inline {
	return &Inline{base: base, fn: fn}
}

// Trigger starts the wrapped function in the background and returns immediately.
func (t *Inline) Trigger(_ context.Context) error {
	go func() {
		if err := t.fn(t.base); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("triggered scan failed", "error", err)
		}
	}()
	return nil
}

// Available always returns true for the inline trigger.
func (t *Inline) Available() bool { return true }

// Running always returns false; the scan service tracks in-process scan state itself.
func (t *Inline) Running(_ context.Context) bool { return false }
