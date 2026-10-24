package scan

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

func newTestRunner(repos []config.Repo) *Runner {
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart()}
	}
	return NewRunner(RunnerOptions{
		Repos:          repos,
		NewExtractors:  newExtractors,
		Scope:          registry.ScopeAll,
		ParallelChecks: 2,
		Cache:          registry.NewIndexCache(),
	})
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

// TestRunRepoURLNotParsedAsGitOption pins the "--" terminator: a URL that looks like a git option must reach git as a repository location, not as an option.
func TestRunLogsRedactedRepoURL(t *testing.T) {
	// Built by hand because config.Load rejects a credentialed URL: the one line a scan logs must not carry the login.
	repos := []config.Repo{{Name: "leaky", URL: "https://user:ghp_secret@127.0.0.1:1/none.git"}}
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	_, repoErrs, err := newTestRunner(repos).Run(context.Background())
	// The scan is expected to fail: git refuses the URL itself (transfer.credentialsInUrl=die).
	if err == nil {
		t.Fatal("Run accepted a credentialed URL; git's own refusal stopped firing")
	}
	out := buf.String()
	if strings.Contains(out, "ghp_secret") {
		t.Errorf("the scan log carries the URL credential: %s", out)
	}
	if strings.Contains(err.Error(), "ghp_secret") {
		t.Errorf("the scan error carries the URL credential: %v", err)
	}
	for _, e := range repoErrs {
		if strings.Contains(e.Error(), "ghp_secret") {
			t.Errorf("repo error (visible on /api/status) carries the URL credential: %v", e)
		}
	}
	if !strings.Contains(out, "url=https://127.0.0.1:1/none.git") {
		t.Errorf("want the credential-free URL logged, got: %s", out)
	}
}

func TestRunRepoURLNotParsedAsGitOption(t *testing.T) {
	good := initGitRepo(t, map[string]string{
		"Chart.yaml": "name: sample\nversion: 1.0.0\n",
	})
	repos := []config.Repo{
		{Name: "good", URL: good},
		{Name: "inj", URL: "-cuploadpack=touch /tmp/yk-uc-pwned"},
	}

	_, repoErrs, err := newTestRunner(repos).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(repoErrs) != 1 {
		t.Fatalf("repoErrs = %v, want exactly 1", repoErrs)
	}
	msg := repoErrs[0].Error()
	// With the terminator, git names the URL itself as the missing repository.
	if !strings.Contains(msg, "yk-uc-pwned") {
		t.Errorf("repoErrs[0] = %v, want git to treat the URL as a repository positional", msg)
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

// newTestRunnerDeadline is newTestRunner with a whole-scan deadline and the per-git-command limit switched off, so only the scan deadline can fire.
func newTestRunnerDeadline(repos []config.Repo, timeout time.Duration) *Runner {
	newExtractors := func() []extractor.Extractor {
		return []extractor.Extractor{extractor.NewHelmChart()}
	}
	return NewRunner(RunnerOptions{
		Repos:             repos,
		NewExtractors:     newExtractors,
		Scope:             registry.ScopeAll,
		ParallelChecks:    2,
		Cache:             registry.NewIndexCache(),
		ScanTimeout:       timeout,
		GitCommandTimeout: -1,
	})
}

// hangingGit installs a stand-in git that never returns and returns a repo URL for it to be pointed at.
func hangingGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return "https://example.invalid/wedge.git"
}

// TestRunAbortsWhenGitCommandTimesOut covers the per-subprocess limit: one hung clone must fail its repo instead of holding the whole scan open forever.
func TestRunAbortsWhenGitCommandTimesOut(t *testing.T) {
	repos := []config.Repo{{Name: "wedge", URL: hangingGit(t)}}
	runner := NewRunner(RunnerOptions{
		Repos:             repos,
		NewExtractors:     func() []extractor.Extractor { return []extractor.Extractor{extractor.NewHelmChart()} },
		Scope:             registry.ScopeAll,
		ParallelChecks:    2,
		GitCommandTimeout: 300 * time.Millisecond,
	})
	start := time.Now()
	_, _, err := runner.Run(context.Background())
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("Run took %s - the per-git limit never fired", elapsed)
	}
	if err == nil {
		t.Fatal("a scan whose only repo hung must not report success")
	}
	// Every repo failed, so Run folds the per-repo errors into one error.
	msg := err.Error()
	if !strings.Contains(msg, "git") {
		t.Errorf("err = %v, want it to name the git subprocess that was cut off", msg)
	}
	if !strings.Contains(msg, "deadline exceeded") && !strings.Contains(msg, "killed") {
		t.Errorf("err = %v, want the git subprocess to have been cut off by its own deadline", msg)
	}
}

// TestRunAbortsWhenScanDeadlineExpires: Run must return an actionable error, not stay "scanning" forever.
func TestRunAbortsWhenScanDeadlineExpires(t *testing.T) {
	repos := []config.Repo{{Name: "wedge", URL: hangingGit(t)}}
	_, _, err := newTestRunnerDeadline(repos, 2*time.Second).Run(context.Background())
	if err == nil {
		t.Fatal("a scan that outlived scan_timeout must report failure, not a truncated success")
	}
	if !strings.Contains(err.Error(), "scan_timeout") {
		t.Errorf("err = %v, want it to name scan_timeout so the fix is obvious", err)
	}
}

func TestRunReportsFailedReposByName(t *testing.T) {
	good := initGitRepo(t, map[string]string{"Chart.yaml": "name: sample\nversion: 1.0.0\n"})
	repos := []config.Repo{
		{Name: "good", URL: good},
		{Name: "bad", URL: filepath.Join(t.TempDir(), "missing")},
	}
	_, repoErrs, err := newTestRunner(repos).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := kindsOf(repoErrs); len(got) != 1 || got["bad"] != errkind.NotFound {
		t.Errorf("repo failures = %v, want bad=not_found", got)
	}
}

// TestOnRepoReportsEveryRepository: the hook sees each repo once, with its error kind, even when all fail.
func TestOnRepoReportsEveryRepository(t *testing.T) {
	var mu sync.Mutex
	kinds := map[string]errkind.Kind{}
	r := NewRunner(RunnerOptions{
		Repos: []config.Repo{
			{Name: "one", URL: filepath.Join(t.TempDir(), "missing")},
			{Name: "two", URL: "https://host.invalid/x.git"},
		},
		NewExtractors:  func() []extractor.Extractor { return nil },
		ParallelChecks: 2,
		OnRepo: func(repo string, _ []Result, err error) error {
			mu.Lock()
			kinds[repo] = errkind.Of(err)
			mu.Unlock()
			return nil
		},
	})
	if _, _, err := r.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded with no reachable repository")
	}
	if kinds["one"] != errkind.NotFound || kinds["two"] != errkind.Unreachable {
		t.Errorf("OnRepo saw %v, want one=not_found two=unreachable", kinds)
	}
}

// kindsOf maps each failed repo in errs to its error kind.
func kindsOf(errs []error) map[string]errkind.Kind {
	out := map[string]errkind.Kind{}
	for _, err := range errs {
		var re *RepoError
		if errors.As(err, &re) {
			out[re.Repo] = errkind.Of(re.Err)
		}
	}
	return out
}

// TestRunResultsCarryManifestKind: metrics, alerts and the dashboard all read the kind from the result.
func TestRunResultsCarryManifestKind(t *testing.T) {
	const manifest = "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: db\nspec:\n  template:\n    spec:\n      containers:\n        - name: db\n          image: 127.0.0.1:1/db:1.0.0\n"
	dir := initGitRepo(t, map[string]string{"db.yaml": manifest})
	runner := NewRunner(RunnerOptions{
		Repos:          []config.Repo{{Name: "r", URL: dir}},
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeAll,
		ParallelChecks: 1,
	})
	results, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Kind != "StatefulSet" {
		t.Fatalf("results = %+v, want one result with Kind StatefulSet", results)
	}
	// The image's registry (port 1) refuses connections: the check fails, and says why in a form the dashboard and metrics can use.
	if results[0].CheckErrorKind != errkind.Unreachable {
		t.Errorf("CheckErrorKind = %q (%s), want unreachable", results[0].CheckErrorKind, results[0].CheckError)
	}
}

// imagesRunner scans with the image extractor only; the images point at a closed port, so every check fails fast but still produces a result.
func imagesRunner(repos []config.Repo, maxBytes int64) *Runner {
	return NewRunner(RunnerOptions{
		Repos:          repos,
		NewExtractors:  func() []extractor.Extractor { return []extractor.Extractor{extractor.NewImages()} },
		Scope:          registry.ScopeAll,
		ParallelChecks: 2,
		MaxFileBytes:   maxBytes,
	})
}

func deployment(name, image string) string {
	return "kind: Deployment\nmetadata:\n  name: " + name + "\nspec:\n  template:\n    spec:\n      containers:\n        - name: c\n          image: " + image + "\n"
}

// TestRunDoesNotFollowSymlinksOutOfTheCheckout: a committed symlink must not make the scan read a file outside the repository (or /dev/zero).
func TestRunDoesNotFollowSymlinksOutOfTheCheckout(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "private.yaml")
	if err := os.WriteFile(outside, []byte(deployment("leaked", "127.0.0.1:1/outside:1.0.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := initGitRepo(t, map[string]string{"ok.yaml": deployment("ok", "127.0.0.1:1/inside:1.0.0")})
	for _, link := range []struct{ name, target string }{{"link.yaml", outside}, {"zero.yaml", "/dev/zero"}} {
		if err := os.Symlink(link.target, filepath.Join(dir, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, dir)

	results, _, err := imagesRunner([]config.Repo{{Name: "r", URL: dir}}, 0).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Dependency != "127.0.0.1:1/inside" {
		t.Errorf("results = %+v, want only the in-repo image", results)
	}
}

func TestRunSkipsOversizedFiles(t *testing.T) {
	big := deployment("big", "127.0.0.1:1/big:1.0.0") + "# " + strings.Repeat("x", 4096) + "\n"
	dir := initGitRepo(t, map[string]string{
		"big.yaml":   big,
		"small.yaml": deployment("small", "127.0.0.1:1/small:1.0.0"),
	})
	results, _, err := imagesRunner([]config.Repo{{Name: "r", URL: dir}}, 1024).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Dependency != "127.0.0.1:1/small" {
		t.Errorf("results = %+v, want the file over the cap skipped", results)
	}
}

// TestRunReportsBadScanPath: a `path:` that is missing or escapes the checkout is a repo failure, not an empty success.
func TestRunReportsBadScanPath(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"apps/ok.yaml": deployment("ok", "127.0.0.1:1/inside:1.0.0")})
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dir)

	repos := []config.Repo{
		{Name: "good", URL: dir, Path: "apps"},
		{Name: "typo", URL: dir, Path: "aps"},
		{Name: "escape", URL: dir, Path: "escape"},
	}
	results, repoErrs, err := imagesRunner(repos, 0).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("results = %+v, want the one image under apps/", results)
	}
	kinds := kindsOf(repoErrs)
	if kinds["typo"] != errkind.NotFound || kinds["escape"] != errkind.Unsupported || len(kinds) != 2 {
		t.Errorf("repo failures = %v, want typo=not_found escape=unsupported", kinds)
	}
}

// gitCommitAll commits whatever a test added to a repo after initGitRepo.
func gitCommitAll(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "more"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestOnRepoErrorFailsTheRepository: a report that was not stored must not look like a success.
func TestOnRepoErrorFailsTheRepository(t *testing.T) {
	dir := initGitRepo(t, map[string]string{"Chart.yaml": "name: sample\nversion: 1.0.0\n"})
	r := NewRunner(RunnerOptions{
		Repos:         []config.Repo{{Name: "r", URL: dir}},
		NewExtractors: func() []extractor.Extractor { return []extractor.Extractor{extractor.NewHelmChart()} },
		OnRepo:        func(string, []Result, error) error { return errors.New("report write refused") },
	})
	_, _, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "report write refused") {
		t.Errorf("Run = %v, want the write failure to fail the scan", err)
	}
}

// TestCancelledRunReportsNothing: an interrupted repo is "not scanned", not "failed".
func TestCancelledRunReportsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	r := NewRunner(RunnerOptions{
		Repos:         []config.Repo{{Name: "r", URL: filepath.Join(t.TempDir(), "missing")}},
		NewExtractors: func() []extractor.Extractor { return nil },
		OnRepo:        func(string, []Result, error) error { called = true; return nil },
	})
	if _, _, err := r.Run(ctx); err == nil {
		t.Fatal("Run succeeded on a cancelled context")
	}
	if called {
		t.Error("OnRepo was called for a repository the cancelled run never scanned")
	}
}
