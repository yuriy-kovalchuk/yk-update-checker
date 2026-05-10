package constants

import "time"

// HTTP Server
const (
	MaxRequestBody    = 4 << 20       // 4 MB
	ShutdownTimeout   = 30 * time.Second
	ReadHeaderTimeout = 10 * time.Second
)

// Database
const (
	DBBusyTimeout       = 5000 * time.Millisecond
	DBMaxOpenConns      = 1
	DBListScansDefault  = 5
	DBStuckScanTimeout = 2 * time.Hour
	DBStuckScanCheck   = time.Minute
)

// HTTP Client
const (
	HTTPClientTimeout = 30 * time.Second
)

// Scanner
const (
	DefaultParallelChecks = 5
	GitCloneDepth        = 1
)

// API
const (
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
)