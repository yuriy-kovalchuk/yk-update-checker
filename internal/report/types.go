// Package report is the contract between scanner and dashboard: the scanner writes, the dashboard reads.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// API group and version of the custom resources.
const (
	Group      = "updatechecker.yuriykovalchuk.dev"
	APIVersion = Group + "/v1alpha1"
)

// Labels on every Dependency.
const (
	LabelRepoID   = Group + "/repo-id"
	LabelType     = Group + "/type"
	LabelOutdated = Group + "/outdated"
	LabelInstance = "app.kubernetes.io/instance" // the Helm release; one release per namespace
)

// Phases of a RepositoryScan.
const (
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
)

// Meta is the part of Kubernetes object metadata the reports use.
type Meta struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// RepositoryScan is how the latest scan of one repository went.
type RepositoryScan struct {
	Meta   Meta       `json:"metadata"`
	Status RepoStatus `json:"status"`
}

// RepoStatus is the outcome of a repository's latest scan, plus its history.
type RepoStatus struct {
	Repo            string     `json:"repo"`
	Phase           string     `json:"phase"`
	LastScanTime    time.Time  `json:"last_scan_time"`
	LastSuccessTime *time.Time `json:"last_success_time,omitempty"`
	Error           *ErrorInfo `json:"error,omitempty"`
	Counts          Counts     `json:"counts"`
	History         []Run      `json:"history,omitempty"` // newest first, capped by the scanner's --history
}

// ErrorInfo is a classified failure.
type ErrorInfo struct {
	Kind    errkind.Kind `json:"kind"`
	Message string       `json:"message"`
}

// Counts summarize a repository's references.
type Counts struct {
	Checked  int `json:"checked"`
	Outdated int `json:"outdated"`
	Failed   int `json:"failed"`
}

// Run is one entry of a repository's scan history.
type Run struct {
	Time      time.Time    `json:"time"`
	Phase     string       `json:"phase"`
	Counts    Counts       `json:"counts"`
	ErrorKind errkind.Kind `json:"error_kind,omitempty"`
}

// Dependency is one checked reference.
type Dependency struct {
	Meta   Meta      `json:"metadata"`
	Status DepStatus `json:"status"`
}

// DepStatus is the scan result plus what only history can tell.
type DepStatus struct {
	scan.Result
	OutdatedSince *time.Time `json:"outdated_since,omitempty"` // first scan that found it behind
	LatestSeenAt  *time.Time `json:"latest_seen_at,omitempty"` // first scan that saw latest_version
}

// RepoID is a short, stable, label-safe identifier of a repository name.
func RepoID(repo string) string { return shortHash(repo) }

// RepositoryScanName is the object name of a repository's RepositoryScan.
func RepositoryScanName(repo string) string { return dnsName(repo, 46) + "-" + shortHash(repo) }

// DependencyName names one reference after its identity (the pinned version included: a new pin is a new reference), so a rescan updates the same object.
func DependencyName(r scan.Result) string {
	id := strings.Join([]string{r.Source, r.Type, r.Path, r.Chart, r.Dependency, r.CurrentVersion}, "\x00")
	return dnsName(r.Source, 30) + "-" + dnsName(r.Type, 10) + "-" + shortHash(id)
}

// dnsName lowercases s into a DNS-1123 fragment; the hash callers append keeps names unique however much this changes.
func dnsName(s string, maxLen int) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b[:min(len(b), maxLen)]), "-")
	if out == "" {
		return "x"
	}
	return out
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
