package extractor

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sync"

	"gopkg.in/yaml.v3"
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
	Tag string `yaml:"tag"`
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
	version   string // pinned version from OCIRepository spec.ref.tag
}

// FluxCD extracts chart refs from HelmRelease manifests via a two-pass prepare+extract pattern (PrepareFile collects HelmRepository/OCIRepository sources; Extract resolves HelmRelease refs against them).
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
func (*FluxCD) Match(_ string, content []byte) bool {
	return bytes.Contains(content, []byte("HelmRelease"))
}

// PrepareFile collects HelmRepository/OCIRepository resources for cross-file HelmRelease sourceRef/chartRef resolution.
func (f *FluxCD) PrepareFile(_ string, content []byte) error {
	if !bytes.Contains(content, []byte("HelmRepository")) &&
		!bytes.Contains(content, []byte("OCIRepository")) {
		return nil
	}

	dec := yaml.NewDecoder(bytes.NewReader(content))

	f.mu.Lock()
	defer f.mu.Unlock()

	for {
		var doc fluxSourceResource
		if err := dec.Decode(&doc); err != nil {
			if keepDecoding(err, "fluxcd prepare") {
				continue
			}
			break
		}

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
				version:   doc.Spec.Ref.Tag,
			}
		}
	}

	return nil
}

// Extract returns chart refs from all HelmRelease documents in content via three patterns: inline repoURL, sourceRef→HelmRepository, chartRef→OCIRepository.
func (f *FluxCD) Extract(_ string, content []byte) (string, []ChartRef, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var all []ChartRef
	for {
		var hr helmReleaseResource
		if err := dec.Decode(&hr); err != nil {
			if keepDecoding(err, "fluxcd extract") {
				continue
			}
			break
		}
		if hr.Metadata.Name == "" {
			continue // not a HelmRelease (or an empty document)
		}
		all = append(all, f.refsFromRelease(hr)...)
	}
	return "", all, nil
}

// keepDecoding reports whether a multi-document decode loop can continue past
// err. Type mismatches (e.g. a Flux HelmChart whose spec.chart is a string)
// consume their document, so later documents in the file must still be read;
// syntax errors and io.EOF end the file.
func keepDecoding(err error, op string) bool {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		slog.Warn("skipping malformed YAML document", "op", op, "error", err)
		return true
	}
	if !errors.Is(err, io.EOF) {
		slog.Warn("stopping YAML parse", "op", op, "error", err)
	}
	return false
}

// refsFromRelease extracts ChartRefs from a single HelmRelease document.
func (f *FluxCD) refsFromRelease(hr helmReleaseResource) []ChartRef {
	releaseName := hr.Metadata.Name
	releaseNS := hr.Metadata.Namespace

	// Pattern 3: chartRef → OCIRepository
	if cr := hr.Spec.ChartRef; cr != nil && cr.Kind == "OCIRepository" {
		ns := cr.Namespace
		if ns == "" {
			ns = releaseNS
		}
		key := ns + "/" + cr.Name

		f.mu.RLock()
		entry, ok := f.oci[key]
		f.mu.RUnlock()

		if !ok || entry.chartName == "" || entry.version == "" {
			return nil
		}
		return []ChartRef{{
			Name:           entry.chartName,
			Chart:          releaseName,
			Protocol:       entry.protocol,
			Repository:     entry.repoURL,
			CurrentVersion: entry.version,
		}}
	}

	cs := hr.Spec.Chart.Spec

	// Pattern 2: sourceRef → HelmRepository
	if sr := cs.SourceRef; sr != nil && sr.Kind == "HelmRepository" {
		ns := sr.Namespace
		if ns == "" {
			ns = releaseNS
		}
		key := ns + "/" + sr.Name

		f.mu.RLock()
		entry, ok := f.helm[key]
		f.mu.RUnlock()

		if !ok || cs.Chart == "" || cs.Version == "" {
			return nil
		}
		return []ChartRef{{
			Name:           cs.Chart,
			Chart:          releaseName,
			Protocol:       entry.protocol,
			Repository:     entry.repoURL,
			CurrentVersion: cs.Version,
		}}
	}

	// Pattern 1: inline repoURL
	if cs.RepoURL == "" || cs.Chart == "" || cs.Version == "" {
		return nil
	}
	protocol, repo := ParseProtocol(cs.RepoURL)
	return []ChartRef{{
		Name:           cs.Chart,
		Chart:          releaseName,
		Protocol:       protocol,
		Repository:     repo,
		CurrentVersion: cs.Version,
	}}
}
