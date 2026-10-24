package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"gopkg.in/yaml.v3"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

func (c *IndexCache) getIndex(ctx context.Context, scheme, repoURL string) (*helmIndex, error) {
	return getOrDo(ctx, c, "helm-index", c.indexes, scheme+"://"+repoURL, func(ctx context.Context) (*helmIndex, error) {
		select {
		case c.fetchSem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-c.fetchSem }()

		return c.fetchIndex(ctx, scheme, repoURL)
	})
}

func (c *IndexCache) getTags(ctx context.Context, ref string) ([]string, error) {
	return getOrDo(ctx, c, "oci-tags", c.tags, "oci://"+ref, func(ctx context.Context) ([]string, error) {
		repo, err := name.NewRepository(ref)
		if err != nil {
			return nil, fmt.Errorf("parse OCI ref %q: %w", ref, err)
		}
		tags, err := remote.List(repo, c.remoteOptions(ctx)...)
		if err != nil {
			return nil, fmt.Errorf("list tags for %q: %w", ref, err)
		}
		return tags, nil
	})
}

// Latest returns the latest stable chart version available in the registry.
func Latest(ctx context.Context, cache *IndexCache, protocol, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	latest, err := latest(ctx, cache, protocol, repoURL, chartName, currentVersion, scope)
	return latest, classify(err)
}

func latest(ctx context.Context, cache *IndexCache, protocol, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	if repoURL == "" {
		return "", errkind.Wrap(errkind.Unsupported, fmt.Errorf("repository URL is empty for chart %q", chartName))
	}
	if currentVersion == "" {
		return "", errkind.Wrap(errkind.InvalidVersion, fmt.Errorf("current version is empty for chart %q", chartName))
	}
	switch p := strings.ToLower(protocol); p {
	case "https", "http":
		return latestHTTP(ctx, cache, p, repoURL, chartName, currentVersion, scope)
	case "oci":
		return latestOCI(ctx, cache, repoURL, chartName, currentVersion, scope)
	default:
		return "", errkind.Wrap(errkind.Unsupported, fmt.Errorf("unsupported protocol: %s", protocol))
	}
}

// resolveCurrent turns the version declared in a manifest into a concrete semver.
func resolveCurrent(currentVersion string, tags []string) (*semver.Version, error) {
	if v, err := semver.NewVersion(currentVersion); err == nil {
		return v, nil
	}
	c, err := semver.NewConstraint(currentVersion)
	if err != nil {
		return nil, errkind.Wrap(errkind.InvalidVersion, fmt.Errorf("invalid current version %q: %w", currentVersion, err))
	}
	var best *semver.Version
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil || v.Prerelease() != "" {
			continue
		}
		if c.Check(v) && (best == nil || v.GreaterThan(best)) {
			best = v
		}
	}
	if best == nil {
		return nil, errkind.Wrap(errkind.InvalidVersion, fmt.Errorf("no registry version matches constraint %q", currentVersion))
	}
	return best, nil
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
			// any newer stable version is eligible; `major` in the config is an alias of `all`, not a major-bumps-only filter (see the Scope docs)
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

func (c *IndexCache) fetchIndex(ctx context.Context, scheme, repoURL string) (*helmIndex, error) {
	indexURL := scheme + "://" + strings.TrimSuffix(repoURL, "/") + "/index.yaml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", indexURL, err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", indexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError(indexURL, resp.StatusCode)
	}

	body := &io.LimitedReader{R: resp.Body, N: c.limits.indexBytes + 1}
	var index helmIndex
	err = yaml.NewDecoder(body).Decode(&index)
	// Truncated YAML often still parses cleanly, so the limit must be checked even when decoding succeeded — a silently truncated index would hide chart entries.
	if body.N <= 0 {
		return nil, errkind.Wrap(errkind.TooLarge, fmt.Errorf("index.yaml at %s exceeds the %d byte size limit", indexURL, c.limits.indexBytes))
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
		return "", errkind.Wrap(errkind.NotFound, fmt.Errorf("chart %q not found in index at %s", chartName, strings.TrimSuffix(repoURL, "/")))
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		tags = append(tags, e.Version)
	}
	current, err := resolveCurrent(currentVersion, tags)
	if err != nil {
		return "", err
	}
	return latestFromTags(current, tags, scope), nil
}

func latestOCI(ctx context.Context, cache *IndexCache, repoURL, chartName, currentVersion string, scope Scope) (string, error) {
	ref := strings.TrimSuffix(repoURL, "/") + "/" + chartName
	tags, err := cache.getTags(ctx, ref)
	if err != nil {
		return "", err
	}
	current, err := resolveCurrent(currentVersion, tags)
	if err != nil {
		return "", err
	}
	return latestFromTags(current, tags, scope), nil
}
