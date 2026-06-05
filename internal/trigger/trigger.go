// Package trigger defines how a scan is initiated.
package trigger

import "context"

// Trigger starts a scan.
type Trigger interface {
	Trigger(ctx context.Context) error
	Available() bool
}
