package version

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/constants"
	"gopkg.in/yaml.v3"
)

type Scope string

const (
	ScopeAll Scope = "all"
	ScopeMajor Scope = "major"
	ScopeMinor Scope = "minor"
	ScopePatch Scope = "patch"
)

var validScopes = map[Scope]bool{
	ScopeAll:   true,
	ScopeMajor: true,
	ScopeMinor: true,
	ScopePatch: true,
}

func (s Scope) IsValid() bool {
	return validScopes[s]
}

// ParseScope converts a string to a Scope, falling back to ScopeAll for
// unrecognised values. Use this instead of bare Scope(s) casts so that
// the fallback logic lives in one place.
func ParseScope(s string) Scope {
	sc := Scope(s)
	if sc.IsValid() {
		return sc
	}
	return ScopeAll
}

// httpClient is shared across all version checks. The 30-second timeout
// prevents a slow or unresponsive registry from stalling a goroutine
// indefinitely.
var httpClient = &http.Client{Timeout: constants.HTTPClientTimeout}

// IndexCache stores parsed Helm index.yaml responses for the duration of a
// scan so that charts sharing a registry avoid redundant HTTP fetches.
type IndexCache struct {
	mu    sync.RWMutex
	items map[string]*helmIndex
}

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
//
// protocol is "https" or "oci".
// repoURL is the registry base URL with the scheme already stripped.
// chartName is the chart to look up within the registry.
// currentVersion is the currently pinned semver string.
// scope controls which upgrade levels are eligible.
//
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

// latestFromTags returns the highest stable version from tags that is newer
// than current and within the allowed scope.
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
	base := strings.TrimSuffix(repoURL, "/")
	indexURL := "https://" + base + "/index.yaml"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", indexURL, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", indexURL, err)
	}
	defer resp.Body.Close()

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
