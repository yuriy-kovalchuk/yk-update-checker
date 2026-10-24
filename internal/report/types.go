// Package report is the contract between scanner and dashboard: the scanner writes, the dashboard reads.
// +groupName=updatechecker.yuriykovalchuk.dev
// +versionName=v1alpha1
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// RepositoryScan is how the latest scan of one repository went.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=repscan,categories=updatechecker
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name=Outdated,type=integer,JSONPath=`.status.counts.outdated`
// +kubebuilder:printcolumn:name=Failed,type=integer,JSONPath=`.status.counts.failed`
// +kubebuilder:printcolumn:name=Last Scan,type=date,JSONPath=`.status.last_scan_time`
type RepositoryScan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            RepoStatus `json:"status"`
}

// RepoStatus is the outcome of a repository's latest scan, plus its history.
type RepoStatus struct {
	// +kubebuilder:validation:MaxLength=253
	Repo string `json:"repo"`
	// +kubebuilder:validation:Enum=Succeeded;Failed
	Phase           string       `json:"phase"`
	LastScanTime    metav1.Time  `json:"last_scan_time"`
	LastSuccessTime *metav1.Time `json:"last_success_time,omitempty"`
	Error           *ErrorInfo   `json:"error,omitempty"`
	Counts          Counts       `json:"counts"`
	// History is newest first, capped by the scanner's --history.
	// +kubebuilder:validation:MaxItems=100
	History []Run `json:"history,omitempty"`
}

// ErrorInfo is a classified failure.
type ErrorInfo struct {
	Kind errkind.Kind `json:"kind"`
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message"`
}

// Counts summarize a repository's references.
type Counts struct {
	// +kubebuilder:validation:Minimum=0
	Checked int `json:"checked"`
	// +kubebuilder:validation:Minimum=0
	Outdated int `json:"outdated"`
	// +kubebuilder:validation:Minimum=0
	Failed int `json:"failed"`
}

// Run is one entry of a repository's scan history.
type Run struct {
	Time metav1.Time `json:"time"`
	// +kubebuilder:validation:Enum=Succeeded;Failed
	Phase     string       `json:"phase"`
	Counts    Counts       `json:"counts"`
	ErrorKind errkind.Kind `json:"error_kind,omitempty"`
}

// Dependency is one checked reference.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=dep,categories=updatechecker
// +kubebuilder:printcolumn:name=Type,type=string,JSONPath=`.status.type`
// +kubebuilder:printcolumn:name=Dependency,type=string,JSONPath=`.status.dependency`
// +kubebuilder:printcolumn:name=Current,type=string,JSONPath=`.status.current_version`
// +kubebuilder:printcolumn:name=Latest,type=string,JSONPath=`.status.latest_version`
// +kubebuilder:printcolumn:name=Scope,type=string,JSONPath=`.status.scope`
// +kubebuilder:printcolumn:name=Outdated Since,type=date,JSONPath=`.status.outdated_since`
type Dependency struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            DepStatus `json:"status"`
}

// DepStatus is the scan result plus what only history can tell.
type DepStatus struct {
	scan.Result   `json:",inline"`
	OutdatedSince *metav1.Time `json:"outdated_since,omitempty"` // first scan that found it behind
	LatestSeenAt  *metav1.Time `json:"latest_seen_at,omitempty"` // first scan that saw latest_version
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
