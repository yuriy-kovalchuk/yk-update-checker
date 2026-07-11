// Package registry resolves the latest available chart version from HTTP(S) or OCI registries.
package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sync/singleflight"
	"gopkg.in/yaml.v3"
)

// maxConcurrentIndexFetches bounds how many index.yaml downloads are parsed at
// once. Decoding a large index transiently needs ~15x its size in heap, so
// unbounded concurrency OOMs the pod on big repositories.
const maxConcurrentIndexFetches = 2

// maxIndexBytes caps how large an index.yaml the checker is willing to parse
// (a var so tests can lower it).
var maxIndexBytes = int64(50 << 20) // 50 MB

// Scope controls which upgrade levels are eligible.
type Scope string

// Eligible upgrade levels for version comparisons.
const (
	ScopeAll   Scope = "all"
	ScopeMajor Scope = "major"
	ScopeMinor Scope = "minor"
	ScopePatch Scope = "patch"
)

var validScopes = map[Scope]bool{
	ScopeAll: true, ScopeMajor: true, ScopeMinor: true, ScopePatch: true,
}

// IsValid reports whether s is a recognised scope value.
func (s Scope) IsValid() bool { return validScopes[s] }

// ParseScope converts a string to a Scope, falling back to ScopeAll.
func ParseScope(s string) Scope {
	sc := Scope(s)
	if sc.IsValid() {
		return sc
	}
	return ScopeAll
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// cached is a memoized fetch outcome; errors are stored too so a broken
// registry is not re-fetched for every dependency that references it.
type cached[V any] struct {
	val V
	err error
}

// IndexCache stores fetched registry data (parsed Helm indexes and OCI tag
// lists) for the duration of a scan.
type IndexCache struct {
	mu      sync.RWMutex
	indexes map[string]cached[*helmIndex]
	tags    map[string]cached[[]string]

	sf       singleflight.Group
	fetchSem chan struct{}
}

// NewIndexCache creates an empty IndexCache for reuse within a single scan.
func NewIndexCache() *IndexCache {
	return &IndexCache{
		indexes:  make(map[string]cached[*helmIndex]),
		tags:     make(map[string]cached[[]string]),
		fetchSem: make(chan struct{}, maxConcurrentIndexFetches),
	}
}

// getOrDo returns the memoized outcome for key, collapsing concurrent misses
// for the same key into a single fetch (singleflight); without it every worker
// referencing the same repo fetches and decodes its own copy simultaneously.
func getOrDo[V any](ctx context.Context, c *IndexCache, store map[string]cached[V], key string, fetch func(context.Context) (V, error)) (V, error) {
	c.mu.RLock()
	entry, ok := store[key]
	c.mu.RUnlock()
	if ok {
		return entry.val, entry.err
	}

	v, err, _ := c.sf.Do(key, func() (any, error) {
		c.mu.RLock()
		entry, ok := store[key]
		c.mu.RUnlock()
		if ok {
			return entry.val, entry.err
		}

		val, ferr := fetch(ctx)
		// Don't memoize cancellation: it says nothing about the registry.
		if !errors.Is(ferr, context.Canceled) && !errors.Is(ferr, context.DeadlineExceeded) {
			c.mu.Lock()
			store[key] = cached[V]{val: val, err: ferr}
			c.mu.Unlock()
		}
		return val, ferr
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return v.(V), nil
}

func (c *IndexCache) getIndex(ctx context.Context, scheme, repoURL string) (*helmIndex, error) {
	return getOrDo(ctx, c, c.indexes, scheme+"://"+repoURL, func(ctx context.Context) (*helmIndex, error) {
		select {
		case c.fetchSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-c.fetchSem }()

		return fetchIndex(ctx, scheme, repoURL)
	})
}

// ociAuth is the authenticator used for OCI registries (a var so tests can
// avoid shelling out to docker credential helpers).
var ociAuth = remote.WithAuthFromKeychain(authn.DefaultKeychain)

func (c *IndexCache) getTags(ctx context.Context, ref string) ([]string, error) {
	return getOrDo(ctx, c, c.tags, "oci://"+ref, func(ctx context.Context) ([]string, error) {
		repo, err := name.NewRepository(ref)
		if err != nil {
			return nil, fmt.Errorf("parse OCI ref %q: %w", ref, err)
		}
		tags, err := remote.List(repo, ociAuth, remote.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("list tags for %q: %w", ref, err)
		}
		return tags, nil
	})
}

// Latest returns the latest stable chart version available in the registry.
// Returns an empty string (no error) when no eligible update exists.
func Latest(ctx context.Context, cache *IndexCache, protocol, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	if repoURL == "" {
		return "", fmt.Errorf("repository URL is empty for chart %q", chartName)
	}
	if currentVersion == "" {
		return "", fmt.Errorf("current version is empty for chart %q", chartName)
	}
	switch p := strings.ToLower(protocol); p {
	case "https", "http":
		return latestHTTP(ctx, cache, p, repoURL, chartName, currentVersion, scope)
	case "oci":
		return latestOCI(ctx, cache, repoURL, chartName, currentVersion, scope)
	default:
		return "", fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func latestFromTags(current *semver.Version, tags []string, scope Scope) string {
	var candidates []*semver.Version
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil || v.Prerelease() != "" {
			continue
		}
		if !v.GreaterThan(current) {
			continue
		}
		switch scope {
		case ScopePatch:
			if v.Major() != current.Major() || v.Minor() != current.Minor() {
				continue
			}
		case ScopeMinor:
			if v.Major() != current.Major() {
				continue
			}
		case ScopeAll, ScopeMajor:
			// any newer stable version is eligible
		}
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Sort(semver.Collection(candidates))
	return candidates[len(candidates)-1].Original()
}

type helmIndexEntry struct {
	Version string `yaml:"version"`
}

type helmIndex struct {
	Entries map[string][]helmIndexEntry `yaml:"entries"`
}

func fetchIndex(ctx context.Context, scheme, repoURL string) (*helmIndex, error) {
	indexURL := scheme + "://" + strings.TrimSuffix(repoURL, "/") + "/index.yaml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", indexURL, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", indexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d", indexURL, resp.StatusCode)
	}

	body := &io.LimitedReader{R: resp.Body, N: maxIndexBytes + 1}
	var index helmIndex
	err = yaml.NewDecoder(body).Decode(&index)
	// Truncated YAML often still parses cleanly, so the limit must be checked
	// even when decoding succeeded — a silently truncated index would hide
	// chart entries.
	if body.N <= 0 {
		return nil, fmt.Errorf("index.yaml at %s exceeds the %d byte size limit", indexURL, maxIndexBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("parse index.yaml: %w", err)
	}
	return &index, nil
}

func latestHTTP(ctx context.Context, cache *IndexCache, scheme, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	index, err := cache.getIndex(ctx, scheme, repoURL)
	if err != nil {
		return "", err
	}
	entries, ok := index.Entries[chartName]
	if !ok {
		return "", fmt.Errorf("chart %q not found in index at %s", chartName, strings.TrimSuffix(repoURL, "/"))
	}
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return "", fmt.Errorf("invalid current version %q: %w", currentVersion, err)
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		tags = append(tags, e.Version)
	}
	return latestFromTags(current, tags, scope), nil
}

func latestOCI(ctx context.Context, cache *IndexCache, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	ref := strings.TrimSuffix(repoURL, "/") + "/" + chartName
	tags, err := cache.getTags(ctx, ref)
	if err != nil {
		return "", err
	}
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return "", fmt.Errorf("invalid current version %q: %w", currentVersion, err)
	}
	return latestFromTags(current, tags, scope), nil
}
