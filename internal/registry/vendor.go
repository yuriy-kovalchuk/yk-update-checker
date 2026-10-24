package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

// publicRegistryHosts never speak a vendor tag API, so the date and digest look-ups below skip them.
var publicRegistryHosts = map[string]bool{
	"index.docker.io": true, "registry-1.docker.io": true, "ghcr.io": true,
	"gcr.io": true, "k8s.gcr.io": true, "registry.k8s.io": true,
	"mcr.microsoft.com": true, "public.ecr.aws": true, "data.forgejo.org": true,
}

// apiKind selects the vendor tag API dialect a registry host speaks.
type apiKind string

const (
	apiNone   apiKind = ""       // OCI registry only: no dates, no digests per tag
	apiDocker apiKind = "docker" // hub.docker.com tag listing and tag detail
	apiQuay   apiKind = "quay"   // quay.io api/v1 repository tag endpoint
	apiHarbor apiKind = "harbor" // Harbor v2.0 artifacts API
)

// vendorAPI is the dialect plus the base URL to speak it at.
type vendorAPI struct {
	kind apiKind
	base string
}

// defaultVendorAPIs pins the base URL of every known vendor tag API.
var defaultVendorAPIs = map[string]vendorAPI{
	"index.docker.io":      {kind: apiDocker, base: "https://hub.docker.com"},
	"registry-1.docker.io": {kind: apiDocker, base: "https://hub.docker.com"},
	"quay.io":              {kind: apiQuay, base: "https://quay.io"},
	"quay.redhat.com":      {kind: apiQuay, base: "https://quay.redhat.com"},
}

func vendorAPIFor(scheme, host string) vendorAPI {
	if api, ok := defaultVendorAPIs[host]; ok {
		return api
	}
	if publicRegistryHosts[host] {
		return vendorAPI{kind: apiNone}
	}
	return vendorAPI{kind: apiHarbor, base: scheme + "://" + host}
}

// vendorTarget resolves the vendor API for a repository and the repository path it expects ("library/alpine" for Docker Hub official images).
func (c *IndexCache) vendorTarget(repository string) (vendorAPI, string, error) {
	repo, err := name.NewRepository(repository)
	if err != nil {
		return vendorAPI{}, "", err
	}
	if api, ok := c.vendors[repo.RegistryStr()]; ok {
		return api, repo.RepositoryStr(), nil
	}
	return vendorAPIFor(repo.Scheme(), repo.RegistryStr()), repo.RepositoryStr(), nil
}

// dialect is one vendor tag API.
type dialect interface {
	// listing reports the vendor's view of a repository's most recent tags.
	listing(ctx context.Context, c *IndexCache, base, path string) (map[string]tagInfo, error)
	// tag reports the vendor's view of one tag.
	tag(ctx context.Context, c *IndexCache, base, path, tag string) (tagInfo, error)
}

var dialects = map[apiKind]dialect{
	apiDocker: dockerHub{},
	apiQuay:   quay{},
	apiHarbor: harbor{},
}

// tagInfo is what a vendor tag API reports about one tag. Fields stay zero when the API does not answer them.
type tagInfo struct {
	Pushed  time.Time
	HasDate bool
	Digest  string // as the API reports it, e.g. "sha256:..."
	Size    int64  // packed size in bytes
}

// tagDates is a tag -> vendor report map, with ok saying the registry speaks a vendor tag API at all.
type tagDates struct {
	infos map[string]tagInfo
	ok    bool
}

// bulkTagDates collects a repository's tag dates in one listing plus a query per missing wanted tag.
func (c *IndexCache) bulkTagDates(ctx context.Context, repository string, want ...string) (tagDates, bool) {
	out := tagDates{infos: map[string]tagInfo{}}
	entry, err := getOrDo(ctx, c, "tag-dates", c.imgDates, repository, func(ctx context.Context) (tagDates, error) {
		return c.fetchBulkTagDates(ctx, repository)
	})
	if err == nil {
		for tag, info := range entry.infos {
			out.infos[tag] = info
		}
		out.ok = entry.ok
	}
	for _, tag := range want {
		if _, done := out.infos[tag]; done {
			continue
		}
		info, err := getOrDo(ctx, c, "tag-info", c.imgInfos, repository+"|"+tag, func(ctx context.Context) (tagInfo, error) {
			return c.fetchTagInfo(ctx, repository, tag)
		})
		if err != nil {
			continue
		}
		out.infos[tag] = info
		out.ok = true
	}
	return out, out.ok
}

// fetchBulkTagDates queries the vendor listing API for one registry host.
func (c *IndexCache) fetchBulkTagDates(ctx context.Context, repository string) (tagDates, error) {
	api, path, err := c.vendorTarget(repository)
	if err != nil {
		return tagDates{}, err
	}
	d, ok := dialects[api.kind]
	if !ok {
		return tagDates{}, nil
	}
	infos, err := d.listing(ctx, c, api.base, path)
	if err != nil {
		return tagDates{}, err
	}
	return tagDates{infos: infos, ok: len(infos) > 0}, nil
}

// fetchTagInfo asks the vendor tag API about one specific tag.
func (c *IndexCache) fetchTagInfo(ctx context.Context, repository, tag string) (tagInfo, error) {
	api, path, err := c.vendorTarget(repository)
	if err != nil {
		return tagInfo{}, err
	}
	d, ok := dialects[api.kind]
	if !ok {
		return tagInfo{}, fmt.Errorf("%s speaks no vendor tag API", repository)
	}
	return d.tag(ctx, c, api.base, path, tag)
}

// ── Docker Hub ────────────────────────────────────────────────────────────────

type dockerHub struct{}

// listing pages newest-first, so one page covers the tags worth comparing.
func (dockerHub) listing(ctx context.Context, c *IndexCache, base, path string) (map[string]tagInfo, error) {
	var body struct {
		Results []hubTag `json:"results"`
	}
	u := fmt.Sprintf("%s/v2/repositories/%s/tags?page_size=%d&ordering=last_updated", base, escPath(path), c.limits.hubHarborPageSize)
	if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body); err != nil {
		return nil, err
	}
	out := map[string]tagInfo{}
	for _, t := range body.Results {
		if info := t.info(); info.HasDate {
			out[t.Name] = info
		}
	}
	return out, nil
}

func (dockerHub) tag(ctx context.Context, c *IndexCache, base, path, tag string) (tagInfo, error) {
	var body hubTag
	u := fmt.Sprintf("%s/v2/repositories/%s/tags/%s", base, escPath(path), url.PathEscape(tag))
	if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body); err != nil {
		return tagInfo{}, err
	}
	return body.info(), nil
}

// hubTag is a Docker Hub tag, as both its listing and its tag endpoint report it.
type hubTag struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
	Digest      string    `json:"digest"`
	FullSize    int64     `json:"full_size"`
	Images      []struct {
		Digest string `json:"digest"`
	} `json:"images"`
}

func (t hubTag) info() tagInfo {
	info := tagInfo{Pushed: t.LastUpdated, HasDate: !t.LastUpdated.IsZero(), Digest: t.Digest, Size: t.FullSize}
	if info.Digest == "" && len(t.Images) > 0 {
		// Docker Hub names the per-platform manifests instead of the index, so an alias of a multi-arch release is not always spotted here.
		info.Digest = t.Images[0].Digest
	}
	return info
}

// ── Quay ──────────────────────────────────────────────────────────────────────

type quay struct{}

// listing reads every tag with its digest, packed size and push timestamp, which keeps the alias check cheap.
func (quay) listing(ctx context.Context, c *IndexCache, base, path string) (map[string]tagInfo, error) {
	out := map[string]tagInfo{}
	for page := 1; page <= c.limits.quayListingPages; page++ {
		var body struct {
			Tags          []quayTag `json:"tags"`
			HasAdditional bool      `json:"has_additional"`
		}
		u := fmt.Sprintf("%s/api/v1/repository/%s/tag/?limit=%d&page=%d", base, escPath(path), c.limits.vendorTagPageSize, page)
		if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body); err != nil {
			if page == 1 {
				return nil, err
			}
			break
		}
		for _, t := range body.Tags {
			out[t.Name] = t.info()
		}
		if !body.HasAdditional || len(body.Tags) == 0 {
			break
		}
	}
	return out, nil
}

func (quay) tag(ctx context.Context, c *IndexCache, base, path, tag string) (tagInfo, error) {
	var body struct {
		Tags []quayTag `json:"tags"`
	}
	u := fmt.Sprintf("%s/api/v1/repository/%s/tag/?specificTag=%s", base, escPath(path), url.QueryEscape(tag))
	if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body); err != nil {
		return tagInfo{}, err
	}
	for _, t := range body.Tags {
		if t.Name == tag {
			return t.info(), nil
		}
	}
	return tagInfo{}, fmt.Errorf("%s has no tag %q in %s", base, tag, path)
}

// quayTag is one entry of Quay's api/v1 tag listing.
type quayTag struct {
	Name   string `json:"name"`
	Digest string `json:"manifest_digest"`
	Size   int64  `json:"size"`
	Start  int64  `json:"start_ts"`
}

func (t quayTag) info() tagInfo {
	info := tagInfo{Digest: t.Digest, Size: t.Size}
	if t.Start > 0 {
		info.Pushed, info.HasDate = time.Unix(t.Start, 0).UTC(), true
	}
	return info
}

// ── Harbor ────────────────────────────────────────────────────────────────────

type harbor struct{}

// listing pages newest-first by push time.
func (harbor) listing(ctx context.Context, c *IndexCache, base, path string) (map[string]tagInfo, error) {
	project, repoName, found := strings.Cut(path, "/")
	if !found {
		return nil, nil
	}
	var body []harborArtifact
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=%d&sort=-push_time",
		base, url.PathEscape(project), harborRepoPath(repoName), c.limits.hubHarborPageSize)
	if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body, c.harborAuth(base)); err != nil {
		return nil, err
	}
	out := map[string]tagInfo{}
	for _, a := range body {
		if info := a.info(); info.HasDate {
			for _, t := range a.Tags {
				out[t.Name] = info
			}
		}
	}
	return out, nil
}

func (harbor) tag(ctx context.Context, c *IndexCache, base, path, tag string) (tagInfo, error) {
	project, repoName, found := strings.Cut(path, "/")
	if !found {
		return tagInfo{}, fmt.Errorf("%s: no Harbor project in %q", base, path)
	}
	var body harborArtifact
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts/%s",
		base, url.PathEscape(project), harborRepoPath(repoName), url.PathEscape(tag))
	if _, err := c.getJSON(ctx, c.vendorClient, u, "application/json", &body, c.harborAuth(base)); err != nil {
		return tagInfo{}, err
	}
	return body.info(), nil
}

// harborArtifact is the part of a Harbor v2.0 artifact the checks read.
type harborArtifact struct {
	Digest    string    `json:"digest"`
	PushTime  time.Time `json:"push_time"`
	ExtraInfo struct {
		Digest string `json:"digest"`
	} `json:"extra_info"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

func (a harborArtifact) info() tagInfo {
	info := tagInfo{Pushed: a.PushTime, HasDate: !a.PushTime.IsZero(), Digest: a.Digest}
	if info.Digest == "" {
		info.Digest = a.ExtraInfo.Digest
	}
	return info
}

// harborAuth sends the registry credentials the keychain holds for a Harbor host (a robot account in registry_auth_file) to its artifacts API.
func (c *IndexCache) harborAuth(base string) func(*http.Request) {
	return func(req *http.Request) {
		u, err := url.Parse(base)
		if err != nil || req.URL.Host != u.Host {
			return
		}
		if user, pass, ok := c.basicAuthFor(u.Host); ok {
			req.SetBasicAuth(user, pass)
		}
	}
}

// harborRepoPath encodes a repository name for Harbor's API, which wants the slashes of nested names ("team/app") encoded twice ("team%252Fapp").
func harborRepoPath(repo string) string {
	return url.PathEscape(url.PathEscape(repo))
}

// escPath percent-escapes every segment of a registry path.
func escPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
