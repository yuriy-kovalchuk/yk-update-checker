// Package scan walks a repository, checks every reference it finds and reports the outcome per repository.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/extractor"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/gitsync"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// RunnerOptions configures a Runner. Named fields instead of positional arguments: several of them share a type and are easy to swap.
type RunnerOptions struct {
	Repos []config.Repo
	// NewExtractors builds a fresh extractor set per repository: FluxCD keeps cross-file state that must not leak from one repository into the next.
	NewExtractors  func() []extractor.Extractor
	Scope          registry.Scope
	ParallelChecks int
	// GitCacheDir keeps clones between runs; empty uses a temporary workspace that is removed after each Run.
	GitCacheDir string
	// Cache holds fetched registry data for this Run; every scan process builds its own.
	Cache *registry.IndexCache
	// ScanTimeout bounds a whole Run; 0 means no deadline (config scan_timeout).
	ScanTimeout time.Duration
	// GitCommandTimeout bounds each git subprocess. 0 means gitsync.DefaultCommandTimeout; a negative value disables the limit.
	GitCommandTimeout time.Duration
	// MaxFileBytes skips repository files larger than this; 0 means DefaultMaxFileBytes.
	MaxFileBytes int64
	// OnRepo is called once per repository as it finishes; its error fails that repository.
	OnRepo func(repo string, results []Result, err error) error
}

// DefaultMaxFileBytes caps one repository file the scan reads.
const DefaultMaxFileBytes = 10 << 20

// maxConcurrentRepos bounds parallel clones: two git children fit the memory the 512Mi scan pod leaves outside the Go heap.
const maxConcurrentRepos = 2

// Runner clones repositories and scans them for outdated dependencies.
type Runner struct {
	opts   RunnerOptions
	syncer *gitsync.Syncer
}

// NewRunner creates a Runner from opts.
func NewRunner(opts RunnerOptions) *Runner {
	timeout := opts.GitCommandTimeout
	switch {
	case timeout == 0:
		timeout = gitsync.DefaultCommandTimeout
	case timeout < 0:
		timeout = 0
	}
	if opts.Cache == nil {
		opts.Cache = registry.NewIndexCache()
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.ParallelChecks <= 0 {
		opts.ParallelChecks = config.DefaultParallelChecks
	}
	return &Runner{opts: opts, syncer: gitsync.New(timeout)}
}

// RepoError is a repository that could not be synced. Callers use Repo to keep that repository's previous results instead of dropping them.
type RepoError struct {
	Repo string
	Err  error
}

func (e *RepoError) Error() string { return "repo " + e.Repo + ": " + e.Err.Error() }

func (e *RepoError) Unwrap() error { return e.Err }

// Run syncs all repos and returns aggregated results.
func (r *Runner) Run(ctx context.Context) (results []Result, repoErrs []error, err error) {
	if r.opts.ScanTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.ScanTimeout)
		defer cancel()
		// Turn "context deadline exceeded" into something an operator can act on, and make sure the caller sees a failure rather than a truncated scan.
		defer func() {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				slog.Warn("scan timed out",
					"scan_timeout", r.opts.ScanTimeout, "repos_scanned", len(results), "repo_errors", len(repoErrs))
				err = errkind.Wrap(errkind.Timeout, fmt.Errorf("scan exceeded scan_timeout of %s (%d of %d repos completed) "+
					"- raise scan_timeout or set it to 0 to disable", r.opts.ScanTimeout, len(results), len(r.opts.Repos)))
			}
		}()
	}

	workDir, cleanup, err := r.setupWorkspace()
	if err != nil {
		return nil, nil, err
	}
	if cleanup {
		defer func() {
			if err := os.RemoveAll(workDir); err != nil {
				slog.Warn("cleanup failed", "dir", workDir, "error", err)
			}
		}()
	}

	var mu sync.Mutex

	runConcurrent(ctx, r.opts.Repos, maxConcurrentRepos, func(ctx context.Context, repo config.Repo) {
		repoResults, err := r.scanRepo(ctx, workDir, repo)
		if ctx.Err() != nil {
			return // interrupted: not scanned, so its previous report stands
		}
		if r.opts.OnRepo != nil {
			if hookErr := r.opts.OnRepo(repo.Name, repoResults, err); err == nil {
				err = hookErr // a report that was not stored is a failed repository
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			slog.Error("repo failed", "repo", repo.Name, "error", err)
			repoErrs = append(repoErrs, &RepoError{Repo: repo.Name, Err: err})
			return
		}
		results = append(results, repoResults...)
	})

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, fmt.Errorf("scan interrupted: %w", ctxErr)
	}
	if len(repoErrs) == len(r.opts.Repos) {
		return nil, nil, fmt.Errorf("all repos failed to sync: %w", errors.Join(repoErrs...))
	}
	return results, repoErrs, nil
}

// scanRepo syncs one repository and checks every reference in it.
func (r *Runner) scanRepo(ctx context.Context, workDir string, repo config.Repo) ([]Result, error) {
	dest := filepath.Join(workDir, config.SafeName(repo.Name))
	slog.Info("syncing repo", "name", repo.Name, "url", gitsync.RedactURL(repo.URL))
	if err := r.syncer.Sync(ctx, repo, dest); err != nil {
		return nil, err
	}
	repoDir, scanPath, err := scanRoot(dest, repo.Path)
	if err != nil {
		return nil, err
	}
	results := r.scanDir(ctx, repo.Name, repoDir, scanPath)
	slog.Info("scan done", "repo", repo.Name, "results", len(results))
	return results, nil
}

func (r *Runner) setupWorkspace() (workDir string, cleanup bool, err error) {
	if r.opts.GitCacheDir != "" {
		if err := os.MkdirAll(r.opts.GitCacheDir, 0o755); err != nil {
			return "", false, fmt.Errorf("create git cache dir: %w", err)
		}
		return r.opts.GitCacheDir, false, nil
	}
	dir, err := os.MkdirTemp("", "yk-scan-*")
	if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

type pendingCheck struct {
	source string
	path   string // file the reference was found in, relative to the repo checkout
	chart  string
	exType string
	ref    extractor.Ref
}

// scanRoot resolves the directory a scan starts in and refuses one that is missing or escapes the checkout by symlink.
func scanRoot(checkout, sub string) (repoDir, root string, err error) {
	repoDir, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(filepath.Join(checkout, sub))
	if err != nil {
		return "", "", errkind.Wrap(errkind.NotFound, fmt.Errorf("path %q does not exist in the repository", sub))
	}
	if rel, err := filepath.Rel(repoDir, root); err != nil || !filepath.IsLocal(rel) {
		return "", "", errkind.Wrap(errkind.Unsupported, fmt.Errorf("path %q leads outside the repository through a symlink", sub))
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", "", errkind.Wrap(errkind.NotFound, fmt.Errorf("path %q is not a directory in the repository", sub))
	}
	return repoDir, root, nil
}

// scanDir scans root, a directory inside the checkout at repoDir.
func (r *Runner) scanDir(ctx context.Context, source, repoDir, root string) []Result {
	extractors := r.opts.NewExtractors()
	files := collectFiles(root, extractors, r.opts.MaxFileBytes)
	pending := extractChecks(source, repoDir, files, extractors)
	return r.checkAll(ctx, pending)
}

// collectFiles walks root once, reading each scannable file once.
func collectFiles(root string, extractors []extractor.Extractor, maxBytes int64) []*extractor.File {
	var matched []*extractor.File
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("skipping unreadable path", "path", path, "error", err)
			return nil
		}
		if d.IsDir() || !extractor.IsScannable(path) {
			return nil
		}
		if !d.Type().IsRegular() {
			slog.Warn("skipping non-regular file", "path", path, "type", d.Type().String())
			return nil
		}
		content, err := readCapped(path, maxBytes)
		if err != nil {
			slog.Warn("skipping file", "path", path, "error", err)
			return nil
		}
		f := extractor.NewFile(path, content)
		for _, ex := range extractors {
			if err := ex.PrepareFile(f); err != nil {
				slog.Warn("extractor prepare failed", "type", ex.Type(), "file", path, "error", err)
			}
		}
		if slices.ContainsFunc(extractors, func(ex extractor.Extractor) bool { return ex.Match(f) }) {
			matched = append(matched, f)
		}
		return nil
	})
	if err != nil {
		slog.Warn("walk dir failed", "root", root, "error", err)
	}
	return matched
}

// readCapped reads a file of at most maxBytes, refusing a larger one rather than truncating it (a truncated manifest parses into the wrong references).
func readCapped(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("larger than the %d byte limit for a repository file", maxBytes)
	}
	return content, nil
}

// extractChecks runs the matching extractors over files and turns every reference into a pending version check.
func extractChecks(source, repoDir string, files []*extractor.File, extractors []extractor.Extractor) []pendingCheck {
	var pending []pendingCheck
	for i, f := range files {
		rel, err := filepath.Rel(repoDir, f.Path)
		if err != nil {
			rel = f.Path
		}
		for _, ex := range extractors {
			if !ex.Match(f) {
				continue
			}
			chartName, refs, err := ex.Extract(f)
			if err != nil {
				slog.Warn("extract failed", "file", f.Path, "type", ex.Type(), "error", err)
				continue
			}
			for _, ref := range refs {
				pending = append(pending, pendingCheck{source: source, path: filepath.ToSlash(rel), chart: chartName, exType: ex.Type(), ref: ref})
			}
		}
		files[i] = nil
	}
	return pending
}

// checkAll resolves every pending check against its registry, concurrently.
func (r *Runner) checkAll(ctx context.Context, pending []pendingCheck) []Result {
	var (
		results []Result
		mu      sync.Mutex
	)
	runConcurrent(ctx, pending, r.opts.ParallelChecks, func(ctx context.Context, p pendingCheck) {
		var res Result
		if p.ref.Protocol == extractor.ProtocolImage {
			res = r.checkImage(ctx, p, p.label())
		} else {
			res = r.checkChart(ctx, p, p.label())
		}
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
	})
	return results
}

// label is the chart column of a check: the reference's own owner, else the file's chart name.
func (p pendingCheck) label() string {
	chart := p.ref.Chart
	if chart == "" {
		return p.chart
	}
	if p.exType == "dockerfile" {
		// A Dockerfile ref is labelled "<file>/<stage>", unique only inside its own directory.
		if i := strings.LastIndex(p.path, "/"); i >= 0 {
			chart = p.path[:i] + "/" + chart
		}
	}
	return chart
}

// newResult fills the fields every check reports, whatever its outcome.
func newResult(p pendingCheck, chart string) Result {
	return Result{
		Source:         p.source,
		Path:           p.path,
		Chart:          chart,
		Dependency:     p.ref.Name,
		Type:           p.exType,
		Kind:           p.ref.Kind,
		Protocol:       p.ref.Protocol,
		CurrentVersion: p.ref.CurrentVersion,
		PinnedDigest:   p.ref.Digest,
		FinalStage:     p.ref.FinalStage,
		CheckedAt:      metav1.Now(),
	}
}

// checkChart resolves the newest chart version for a Helm/FluxCD reference.
func (r *Runner) checkChart(ctx context.Context, p pendingCheck, chart string) Result {
	res := newResult(p, chart)
	latest, err := registry.Latest(ctx, r.opts.Cache, p.ref.Protocol, p.ref.Repository, p.ref.Name, p.ref.CurrentVersion, r.opts.Scope)
	if err != nil {
		slog.Warn("version check failed", "dep", p.ref.Name, "repo", p.ref.Repository, "error", err)
		res.CheckError, res.CheckErrorKind = err.Error(), errkind.Of(err)
		return res
	}
	res.LatestVersion = latest
	res.Scope = registry.ChartScope(p.ref.CurrentVersion, latest)
	res.UpdateAvailable = registry.IsNewerChart(latest, p.ref.CurrentVersion)
	return res
}

// checkImage resolves the newest comparable tag for a container image.
func (r *Runner) checkImage(ctx context.Context, p pendingCheck, chart string) Result {
	res := newResult(p, chart)
	res.TagKind = string(registry.TagKindOf(p.ref.CurrentVersion))
	info, err := r.opts.Cache.LatestImage(ctx, p.ref.Repository, p.ref.CurrentVersion, p.ref.Digest, r.opts.Scope)
	if err != nil {
		slog.Warn("image check failed", "image", p.ref.Repository, "tag", p.ref.CurrentVersion, "error", err)
		res.CheckError, res.CheckErrorKind = err.Error(), errkind.Of(err)
		return res
	}
	res.TagKind = string(info.Kind)
	res.LatestVersion = info.Latest
	res.CurrentResolved = info.Resolved
	res.ImageDigest = info.Digest
	res.Note = info.Note
	res.Skipped = info.Skipped
	res.CurrentPushedAt = timePtr(info.CurrentPushed)
	res.LatestPushedAt = timePtr(info.LatestPushed)
	res.UpdateAvailable = info.Latest != ""
	// The bump is measured from what the look-up compared against: the version a floating tag resolves to, when it resolved to one.
	base := p.ref.CurrentVersion
	if info.Resolved != "" {
		base = info.Resolved
	}
	res.Scope = registry.ImageScope(base, info.Latest)
	// A reference pinned to a digest that no longer matches what the tag serves is drift even on the very first scan.
	if p.ref.Digest != "" && info.Digest != "" && p.ref.Digest != info.Digest {
		res.DigestChanged = true
	}
	return res
}

// timePtr omits unknown (zero) times from the JSON.
func timePtr(t time.Time) *metav1.Time {
	if t.IsZero() {
		return nil
	}
	return &metav1.Time{Time: t}
}

func runConcurrent[T any](ctx context.Context, items []T, limit int, fn func(context.Context, T)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
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
