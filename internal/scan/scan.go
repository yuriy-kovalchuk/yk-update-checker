package scan

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Masterminds/semver/v3"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/types"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/version"
)

// ============================================================================
// Result
// ============================================================================

// Result is an alias to the shared types.Result
type Result = types.Result

// RepoTarget is a repository to scan.
// Uses types.RepoAuth for credentials.
type RepoTarget struct {
	Name string
	URL  string
	Path string
	Auth types.RepoAuth
}

// cloneURL returns the repository URL with credentials embedded for HTTPS
// clones. SSH URLs are returned unchanged (auth handled via GIT_SSH_COMMAND).
func (rt RepoTarget) cloneURL() string {
	u, err := url.Parse(rt.URL)
	if err != nil || u.Scheme == "" {
		return rt.URL
	}
	switch rt.Auth.Type {
	case "token":
		tok := rt.Auth.Token
		if tok == "" && rt.Auth.TokenFile != "" {
			tok = readCredFile(rt.Auth.TokenFile)
		}
		if tok != "" {
			u.User = url.UserPassword("git", tok)
		}
	case "basic":
		pass := rt.Auth.Password
		if pass == "" && rt.Auth.PasswordFile != "" {
			pass = readCredFile(rt.Auth.PasswordFile)
		}
		if pass != "" {
			u.User = url.UserPassword(rt.Auth.Username, pass)
		}
	}
	return u.String()
}

func readCredFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("credential file unreadable", "path", path, "error", err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

// authEnv returns the environment for git commands. It always sets
// GIT_TERMINAL_PROMPT=0 so git fails fast instead of hanging on missing
// credentials. SSH key auth is injected via GIT_SSH_COMMAND.
func (rt RepoTarget) authEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if rt.Auth.Type == "ssh" && rt.Auth.SSHKeyPath != "" {
		env = append(env,
			"GIT_SSH_COMMAND=ssh -i "+rt.Auth.SSHKeyPath+" -o StrictHostKeyChecking=no -o BatchMode=yes",
		)
	}
	return env
}

// ============================================================================
// Runner
// ============================================================================

// Runner clones repositories and scans them for chart dependencies.
type Runner struct {
	repos          []RepoTarget
	newExtractors  func() []extractor.Extractor
	scope          version.Scope
	parallelChecks int
	gitCacheDir    string
}

// NewRunner creates a Runner. newExtractors is called once per repo scan so
// that stateful extractors receive a fresh instance per repository and
// concurrent scans never share mutable extractor state.
func NewRunner(repos []RepoTarget, newExtractors func() []extractor.Extractor, scope version.Scope, parallelChecks int, gitCacheDir string) *Runner {
	return &Runner{
		repos:          repos,
		newExtractors:  newExtractors,
		scope:          scope,
		parallelChecks: parallelChecks,
		gitCacheDir:    gitCacheDir,
	}
}

// Run syncs all repos, scans them, and returns the aggregated results.
func (r *Runner) Run(ctx context.Context) ([]Result, error) {
	workDir, cleanup, err := r.setupWorkspace()
	if err != nil {
		return nil, err
	}
	if cleanup {
		defer os.RemoveAll(workDir)
	}

	cache := version.NewIndexCache()

	var (
		results []Result
		mu      sync.Mutex
	)

	runConcurrent(ctx, r.repos, r.parallelChecks, func(ctx context.Context, rt RepoTarget) {
		dest := filepath.Join(workDir, safeName(rt.Name))
		slog.Info("syncing repo", "name", rt.Name, "url", rt.URL)

		if err := syncRepo(ctx, rt, dest); err != nil {
			slog.Error("sync failed", "repo", rt.Name, "error", err)
			return
		}

		scanPath := dest
		if rt.Path != "" {
			scanPath = filepath.Join(dest, rt.Path)
		}

		repoResults := r.scanDir(ctx, rt.Name, scanPath, cache)
		slog.Info("scan done", "repo", rt.Name, "results", len(repoResults))

		mu.Lock()
		results = append(results, repoResults...)
		mu.Unlock()
	})

	return results, nil
}

func (r *Runner) setupWorkspace() (workDir string, cleanup bool, err error) {
	if r.gitCacheDir != "" {
		if err := os.MkdirAll(r.gitCacheDir, 0o755); err != nil {
			return "", false, fmt.Errorf("create git cache dir: %w", err)
		}
		return r.gitCacheDir, false, nil
	}
	dir, err := os.MkdirTemp("", "yk-scan-*")
	if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

// ============================================================================
// Directory Scanning
// ============================================================================

// pendingCheck holds everything needed to perform one version lookup.
type pendingCheck struct {
	source string
	chart  string
	exType string
	ref    extractor.ChartRef
}

// scanDir walks root and returns one Result per chart dependency found.
func (r *Runner) scanDir(ctx context.Context, source, root string, cache *version.IndexCache) []Result {
	extractors := r.newExtractors()

	// walkYAML calls fn for each .yaml/.yml file under root, one at a time.
	walkYAML := func(fn func(path string, content []byte)) {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !isYAML(path) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			fn(path, content)
			return nil
		})
	}

	// Pass 1: Prepare - let extractors collect cross-file references
	walkYAML(func(path string, content []byte) {
		for _, ex := range extractors {
			if err := ex.PrepareFile(path, content); err != nil {
				slog.Warn("extractor prepare failed", "type", ex.Type(), "file", path, "error", err)
			}
		}
	})

	// Pass 2: Extract chart references
	var pending []pendingCheck
	walkYAML(func(path string, content []byte) {
		for _, ex := range extractors {
			if !ex.Match(path, content) {
				continue
			}
			chartName, refs, err := ex.Extract(path, content)
			if err != nil {
				slog.Warn("extract failed", "file", path, "type", ex.Type(), "error", err)
				continue
			}
			for _, ref := range refs {
				pending = append(pending, pendingCheck{source: source, chart: chartName, exType: ex.Type(), ref: ref})
			}
		}
	})

	// Pass 3: Check versions concurrently
	var (
		results []Result
		mu      sync.Mutex
	)
	runConcurrent(ctx, pending, r.parallelChecks, func(ctx context.Context, p pendingCheck) {
		latest, err := version.Latest(ctx, cache, p.ref.Protocol, p.ref.Repository, p.ref.Name, p.ref.CurrentVersion, r.scope)
		if err != nil {
			slog.Debug("version check failed", "dep", p.ref.Name, "error", err)
			latest = ""
		}

		chart := p.ref.Chart
		if chart == "" {
			chart = p.chart
		}

		mu.Lock()
		results = append(results, Result{
			Source:          p.source,
			Chart:           chart,
			Dependency:      p.ref.Name,
			Type:            p.exType,
			Protocol:        p.ref.Protocol,
			CurrentVersion:  p.ref.CurrentVersion,
			LatestVersion:   latest,
			Scope:           string(r.scope),
			UpdateAvailable: isNewer(latest, p.ref.CurrentVersion),
			CheckedAt:       time.Now(),
		})
		mu.Unlock()
	})

	return results
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// isNewer reports whether latest represents a strictly greater version than current.
func isNewer(latest, current string) bool {
	if latest == "" {
		return false
	}
	l, err1 := semver.NewVersion(latest)
	c, err2 := semver.NewVersion(current)
	if err1 != nil || err2 != nil {
		return latest != current
	}
	return l.GreaterThan(c)
}

// ============================================================================
// Git Operations
// ============================================================================

func syncRepo(ctx context.Context, rt RepoTarget, dest string) error {
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		return fetchRepo(ctx, rt, dest)
	}
	return cloneRepo(ctx, rt, dest)
}

func cloneRepo(ctx context.Context, rt RepoTarget, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", rt.cloneURL(), dest)
	cmd.Env = rt.authEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fetchRepo(ctx context.Context, rt RepoTarget, dest string) error {
	fetch := exec.CommandContext(ctx, "git", "-C", dest, "fetch", "--depth=1", "origin")
	fetch.Env = rt.authEnv()
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	reset := exec.CommandContext(ctx, "git", "-C", dest, "reset", "--hard", "FETCH_HEAD")
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ============================================================================
// Helpers
// ============================================================================

// runConcurrent fans out fn across items using at most limit goroutines.
func runConcurrent[T any](ctx context.Context, items []T, limit int, fn func(context.Context, T)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		item := item
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(ctx, item)
		}()
	}
	wg.Wait()
}

// safeName converts s into a string safe for use as a directory name.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}
