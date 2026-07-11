package extractor

import (
	"testing"
)

const inlineRelease = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
  namespace: default
spec:
  chart:
    spec:
      chart: podinfo
      version: 1.0.0
      repoURL: https://charts.example.com
`

// A Flux HelmChart resource: spec.chart is a *string* here, which
// type-mismatches helmReleaseSpec's struct field during Extract.
const fluxHelmChart = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata:
  name: broken
spec:
  chart: podinfo
`

func TestExtractInlineRepoURL(t *testing.T) {
	f := NewFluxCD()
	_, refs, err := f.Extract("release.yaml", []byte(inlineRelease))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want 1", refs)
	}
	ref := refs[0]
	if ref.Name != "podinfo" || ref.CurrentVersion != "1.0.0" || ref.Protocol != "https" || ref.Repository != "charts.example.com" {
		t.Errorf("unexpected ref: %+v", ref)
	}
}

func TestExtractContinuesPastMalformedDocument(t *testing.T) {
	content := fluxHelmChart + "---\n" + inlineRelease

	f := NewFluxCD()
	_, refs, err := f.Extract("mixed.yaml", []byte(content))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "podinfo" {
		t.Fatalf("refs = %+v, want the HelmRelease after the malformed document", refs)
	}
}

func TestExtractSourceRef(t *testing.T) {
	sources := `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: example
  namespace: flux-system
spec:
  url: https://charts.example.com
`
	release := `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
  namespace: flux-system
spec:
  chart:
    spec:
      chart: podinfo
      version: 2.1.0
      sourceRef:
        kind: HelmRepository
        name: example
`
	f := NewFluxCD()
	if err := f.PrepareFile("sources.yaml", []byte(sources)); err != nil {
		t.Fatalf("PrepareFile: %v", err)
	}
	_, refs, err := f.Extract("release.yaml", []byte(release))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want 1", refs)
	}
	if refs[0].Repository != "charts.example.com" || refs[0].CurrentVersion != "2.1.0" {
		t.Errorf("unexpected ref: %+v", refs[0])
	}
}
