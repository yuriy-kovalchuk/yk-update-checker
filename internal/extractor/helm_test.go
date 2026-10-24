package extractor

import "testing"

// Local subcharts and repository aliases have no registry to ask; they used to be fetched as https://file://.../index.yaml and reported as errors.
func TestHelmSkipsLocalAndAliasedDependencies(t *testing.T) {
	const chart = `name: umbrella
dependencies:
  - name: common
    version: 1.0.0
    repository: file://../common
  - name: redis
    version: 18.0.0
    repository: "@bitnami"
  - name: pg
    version: 12.0.0
    repository: alias:bitnami
  - name: podinfo
    version: 6.0.0
    repository: https://stefanprodan.github.io/podinfo
`
	name, refs, err := NewHelmChart().Extract(NewFile("Chart.yaml", []byte(chart)))
	if err != nil {
		t.Fatal(err)
	}
	if name != "umbrella" {
		t.Errorf("chart name = %q, want umbrella", name)
	}
	if len(refs) != 1 || refs[0].Name != "podinfo" {
		t.Errorf("refs = %+v, want only the remote podinfo dependency", refs)
	}
}
