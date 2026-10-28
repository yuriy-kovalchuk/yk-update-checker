package scan

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ociregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// startImageRegistry serves an in-memory OCI registry holding "app" at tags 1.0.0 and 1.1.0, and returns its host.
func startImageRegistry(t *testing.T) string {
	t.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard) // the in-memory registry logs every request
	t.Cleanup(func() { log.SetOutput(prev) })

	srv := httptest.NewServer(ociregistry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse registry URL: %v", err)
	}

	for i, tag := range []string{"1.0.0", "1.1.0"} {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		cf.Created = v1.Time{Time: time.Unix(int64(1750000000+i*86400), 0)}
		cf.Config.Labels = map[string]string{"org.opencontainers.image.version": tag}
		img, err = mutate.ConfigFile(img, cf)
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		ref, err := name.NewTag(u.Host+"/app:"+tag, name.WeakValidation)
		if err != nil {
			t.Fatalf("tag ref: %v", err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("push %s: %v", tag, err)
		}
	}
	return u.Host
}

func TestRunScansContainerImages(t *testing.T) {
	host := startImageRegistry(t)
	dir := initGitRepo(t, map[string]string{
		"apps/web/deployment.yaml": fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: %s/app:1.0.0
`, host),
		"platform/kubevirt/crd.yaml": `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: kubevirts.kubevirt.io
spec:
  versions:
    - schema:
        openAPIV3Schema:
          properties:
            spec:
              properties:
                imageTag:
                  type: string
                  description: The container image tag to be used by all KubeVirt images
`,
	})

	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeMinor,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})

	results, repoErrs, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (repoErrs=%v)", err, repoErrs)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want exactly one image result (CRD must not count)", results)
	}

	got := results[0]
	if got.Type != "image" || got.Protocol != "image" {
		t.Errorf("Type/Protocol = %q/%q, want image/image", got.Type, got.Protocol)
	}
	if got.Chart != "Deployment/web" {
		t.Errorf("Chart = %q, want Deployment/web", got.Chart)
	}
	if got.Dependency != host+"/app" {
		t.Errorf("Dependency = %q, want %q", got.Dependency, host+"/app")
	}
	if got.CurrentVersion != "1.0.0" || got.LatestVersion != "1.1.0" {
		t.Errorf("current/latest = %q/%q, want 1.0.0/1.1.0", got.CurrentVersion, got.LatestVersion)
	}
	if !got.UpdateAvailable {
		t.Error("UpdateAvailable = false, want true")
	}
	if got.Scope != "minor" {
		t.Errorf("Scope = %q, want minor", got.Scope)
	}
	if got.TagKind != "semver" {
		t.Errorf("TagKind = %q, want semver", got.TagKind)
	}
	if got.CheckError != "" {
		t.Errorf("CheckError = %q, want none", got.CheckError)
	}
}

func TestRunImageCheckFailureIsReportedPerImage(t *testing.T) {
	dir := initGitRepo(t, map[string]string{
		"apps/web/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: 127.0.0.1:1/nope/missing:1.0.0
`,
	})
	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeAll,
		ParallelChecks: 1,
		Cache:          registry.NewIndexCache(),
	})

	results, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want 1", results)
	}
	if results[0].CheckError == "" {
		t.Error("CheckError empty, want the unreachable registry to be reported per image")
	}
	if results[0].UpdateAvailable {
		t.Error("UpdateAvailable = true for a failed check")
	}
	if results[0].TagKind != "semver" {
		t.Errorf("TagKind = %q, want semver even when the check failed", results[0].TagKind)
	}
}

// TestRunImageDigestPinIsRecorded keeps a digest pin visible in the result: the report name
// hashes it, so a scan that lost it would merge two references into one report.
func TestRunImageDigestPinIsRecorded(t *testing.T) {
	host := startImageRegistry(t)

	tag, err := name.NewTag(host+"/app:1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatalf("name the pushed tag: %v", err)
	}
	desc, err := remote.Get(tag, remote.WithContext(context.Background()))
	if err != nil {
		t.Fatalf("read the pushed tag: %v", err)
	}

	dir := initGitRepo(t, map[string]string{
		"apps/web/deployment.yaml": fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: follows-the-tag
          image: %s/app:1.0.0
        - name: pinned
          image: %s/app:1.0.0@%s
`, host, host, desc.Digest),
	})

	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeMinor,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})

	results, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per container", results)
	}

	pinned := 0
	for _, r := range results {
		if r.CheckError != "" {
			t.Errorf("%s: CheckError = %q, want the pinned digest to resolve", r.Dependency, r.CheckError)
		}
		switch r.PinnedDigest {
		case "":
		case desc.Digest.String():
			pinned++
		default:
			t.Errorf("PinnedDigest = %q, want %q", r.PinnedDigest, desc.Digest)
		}
	}
	if pinned != 1 {
		t.Errorf("results carry the digest pin for %d references, want 1", pinned)
	}
}
