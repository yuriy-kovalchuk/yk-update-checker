package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// pushTags adds plain single-platform images to the test registry.
func pushTags(t *testing.T, host, repository string, tags ...string) {
	t.Helper()
	for _, tag := range tags {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		cf.Created = v1.Time{Time: time.Unix(1750000000, 0)}
		cf.OS, cf.Architecture = "linux", "amd64"
		cf.Config.Labels = map[string]string{"org.opencontainers.image.version": tag}
		img, err = mutate.ConfigFile(img, cf)
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repository, tag), name.WeakValidation)
		if err != nil {
			t.Fatalf("tag ref: %v", err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("push %s:%s: %v", repository, tag, err)
		}
	}
}

// A stray tag must not outrank the real update, and the rejection has to survive all the way into the stored result (and its JSON).
func TestRunImageResultCarriesSkippedCandidates(t *testing.T) {
	host := startImageRegistry(t) // app:1.0.0, app:1.1.0
	pushTags(t, host, "app", "17.42.37")

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
	})

	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeAll,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})
	results, repoErrs, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (repoErrs=%v)", err, repoErrs)
	}

	var res *Result
	for i, r := range results {
		if r.Type == "image" {
			res = &results[i]
			break
		}
	}
	if res == nil {
		t.Fatalf("no image result in %+v", results)
	}
	if res.CurrentVersion != "1.0.0" || res.LatestVersion != "1.1.0" {
		t.Errorf("current/latest = %q/%q, want 1.0.0/1.1.0", res.CurrentVersion, res.LatestVersion)
	}
	// The dashboard shows where to make the change: the file, relative to the repo.
	if res.Path != "apps/web/deployment.yaml" {
		t.Errorf("Path = %q, want apps/web/deployment.yaml", res.Path)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Tag != "17.42.37" {
		t.Fatalf("Skipped = %+v, want 17.42.37 rejected", res.Skipped)
	}
	if len(res.Skipped[0].Reasons) < 2 {
		t.Errorf("reasons = %v, want at least two soft signals", res.Skipped[0].Reasons)
	}
	if !strings.Contains(res.Note, "skipped 17.42.37") {
		t.Errorf("Note = %q, want the skipped tag summarized", res.Note)
	}

	body, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"skipped"`, `"17.42.37"`, `"reasons"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("JSON %s missing %s", body, want)
		}
	}
}
