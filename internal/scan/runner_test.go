package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

func newTestRunner(repos []config.Repo) *Runner {
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart()}
	}
	return NewRunner(repos, newExtractors, registry.ScopeAll, 2, "")
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
