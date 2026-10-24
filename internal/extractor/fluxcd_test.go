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

// A Flux HelmChart resource: spec.chart is a *string* here, which type-mismatches helmReleaseSpec's struct field during Extract.
const fluxHelmChart = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmChart
metadata:
  name: broken
spec:
  chart: podinfo
`

func TestExtractInlineRepoURL(t *testing.T) {
	f := NewFluxCD()
	_, refs, err := f.Extract(NewFile("release.yaml", []byte(inlineRelease)))
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
	if ref.Kind != "HelmRelease" {
		t.Errorf("ref.Kind = %q, want HelmRelease so metrics report the manifest kind", ref.Kind)
	}
}

func TestExtractContinuesPastMalformedDocument(t *testing.T) {
	content := fluxHelmChart + "---\n" + inlineRelease

	f := NewFluxCD()
	_, refs, err := f.Extract(NewFile("mixed.yaml", []byte(content)))
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
	if err := f.PrepareFile(NewFile("sources.yaml", []byte(sources))); err != nil {
		t.Fatalf("PrepareFile: %v", err)
	}
	_, refs, err := f.Extract(NewFile("release.yaml", []byte(release)))
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

// TestFluxResolvesSourcesWithoutNamespace: a source with no metadata.namespace matches any namespace asked for.
func TestFluxResolvesSourcesWithoutNamespace(t *testing.T) {
	const sources = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bitnami
spec:
  url: https://charts.bitnami.com/bitnami
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: podinfo
spec:
  url: oci://ghcr.io/stefanprodan/charts/podinfo
  ref:
    semver: ">=6.0.0 <7.0.0"
`
	const releases = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: redis
spec:
  chart:
    spec:
      chart: redis
      version: 18.0.0
      sourceRef:
        kind: HelmRepository
        name: bitnami
        namespace: flux-system
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: podinfo
  namespace: apps
spec:
  chartRef:
    kind: OCIRepository
    name: podinfo
`
	f := NewFluxCD()
	if err := f.PrepareFile(NewFile("sources.yaml", []byte(sources))); err != nil {
		t.Fatal(err)
	}
	_, refs, err := f.Extract(NewFile("releases.yaml", []byte(releases)))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want redis and podinfo", refs)
	}
	if refs[0].Name != "redis" || refs[0].Repository != "charts.bitnami.com/bitnami" {
		t.Errorf("refs[0] = %+v, want redis from the namespace-less bitnami source", refs[0])
	}
	// spec.ref.semver is the pin: resolveCurrent turns the range into the newest matching registry version.
	if refs[1].Name != "podinfo" || refs[1].CurrentVersion != ">=6.0.0 <7.0.0" {
		t.Errorf("refs[1] = %+v, want podinfo pinned by its semver range", refs[1])
	}
}

// A source declared in the release's namespace wins over a namespace-less one of the same name.
func TestFluxPrefersExactNamespaceSource(t *testing.T) {
	const sources = `kind: HelmRepository
metadata:
  name: charts
spec:
  url: https://unscoped.example/charts
---
kind: HelmRepository
metadata:
  name: charts
  namespace: team
spec:
  url: https://team.example/charts
`
	const release = `kind: HelmRelease
metadata:
  name: app
  namespace: team
spec:
  chart:
    spec:
      chart: app
      version: 1.0.0
      sourceRef:
        kind: HelmRepository
        name: charts
`
	f := NewFluxCD()
	if err := f.PrepareFile(NewFile("sources.yaml", []byte(sources))); err != nil {
		t.Fatal(err)
	}
	_, refs, _ := f.Extract(NewFile("release.yaml", []byte(release)))
	if len(refs) != 1 || refs[0].Repository != "team.example/charts" {
		t.Errorf("refs = %+v, want the team-namespace source", refs)
	}
}
