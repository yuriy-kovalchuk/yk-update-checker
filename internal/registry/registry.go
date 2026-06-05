// Package registry resolves the latest available chart version from HTTPS or OCI registries.
package registry

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"gopkg.in/yaml.v3"
)

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

// IndexCache stores parsed Helm index.yaml responses for the duration of a scan.
type IndexCache struct {
	mu    sync.RWMutex
	items map[string]*helmIndex
}

// NewIndexCache creates an empty IndexCache for reuse within a single scan.
func NewIndexCache() *IndexCache {
	return &IndexCache{items: make(map[string]*helmIndex)}
}

func (c *IndexCache) getOrFetch(ctx context.Context, repoURL string) (*helmIndex, error) {
	c.mu.RLock()
	idx, ok := c.items[repoURL]
	c.mu.RUnlock()
	if ok {
		return idx, nil
	}

	idx, err := fetchIndex(ctx, repoURL)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if existing, ok := c.items[repoURL]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	c.items[repoURL] = idx
	c.mu.Unlock()
	return idx, nil
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
	switch strings.ToLower(protocol) {
	case "https":
		return latestHTTPS(ctx, cache, repoURL, chartName, currentVersion, scope)
	case "oci":
		return latestOCI(ctx, repoURL, chartName, currentVersion, scope)
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

func fetchIndex(ctx context.Context, repoURL string) (*helmIndex, error) {
	indexURL := "https://" + strings.TrimSuffix(repoURL, "/") + "/index.yaml"
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
	var index helmIndex
	if err := yaml.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("parse index.yaml: %w", err)
	}
	return &index, nil
}

func latestHTTPS(ctx context.Context, cache *IndexCache, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	index, err := cache.getOrFetch(ctx, repoURL)
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

func latestOCI(ctx context.Context, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	ref := strings.TrimSuffix(repoURL, "/") + "/" + chartName
	repo, err := name.NewRepository(ref)
	if err != nil {
		return "", fmt.Errorf("parse OCI ref %q: %w", ref, err)
	}
	tags, err := remote.List(repo,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithContext(ctx),
	)
	if err != nil {
		return "", fmt.Errorf("list tags for %q: %w", ref, err)
	}
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return "", fmt.Errorf("invalid current version %q: %w", currentVersion, err)
	}
	return latestFromTags(current, tags, scope), nil
}
