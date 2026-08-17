package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

func newTestRunner(repos []config.Repo) *Runner {
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart()}
	}
	return NewRunner(repos, newExtractors, registry.ScopeAll, 2, "", registry.NewIndexCache(0))
}

// initGitRepo creates a local git repository with the given files committed.
func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestRunAllReposFailReturnsError(t *testing.T) {
	repos := []config.Repo{
		{Name: "bad-one", URL: filepath.Join(t.TempDir(), "missing")},
		{Name: "bad-two", URL: filepath.Join(t.TempDir(), "missing")},
	}

	results, repoErrs, err := newTestRunner(repos).Run(context.Background())
	if err == nil {
		t.Fatal("Run: want error when all repos fail, got nil")
	}
	if !strings.Contains(err.Error(), "all repos failed") {
		t.Errorf("err = %v, want all-repos-failed error", err)
	}
	if results != nil || repoErrs != nil {
		t.Errorf("results=%v repoErrs=%v, want nil on total failure", results, repoErrs)
	}
}

func TestRunPartialFailureReturnsRepoErrs(t *testing.T) {
	good := initGitRepo(t, map[string]string{
		"Chart.yaml": "name: sample\nversion: 1.0.0\n",
	})
	repos := []config.Repo{
		{Name: "good", URL: good},
		{Name: "bad", URL: filepath.Join(t.TempDir(), "missing")},
	}

	_, repoErrs, err := newTestRunner(repos).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(repoErrs) != 1 {
		t.Fatalf("repoErrs = %v, want exactly 1", repoErrs)
	}
	if !strings.Contains(repoErrs[0].Error(), "repo bad") {
		t.Errorf("repoErrs[0] = %v, want it to name repo bad", repoErrs[0])
	}
}

// TestRunReusesSharedCacheBetweenRuns verifies that a cache shared between
// Runs (as serve mode does) avoids re-fetching registry indexes.
func TestRunReusesSharedCacheBetweenRuns(t *testing.T) {
	var indexCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		indexCalls.Add(1)
		_, _ = w.Write([]byte("entries:\n  podinfo:\n  - version: 6.0.0\n"))
	}))
	defer ts.Close()

	chart := "name: sample\nversion: 1.0.0\ndependencies:\n" +
		"- name: podinfo\n  version: 5.0.0\n  repository: " + ts.URL + "\n"
	good := initGitRepo(t, map[string]string{"Chart.yaml": chart})
	repos := []config.Repo{{Name: "good", URL: good}}

	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart()}
	}
	runner := NewRunner(repos, newExtractors, registry.ScopeAll, 2, "", registry.NewIndexCache(0))

	for _, run := range []string{"first", "second"} {
		results, _, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("Run (%s): %v", run, err)
		}
		if len(results) != 1 {
			t.Fatalf("Run (%s): got %d results, want 1", run, len(results))
		}
	}

	if got := indexCalls.Load(); got != 1 {
		t.Errorf("index fetched %d times across 2 runs with a shared cache, want 1", got)
	}
}

func TestAuthEnvTokenUsesHeaderNotURL(t *testing.T) {
	repo := config.Repo{Auth: config.RepoAuth{Type: "token", Token: "s3cret"}}
	env := authEnv(repo)

	// base64("git:s3cret")
	want := "GIT_CONFIG_VALUE_0=Authorization: Basic Z2l0OnMzY3JldA=="
	found := false
	for _, e := range env {
		if e == want {
			found = true
		}
		if strings.Contains(e, "s3cret") {
			t.Errorf("plaintext credential leaked into env: %s", e)
		}
	}
	if !found {
		t.Errorf("env missing auth header %q", want)
	}
}

func TestAuthEnvSSHAcceptNew(t *testing.T) {
	repo := config.Repo{Auth: config.RepoAuth{Type: "ssh", SSHKeyPath: "/keys/id_ed25519"}}
	for _, e := range authEnv(repo) {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			if !strings.Contains(e, "StrictHostKeyChecking=accept-new") {
				t.Errorf("GIT_SSH_COMMAND = %q, want accept-new host key policy", e)
			}
			return
		}
	}
	t.Error("GIT_SSH_COMMAND not set for ssh auth")
}

func TestRunCancelledReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repos := []config.Repo{{Name: "any", URL: filepath.Join(t.TempDir(), "missing")}}
	results, _, err := newTestRunner(repos).Run(ctx)
	if err == nil {
		t.Fatal("Run: want error when context is cancelled, got nil")
	}
	if results != nil {
		t.Errorf("results = %v, want nil for interrupted scan", results)
	}
}

func TestClassifyScope(t *testing.T) {
	tests := []struct {
		current, latest, want string
	}{
		{"1.0.0", "2.0.0", "major"},
		{"3.5.1", "4.0.0", "major"},
		{"1.0.0", "1.1.0", "minor"},
		{"1.2.3", "1.99.0", "minor"},
		{"1.0.0", "1.0.1", "patch"},
		{"1.0.0", "1.0.42", "patch"},
		{"1.0.0", "1.0.0", ""},        // same version → no bump
		{"1.0.0", "", ""},             // no latest
		{"abc", "def", "unknown"},     // non-semver, different strings
		{"v1.2.3", "v1.2.4", "patch"}, // v-prefixed semver
		{"0.1.0", "0.2.0", "minor"},   // major=0 minor change
		{"0.1.0", "0.1.1", "patch"},   // major=0 patch change
	}

	for _, tt := range tests {
		got := classifyScope(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("classifyScope(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"2.0.0", "1.0.0", true},
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "2.0.0", false},
		{"", "1.0.0", false},  // empty latest → never newer
		{"abc", "def", true},  // non-semver, different strings
		{"abc", "abc", false}, // non-semver, same string
	}

	for _, tt := range tests {
		got := isNewer(tt.latest, tt.current)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}
