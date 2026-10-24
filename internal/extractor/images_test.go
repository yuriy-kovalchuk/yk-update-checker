package extractor

import (
	"strings"
	"testing"
)

const workloadManifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: homepage
spec:
  template:
    spec:
      initContainers:
        - name: perms
          image: busybox
      containers:
        - name: web
          image: ghcr.io/gethomepage/homepage:v2.4.0
        - name: sidecar
          image: docker:29.8.0-dind
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: kubectl
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: kubectl
              image: alpine/k8s:1.36.1
          initContainers:
            - name: wait
              image: alpine:3.21
---
apiVersion: v1
kind: Pod
metadata:
  name: oneoff
spec:
  containers:
    - name: main
      image: alpine@sha256:4bccb98203a0e353fae5a869412050fcd017675aa7c6c8651b8d9f3f6d24f3c0
  ephemeralContainers:
    - name: debugger
      image: busybox:1.37.0
`

func TestExtractImagesAcrossKinds(t *testing.T) {
	images := NewImages()
	_, refs, err := images.Extract(NewFile("app.yaml", []byte(workloadManifests)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	want := []struct{ chart, name, repo, tag, digest string }{
		{"Deployment/homepage", "busybox", "index.docker.io/library/busybox", "", ""},
		{"Deployment/homepage", "ghcr.io/gethomepage/homepage", "ghcr.io/gethomepage/homepage", "v2.4.0", ""},
		{"Deployment/homepage", "docker", "index.docker.io/library/docker", "29.8.0-dind", ""},
		{"CronJob/kubectl", "alpine/k8s", "index.docker.io/alpine/k8s", "1.36.1", ""},
		{"CronJob/kubectl", "alpine", "index.docker.io/library/alpine", "3.21", ""},
		{"Pod/oneoff", "alpine", "index.docker.io/library/alpine", "", "sha256:4bccb98203a0e353fae5a869412050fcd017675aa7c6c8651b8d9f3f6d24f3c0"},
		{"Pod/oneoff", "busybox", "index.docker.io/library/busybox", "1.37.0", ""},
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %d (%v), want %d", len(refs), refNames(refs), len(want))
	}
	for i, w := range want {
		got := refs[i]
		if got.Chart != w.chart || got.Name != w.name || got.Repository != w.repo ||
			got.CurrentVersion != w.tag || got.Digest != w.digest || got.Protocol != ProtocolImage {
			t.Errorf("ref %d = %+v, want %+v", i, got, w)
		}
	}
	// The manifest kind travels with the reference so metrics can say what sort of object is outdated, and the workload label is prefixed with it.
	for _, r := range refs {
		if r.Kind == "" {
			t.Errorf("Ref %s: Kind is empty, the workload kind must be kept", r.Name)
		}
		if !strings.HasPrefix(r.Chart, r.Kind+"/") {
			t.Errorf("Ref %s: Chart = %q, want it prefixed with its Kind (%q)", r.Name, r.Chart, r.Kind+"/")
		}
	}

}

func TestExtractImagesSkipsHelmReleaseValueImages(t *testing.T) {
	// HelmRelease value overrides are out of scope for v1: they have no repository and no tag in most cases, so they would be unverifiable.
	content := `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: cloudnative-pg
spec:
  values:
    image:
      tag: 1.27.0
    resources:
      image: ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0
`
	images := NewImages()
	_, refs, err := images.Extract(NewFile("hr.yaml", []byte(content)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %v, want none", refNames(refs))
	}
}

const crdWithImageProperties = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: kubevirts.kubevirt.io
spec:
  versions:
    - name: v1
      schema:
        openAPIV3Schema:
          properties:
            spec:
              properties:
                imagePrefix:
                  type: string
                  description: The container prefix for all images used by KubeVirt
                imageTag:
                  type: string
                  description: The container image tag to be used by all KubeVirt images
            status:
              properties:
                observedDeploymentImagePullPolicy:
                  type: string
                targetKubeVirtVersion:
                  type: string
`

const strimziPodLike = `apiVersion: kafka.strimzi.io/v1beta2
kind: Kafka
metadata:
  name: kafka-cluster
spec:
  kafka:
    containers:
      - name: kafka
        image: quay.io/strimzi/kafka:0.49.0-kafka-4.2.0
`

func TestExtractImagesSkipsCRDSchemaButKeepsCustomKinds(t *testing.T) {
	images := NewImages()

	_, refs, err := images.Extract(NewFile("crd.yaml", []byte(crdWithImageProperties)))
	if err != nil {
		t.Fatalf("Extract(crd): %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("CRD refs = %v, want none", refNames(refs))
	}

	_, refs, err = images.Extract(NewFile("kafka.yaml", []byte(strimziPodLike)))
	if err != nil {
		t.Fatalf("Extract(kafka): %v", err)
	}
	if len(refs) != 1 || refs[0].Chart != "Kafka/kafka-cluster" || refs[0].CurrentVersion != "0.49.0-kafka-4.2.0" {
		t.Fatalf("refs = %+v, want the strimzi container", refs)
	}
}

func TestExtractImagesDeduplicatesWithinWorkload(t *testing.T) {
	content := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: valkey
spec:
  template:
    spec:
      initContainers:
        - name: setup
          image: valkey/valkey:9
      containers:
        - name: valkey
          image: valkey/valkey:9
`
	images := NewImages()
	_, refs, err := images.Extract(NewFile("sts.yaml", []byte(content)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %v, want 1 distinct image", refNames(refs))
	}
}

func TestExtractImagesIgnoresMalformedDocument(t *testing.T) {
	content := "kind: Deployment\nspec: [broken\n---\n" + strimziPodLike
	images := NewImages()
	_, refs, err := images.Extract(NewFile("mixed.yaml", []byte(content)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "quay.io/strimzi/kafka" {
		t.Fatalf("refs = %v, want the document after the broken one", refNames(refs))
	}
}

func TestImagesMatch(t *testing.T) {
	images := NewImages()
	if images.Match(NewFile("values.yml.something", []byte("image: foo"))) {
		t.Error("non-YAML path matched")
	}
	if images.Match(NewFile("config.yaml", []byte("imagetTag: foo"))) {
		t.Error("file without an image: key matched")
	}
	if !images.Match(NewFile("deployment.YAML", []byte("image: alpine"))) {
		t.Error("case-insensitive YAML path with image: did not match")
	}
}

func TestParseImageRef(t *testing.T) {
	cases := []struct{ in, name, repo, tag, digest string }{
		{"alpine:3.21", "alpine", "index.docker.io/library/alpine", "3.21", ""},
		{"busybox", "busybox", "index.docker.io/library/busybox", "", ""},
		{"alpine/k8s:1.36.1", "alpine/k8s", "index.docker.io/alpine/k8s", "1.36.1", ""},
		{"localhost:5000/app:1.0", "localhost:5000/app", "localhost:5000/app", "1.0", ""},
		{"harbor.example.com/homelab/app:v1", "harbor.example.com/homelab/app", "harbor.example.com/homelab/app", "v1", ""},
		{"alpine@sha256:abcd", "alpine", "index.docker.io/library/alpine", "", "sha256:abcd"},
		{"registry.example.com:5000/x/y@sha256:abcd", "registry.example.com:5000/x/y", "registry.example.com:5000/x/y", "", "sha256:abcd"},
	}
	for _, c := range cases {
		got, ok := parseImageRef(c.in)
		if !ok {
			t.Errorf("parseImageRef(%q): not ok", c.in)
			continue
		}
		if got.Name != c.name || got.Repository != c.repo || got.Tag != c.tag || got.Digest != c.digest {
			t.Errorf("parseImageRef(%q) = %+v, want name=%q repo=%q tag=%q digest=%q", c.in, got, c.name, c.repo, c.tag, c.digest)
		}
	}
	if _, ok := parseImageRef("   "); ok {
		t.Error("blank reference parsed as ok")
	}
}

func refNames(refs []Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, strings.Join([]string{r.Chart, r.Name + ":" + r.CurrentVersion}, "|"))
	}
	return out
}

// A templated image reference is resolved at deploy time; asking a registry about "${VERSION}" only produces an error row.
func TestImagesSkipsTemplatedReferences(t *testing.T) {
	const manifest = `kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
        - name: a
          image: ghcr.io/org/app:${VERSION}
        - name: b
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
        - name: c
          image: registry.example/app:$(TAG)
        - name: d
          image: alpine:3.21
`
	_, refs, err := NewImages().Extract(NewFile("app.yaml", []byte(manifest)))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Name != "alpine" {
		t.Errorf("refs = %+v, want only alpine", refs)
	}
}
