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

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// Runner clones repositories and scans them for chart dependencies.
type Runner struct {
	repos          []config.Repo
	newExtractors  func() []extractor.Extractor
	scope          registry.Scope
	parallelChecks int
	gitCacheDir    string
}

// NewRunner creates a Runner that clones the given repos and scans them for outdated dependencies.
func NewRunner(repos []config.Repo, newExtractors func() []extractor.Extractor, scope registry.Scope, parallelChecks int, gitCacheDir string) *Runner {
	return &Runner{
		repos:          repos,
		newExtractors:  newExtractors,
		scope:          scope,
		parallelChecks: parallelChecks,
		gitCacheDir:    gitCacheDir,
	}
}

// Run syncs all repos and returns aggregated results.
func (r *Runner) Run(ctx context.Context) ([]Result, error) {
	workDir, cleanup, err := r.setupWorkspace()
	if err != nil {
		return nil, err
	}
	if cleanup {
		defer func() {
			if err := os.RemoveAll(workDir); err != nil {
				slog.Warn("cleanup failed", "dir", workDir, "error", err)
			}
		}()
	}

	cache := registry.NewIndexCache()

	var (
		results []Result
		mu      sync.Mutex
	)

	runConcurrent(ctx, r.repos, r.parallelChecks, func(ctx context.Context, repo config.Repo) {
		dest := filepath.Join(workDir, safeName(repo.Name))
		slog.Info("syncing repo", "name", repo.Name, "url", repo.URL)

		if err := syncRepo(ctx, repo, dest); err != nil {
			slog.Error("sync failed", "repo", repo.Name, "error", err)
			return
		}

		scanPath := dest
		if repo.Path != "" {
			scanPath = filepath.Join(dest, repo.Path)
		}

		repoResults := r.scanDir(ctx, repo.Name, scanPath, cache)
		slog.Info("scan done", "repo", repo.Name, "results", len(repoResults))

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

type pendingCheck struct {
	source string
	chart  string
	exType string
	ref    extractor.ChartRef
}

func (r *Runner) scanDir(ctx context.Context, source, root string, cache *registry.IndexCache) []Result {
	extractors := r.newExtractors()

	walkYAML := func(fn func(path string, content []byte)) {
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !isYAML(path) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			fn(path, content)
			return nil
		}); err != nil {
			slog.Warn("walk dir failed", "root", root, "error", err)
		}
	}

	// Pass 1: collect cross-file references
	walkYAML(func(path string, content []byte) {
		for _, ex := range extractors {
			if err := ex.PrepareFile(path, content); err != nil {
				slog.Warn("extractor prepare failed", "type", ex.Type(), "file", path, "error", err)
			}
		}
	})

	// Pass 2: extract chart references
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

	// Pass 3: check versions concurrently
	var (
		results []Result
		mu      sync.Mutex
	)
	runConcurrent(ctx, pending, r.parallelChecks, func(ctx context.Context, p pendingCheck) {
		latest, err := registry.Latest(ctx, cache, p.ref.Protocol, p.ref.Repository, p.ref.Name, p.ref.CurrentVersion, r.scope)
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

// ── Git helpers ───────────────────────────────────────────────────────────────

func syncRepo(ctx context.Context, repo config.Repo, dest string) error {
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		return fetchRepo(ctx, repo, dest)
	}
	return cloneRepo(ctx, repo, dest)
}

func cloneRepo(ctx context.Context, repo config.Repo, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", cloneURL(repo), dest)
	cmd.Env = authEnv(repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fetchRepo(ctx context.Context, repo config.Repo, dest string) error {
	fetch := exec.CommandContext(ctx, "git", "-C", dest, "fetch", "--depth=1", "origin")
	fetch.Env = authEnv(repo)
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	reset := exec.CommandContext(ctx, "git", "-C", dest, "reset", "--hard", "FETCH_HEAD")
	if out, err := reset.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cloneURL(repo config.Repo) string {
	u, err := url.Parse(repo.URL)
	if err != nil || u.Scheme == "" {
		return repo.URL
	}
	auth := repo.Auth
	switch auth.Type {
	case "token":
		tok := auth.Token
		if tok == "" && auth.TokenFile != "" {
			tok = readCredFile(auth.TokenFile)
		}
		if tok != "" {
			u.User = url.UserPassword("git", tok)
		}
	case "basic":
		pass := auth.Password
		if pass == "" && auth.PasswordFile != "" {
			pass = readCredFile(auth.PasswordFile)
		}
		if pass != "" {
			u.User = url.UserPassword(auth.Username, pass)
		}
	}
	return u.String()
}

func authEnv(repo config.Repo) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if repo.Auth.Type == "ssh" && repo.Auth.SSHKeyPath != "" {
		env = append(env,
			"GIT_SSH_COMMAND=ssh -i "+repo.Auth.SSHKeyPath+" -o StrictHostKeyChecking=no -o BatchMode=yes",
		)
	}
	return env
}

func readCredFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("credential file unreadable", "path", path, "error", err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── Concurrency ───────────────────────────────────────────────────────────────

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

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}
