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
	Running(ctx context.Context) bool // a scan started through this trigger is still in progress
}
