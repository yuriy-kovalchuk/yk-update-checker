// Package extractor parses GitOps YAML files and extracts Helm chart dependency references.
package extractor

import "strings"

// ChartRef holds everything the version engine needs to check for an update.
type ChartRef struct {
	Name           string // human label and registry lookup key
	Chart          string // logical owner (e.g. HelmRelease name); overrides the outer chartName in multi-doc files
	Protocol       string // "https" or "oci"
	Repository     string // registry base URL, scheme already stripped
	CurrentVersion string
}

// Extractor extracts ChartRefs from a file on disk; implementations must be safe for concurrent use.
type Extractor interface {
	// Type returns the label used in the Type column (e.g. "helm", "fluxcd").
	Type() string
	// Match reports whether this extractor should process the given file.
	Match(path string, content []byte) bool
	// PrepareFile is called once per file during the pre-pass; use for cross-file reference resolution (e.g. FluxCD).
	PrepareFile(path string, content []byte) error
	// Extract parses content and returns the logical chart name and its chart references.
	Extract(path string, content []byte) (chartName string, refs []ChartRef, err error)
}

// SplitOCIRef splits the last path segment off a bare OCI URL, e.g. "ghcr.io/org/charts/podinfo" → ("ghcr.io/org/charts", "podinfo").
func SplitOCIRef(bare string) (repo, chart string) {
	i := strings.LastIndex(bare, "/")
	if i < 0 {
		return bare, ""
	}
	return bare[:i], bare[i+1:]
}

// ParseProtocol splits a raw repository URL into protocol and bare URL, e.g. "oci://ghcr.io/org" → ("oci", "ghcr.io/org").
func ParseProtocol(rawURL string) (protocol, repo string) {
	switch {
	case strings.HasPrefix(rawURL, "oci://"):
		return "oci", strings.TrimPrefix(rawURL, "oci://")
	case strings.HasPrefix(rawURL, "https://"):
		return "https", strings.TrimPrefix(rawURL, "https://")
	case strings.HasPrefix(rawURL, "http://"):
		return "http", strings.TrimPrefix(rawURL, "http://")
	default:
		return "https", rawURL
	}
}
