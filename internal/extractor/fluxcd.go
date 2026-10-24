package extractor

import (
	"bytes"
	"sync"
)

// objectMeta mirrors the Kubernetes ObjectMeta fields we care about.
type objectMeta struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// crossNamespaceRef is the sourceRef / chartRef block inside a HelmRelease.
type crossNamespaceRef struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// fluxSourceSpec is the spec block of a HelmRepository or OCIRepository.
type fluxSourceSpec struct {
	URL string     `yaml:"url"`
	Ref fluxOCIRef `yaml:"ref"`
}

// fluxOCIRef is the spec.ref block of an OCIRepository.
type fluxOCIRef struct {
	Tag    string `yaml:"tag"`
	SemVer string `yaml:"semver"`
}

// version is what the OCIRepository is pinned to, in Flux's own precedence.
func (r fluxOCIRef) version() string {
	if r.SemVer != "" {
		return r.SemVer
	}
	return r.Tag
}

// fluxSourceResource parses HelmRepository and OCIRepository manifests during Prepare.
type fluxSourceResource struct {
	Kind     string         `yaml:"kind"`
	Metadata objectMeta     `yaml:"metadata"`
	Spec     fluxSourceSpec `yaml:"spec"`
}

// helmChartSpec is the spec.chart.spec block of a HelmRelease.
type helmChartSpec struct {
	Chart     string             `yaml:"chart"`
	Version   string             `yaml:"version"`
	RepoURL   string             `yaml:"repoURL"`
	SourceRef *crossNamespaceRef `yaml:"sourceRef"`
}

// helmChartWrapper is the spec.chart block of a HelmRelease.
type helmChartWrapper struct {
	Spec helmChartSpec `yaml:"spec"`
}

// helmReleaseSpec is the spec block of a HelmRelease.
type helmReleaseSpec struct {
	ChartRef *crossNamespaceRef `yaml:"chartRef"`
	Chart    helmChartWrapper   `yaml:"chart"`
}

// helmReleaseResource is used during Extract to parse a HelmRelease manifest.
type helmReleaseResource struct {
	Metadata objectMeta      `yaml:"metadata"`
	Spec     helmReleaseSpec `yaml:"spec"`
}

// repoEntry is a resolved registry record built from a HelmRepository or OCIRepository during Prepare.
type repoEntry struct {
	protocol  string
	repoURL   string // bare URL, scheme stripped
	chartName string // derived from OCI URL last segment; empty for HTTPS
	version   string // pinned version from OCIRepository spec.ref (semver range or tag)
}

// FluxCD resolves HelmRelease chart refs: PrepareFile collects sources, Extract resolves the releases against them.
type FluxCD struct {
	mu   sync.RWMutex
	helm map[string]repoEntry // keyed by "namespace/name"
	oci  map[string]repoEntry // keyed by "namespace/name"
}

// NewFluxCD creates a new FluxCD extractor.
func NewFluxCD() *FluxCD {
	return &FluxCD{
		helm: make(map[string]repoEntry),
		oci:  make(map[string]repoEntry),
	}
}

// Type returns "fluxcd".
func (*FluxCD) Type() string { return "fluxcd" }

// Match reports whether the file contains a HelmRelease resource.
func (*FluxCD) Match(f *File) bool {
	return bytes.Contains(f.Content, []byte("HelmRelease"))
}

// PrepareFile collects HelmRepository/OCIRepository resources for cross-file HelmRelease sourceRef/chartRef resolution.
func (f *FluxCD) PrepareFile(file *File) error {
	if !bytes.Contains(file.Content, []byte("HelmRepository")) &&
		!bytes.Contains(file.Content, []byte("OCIRepository")) {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, doc := range decodeAll[fluxSourceResource](file) {
		key := doc.Metadata.Namespace + "/" + doc.Metadata.Name

		switch doc.Kind {
		case "HelmRepository":
			protocol, bare := ParseProtocol(doc.Spec.URL)
			f.helm[key] = repoEntry{protocol: protocol, repoURL: bare}

		case "OCIRepository":
			protocol, bare := ParseProtocol(doc.Spec.URL)
			repo, chart := SplitOCIRef(bare)
			f.oci[key] = repoEntry{
				protocol:  protocol,
				repoURL:   repo,
				chartName: chart,
				version:   doc.Spec.Ref.version(),
			}
		}
	}

	return nil
}

// Extract returns chart refs from all HelmRelease documents in content via three patterns: inline repoURL, sourceRef→HelmRepository, chartRef→OCIRepository.
func (f *FluxCD) Extract(file *File) (string, []Ref, error) {
	var all []Ref
	for _, hr := range decodeAll[helmReleaseResource](file) {
		if hr.Metadata.Name == "" {
			continue // not a HelmRelease (or an empty document)
		}
		all = append(all, f.refsFromRelease(hr)...)
	}
	return "", all, nil
}

// source resolves a release's cross-namespace reference among the sources collected during Prepare; the namespace defaults to the release's own.
func (f *FluxCD) source(store map[string]repoEntry, ref *crossNamespaceRef, releaseNS string) (repoEntry, bool) {
	ns := ref.Namespace
	if ns == "" {
		ns = releaseNS
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if entry, ok := store[ns+"/"+ref.Name]; ok {
		return entry, true
	}
	entry, ok := store["/"+ref.Name]
	return entry, ok
}

// refsFromRelease extracts Refs from a single HelmRelease document.
func (f *FluxCD) refsFromRelease(hr helmReleaseResource) []Ref {
	releaseName := hr.Metadata.Name

	// Pattern 3: chartRef → OCIRepository
	if cr := hr.Spec.ChartRef; cr != nil && cr.Kind == "OCIRepository" {
		entry, ok := f.source(f.oci, cr, hr.Metadata.Namespace)
		if !ok {
			return nil
		}
		return releaseRef(releaseName, entry.chartName, entry.version, entry)
	}

	cs := hr.Spec.Chart.Spec

	// Pattern 2: sourceRef → HelmRepository
	if sr := cs.SourceRef; sr != nil && sr.Kind == "HelmRepository" {
		entry, ok := f.source(f.helm, sr, hr.Metadata.Namespace)
		if !ok {
			return nil
		}
		return releaseRef(releaseName, cs.Chart, cs.Version, entry)
	}

	// Pattern 1: inline repoURL
	if cs.RepoURL == "" {
		return nil
	}
	protocol, repo := ParseProtocol(cs.RepoURL)
	return releaseRef(releaseName, cs.Chart, cs.Version, repoEntry{protocol: protocol, repoURL: repo})
}

// releaseRef is the one Ref a HelmRelease yields, or none when the chart or its version is unknown (a digest-only OCIRepository, an unpinned chart).
func releaseRef(release, chart, version string, src repoEntry) []Ref {
	if chart == "" || version == "" {
		return nil
	}
	return []Ref{{
		Name:           chart,
		Chart:          release,
		Protocol:       src.protocol,
		Repository:     src.repoURL,
		CurrentVersion: version,
		Kind:           "HelmRelease",
	}}
}
