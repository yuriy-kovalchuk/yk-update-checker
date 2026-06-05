package extractor

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type chartDependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
}

type chartManifest struct {
	Name         string            `yaml:"name"`
	Dependencies []chartDependency `yaml:"dependencies"`
}

// HelmChart extracts dependencies from Helm Chart.yaml files.
type HelmChart struct{}

// NewHelmChart creates a new HelmChart extractor.
func NewHelmChart() *HelmChart { return &HelmChart{} }

// Type returns "helm".
func (*HelmChart) Type() string { return "helm" }

// Match reports whether the file is a Chart.yaml.
func (*HelmChart) Match(path string, _ []byte) bool {
	return strings.EqualFold(filepath.Base(path), "Chart.yaml")
}

// PrepareFile is a no-op for HelmChart; cross-file preparation is not needed.
func (*HelmChart) PrepareFile(_ string, _ []byte) error {
	return nil // HelmChart doesn't need cross-file preparation
}

// Extract parses a Chart.yaml and returns its name and dependency chart refs.
func (*HelmChart) Extract(_ string, content []byte) (string, []ChartRef, error) {
	var chart chartManifest
	if err := yaml.Unmarshal(content, &chart); err != nil {
		return "", nil, err
	}

	refs := make([]ChartRef, 0, len(chart.Dependencies))
	for _, d := range chart.Dependencies {
		if d.Repository == "" || d.Name == "" || d.Version == "" {
			continue
		}
		protocol, repo := ParseProtocol(d.Repository)
		refs = append(refs, ChartRef{
			Name:           d.Name,
			Protocol:       protocol,
			Repository:     repo,
			CurrentVersion: d.Version,
		})
	}
	return chart.Name, refs, nil
}
