package scan

import (
	"context"
	"fmt"
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// TestRunScansDockerfiles: the walk must open files with no YAML extension, and the final build stage must be flagged.
func TestRunScansDockerfiles(t *testing.T) {
	host := startImageRegistry(t) // serves "app" at 1.0.0 and 1.1.0
	dir := initGitRepo(t, map[string]string{
		"build/Dockerfile": fmt.Sprintf(`FROM %[1]s/app:1.0.0 AS build
RUN true
FROM build AS bundle
FROM %[1]s/app:1.1.0 AS runtime
`, host),
		"build/.dockerignore": "vendor",
	})

	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewDockerfile()} },
		Scope:          registry.ScopeMinor,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})

	results, repoErrs, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (repoErrs=%v)", err, repoErrs)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want the two real base images (stage reference skipped)", results)
	}

	build, runtime := resultByChart(t, results, "build/Dockerfile/build"), resultByChart(t, results, "build/Dockerfile/runtime")
	if build.Type != "dockerfile" || build.Protocol != "image" {
		t.Errorf("Type/Protocol = %q/%q, want dockerfile/image", build.Type, build.Protocol)
	}
	if build.Path != "build/Dockerfile" {
		t.Errorf("Path = %q, want build/Dockerfile", build.Path)
	}
	if build.Chart != "build/Dockerfile/build" || runtime.Chart != "build/Dockerfile/runtime" {
		t.Errorf("charts = %q / %q, want the repo-relative file plus stage", build.Chart, runtime.Chart)
	}
	if build.Dependency != host+"/app" || build.CurrentVersion != "1.0.0" || build.LatestVersion != "1.1.0" {
		t.Errorf("build stage = %q %s → %s", build.Dependency, build.CurrentVersion, build.LatestVersion)
	}
	if !build.UpdateAvailable || build.Scope != "minor" {
		t.Errorf("build stage: update=%v scope=%q, want true/minor", build.UpdateAvailable, build.Scope)
	}
	if build.FinalStage {
		t.Error("intermediate stage flagged final")
	}
	if !runtime.FinalStage {
		t.Error("last base image not flagged final")
	}
	if runtime.UpdateAvailable || runtime.CurrentVersion != "1.1.0" {
		t.Errorf("runtime stage: update=%v current=%q, want false/1.1.0", runtime.UpdateAvailable, runtime.CurrentVersion)
	}
}

// TestRunDockerfilesInSeparateDirsAreDistinct: two Dockerfiles sharing a stage alias must stay two identities.
func TestRunDockerfilesInSeparateDirsAreDistinct(t *testing.T) {
	host := startImageRegistry(t) // serves "app" at 1.0.0 and 1.1.0
	dir := initGitRepo(t, map[string]string{
		"apps/api/Dockerfile":    fmt.Sprintf("FROM %s/app:1.0.0 AS build\n", host),
		"apps/worker/Dockerfile": fmt.Sprintf("FROM %s/app:1.0.0 AS build\n", host),
	})

	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewDockerfile()} },
		Scope:          registry.ScopeAll,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})

	results, repoErrs, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (repoErrs=%v)", err, repoErrs)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want one per Dockerfile", results)
	}

	api := resultByChart(t, results, "apps/api/Dockerfile/build")
	worker := resultByChart(t, results, "apps/worker/Dockerfile/build")
	if api.Path == worker.Path {
		t.Errorf("both results report Path %q", api.Path)
	}
	// Every Prometheus label except Chart is identical here, which is what makes the Chart prefix the thing keeping the two series apart.
	if api.Dependency != worker.Dependency || api.Scope != worker.Scope || api.Type != worker.Type {
		t.Errorf("expected the pair to match apart from Chart, got %+v and %+v", api, worker)
	}
}

// resultByChart picks one result out of a scan whose order is nondeterministic (checks run concurrently).
func resultByChart(t *testing.T, results []Result, chart string) Result {
	t.Helper()
	for _, r := range results {
		if r.Chart == chart {
			return r
		}
	}
	t.Fatalf("no result for chart %q in %+v", chart, results)
	return Result{}
}

// TestRunDockerfilesAlongsideWorkloads checks the two image extractors label their results apart.
func TestRunDockerfilesAlongsideWorkloads(t *testing.T) {
	host := startImageRegistry(t)
	dir := initGitRepo(t, map[string]string{
		"Dockerfile": fmt.Sprintf("FROM %s/app:1.0.0\n", host),
		"apps/web.yaml": fmt.Sprintf(`apiVersion: apps/v1
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
		Repos: []config.Repo{{Name: "homelab", URL: dir}},
		NewExtractors: func() []extractor.Extractor {
			return []extractor.Extractor{extractor.NewImages(), extractor.NewDockerfile()}
		},
		Scope:          registry.ScopeAll,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})

	results, repoErrs, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (repoErrs=%v)", err, repoErrs)
	}
	types := map[string]int{}
	for _, r := range results {
		types[r.Type]++
	}
	if types["image"] != 1 || types["dockerfile"] != 1 {
		t.Fatalf("results by type = %+v, want one image and one dockerfile result: %+v", types, results)
	}
}
