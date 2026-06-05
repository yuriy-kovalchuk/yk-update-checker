package trigger

import "context"

// Inline runs the scan in-process by calling the provided function.
// Used in binary and Docker deployments.
type Inline struct {
	fn func(ctx context.Context) error
}

// NewInline creates an Inline trigger that calls fn directly.
func NewInline(fn func(ctx context.Context) error) *Inline {
	return &Inline{fn: fn}
}

// Trigger calls the wrapped function.
func (t *Inline) Trigger(ctx context.Context) error { return t.fn(ctx) }

// Available always returns true for the inline trigger.
func (t *Inline) Available() bool { return true }
