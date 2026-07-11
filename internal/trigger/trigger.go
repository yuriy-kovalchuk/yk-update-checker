// Package trigger defines how a scan is initiated.
package trigger

import (
	"context"
	"errors"
)

// ErrAlreadyRunning is returned by Trigger when a scan is already in progress.
var ErrAlreadyRunning = errors.New("scan already running")

// Trigger starts a scan.
type Trigger interface {
	Trigger(ctx context.Context) error
	Available() bool
	// Running reports whether a scan started through this trigger is still in
	// progress. Triggers that run scans in-process may return false and let the
	// caller track its own scan state.
	Running(ctx context.Context) bool
}
