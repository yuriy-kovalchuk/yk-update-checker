// Package version exposes build-time version information injected via ldflags.
package version

import "runtime"

// Set at build time via -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// GoVersion returns the Go runtime version the binary was compiled with.
func GoVersion() string { return runtime.Version() }
