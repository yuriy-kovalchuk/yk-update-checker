package scan

import "time"

// Result is the outcome of one dependency version check.
type Result struct {
	Source          string    `json:"source"`
	Chart           string    `json:"chart"`
	Dependency      string    `json:"dependency"`
	Type            string    `json:"type"`
	Protocol        string    `json:"protocol"`
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	Scope           string    `json:"scope"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at"`
}

// Status describes the current state of the scanner.
type Status struct {
	Scanning         bool       `json:"scanning"`
	TriggerAvailable bool       `json:"trigger_available"`
	LastScanAt       *time.Time `json:"last_scan_at,omitempty"`
	ResultCount      int        `json:"result_count"`
	Version          string     `json:"version"`
}
