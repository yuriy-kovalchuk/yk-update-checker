// Package extractor parses GitOps YAML files and container build definitions and extracts the dependency references they pin.
package extractor

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Ref is one pinned dependency reference — a chart, a HelmRelease's chart or a container image — with everything the version engine needs to check it.
type Ref struct {
	Name           string // human label and registry lookup key
	Chart          string // logical owner (e.g. HelmRelease name); overrides the outer chartName in multi-doc files
	Protocol       string // "https", "oci" or "image"
	Repository     string // registry base URL, scheme already stripped (for images: canonical image path)
	CurrentVersion string
	Digest         string // digest the reference is pinned to, if any (images only)
	// Kind is the manifest kind the reference was found in ("Deployment", "HelmRelease", …), empty where there is no manifest (a Chart.yaml dependency).
	Kind string
	// FinalStage marks the reference that produces the build's output image (Dockerfile FROM lines only); intermediate build stages leave it false.
	FinalStage bool
}

// File is one repository file handed to the extractors.
type File struct {
	Path    string
	Content []byte

	docs    []*yaml.Node
	decoded bool
}

// NewFile wraps a file read from a checkout.
func NewFile(path string, content []byte) *File {
	return &File{Path: path, Content: content}
}

// Documents returns the root node of every YAML document in the file, decoding on first use.
func (f *File) Documents() []*yaml.Node {
	if !f.decoded {
		f.docs, f.decoded = decodeDocuments(f.Content), true
	}
	return f.docs
}

// Extractor extracts Refs from a file; implementations must be safe for concurrent use.
type Extractor interface {
	// Type returns the label used in the Type column (e.g. "helm", "fluxcd").
	Type() string
	// Match reports whether this extractor should process the given file.
	Match(f *File) bool
	// PrepareFile is called once per file during the pre-pass; use for cross-file reference resolution (e.g. FluxCD).
	PrepareFile(f *File) error
	// Extract returns the logical chart name of a file and its references.
	Extract(f *File) (chartName string, refs []Ref, err error)
}

// IsScannable reports whether a file name is one any extractor reads, so the repository walk can skip everything else without opening it.
func IsScannable(path string) bool { return IsYAML(path) || IsDockerfile(path) }

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
