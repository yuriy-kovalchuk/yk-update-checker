package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// searchAnchors are the cursors a lookup starts from.
func searchAnchors(curTag, resolved string) []string {
	var out []string
	for _, a := range []string{curTag, resolved, widthProbe(curTag), widthProbe(resolved)} {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// imageTags returns the tags that can compete with the given anchors, and whether the listing was fully consumed.
func (c *IndexCache) imageTags(ctx context.Context, repository string, anchors ...string) ([]string, bool, error) {
	repo, client, err := c.registryClient(ctx, repository)
	if err != nil {
		return nil, false, err
	}
	w := &tagWalk{c: c, client: client, repo: repo, budget: c.limits.pageBudget, seen: map[string]bool{}}

	// The search is exhaustive only if every anchored walk reached the end of the listing: for lexicographic registries the newer tags live after the anchor.
	exhaustive := true
	seenAnchor := map[string]bool{}
	for _, anchor := range anchors {
		for _, a := range []string{anchor, rolloverProbe(anchor)} {
			if a == "" || seenAnchor[a] {
				continue
			}
			seenAnchor[a] = true
			exhaustive = w.walk(ctx, a) && exhaustive
		}
	}
	return w.out, exhaustive, w.firstErr
}

// tagWalk is the state imageTags shares across its anchored walks: the page budget they draw from, the tags collected so far and the first error seen.
type tagWalk struct {
	c        *IndexCache
	client   *http.Client
	repo     name.Repository
	budget   int
	seen     map[string]bool
	out      []string
	firstErr error
}

// walk reads pages starting after anchor, reporting whether it saw the remainder of the listing (registries that ignore n/last always do).
func (w *tagWalk) walk(ctx context.Context, anchor string) (complete bool) {
	pageSize := w.c.limits.tagPageSize
	next := ""
	for page := 0; page < w.c.limits.maxPages && w.budget > 0; page++ {
		w.budget--
		tags, cursor, err := w.c.tagsPage(ctx, w.client, w.repo, anchor, next)
		if err != nil {
			if w.firstErr == nil {
				w.firstErr = err
			}
			return false
		}
		w.collect(tags)
		switch {
		case len(tags) > pageSize:
			return true // registry returned the whole list regardless of n
		case cursor == "":
			return len(tags) < pageSize
		case !sameFamily(tags, anchor):
			return false // the window drifted away from this tag's family
		}
		next = cursor
	}
	return false
}

func (w *tagWalk) collect(tags []string) {
	for _, t := range tags {
		if !w.seen[t] {
			w.seen[t] = true
			w.out = append(w.out, t)
		}
	}
}

// rolloverProbe returns the lexicographic anchor for a wider numeric group: v1.9.1 gives "v1.".
func rolloverProbe(tag string) string {
	type run struct{ start, end int }
	var runs []run
	for i := 0; i < len(tag); i++ {
		if tag[i] < '0' || tag[i] > '9' {
			continue
		}
		j := i
		for j < len(tag) && tag[j] >= '0' && tag[j] <= '9' {
			j++
		}
		runs = append(runs, run{i, j})
		i = j - 1
	}
	switch len(runs) {
	case 0, 1:
		return ""
	case 2:
		return tag[:runs[0].end]
	default:
		return tag[:runs[len(runs)-3].end]
	}
}

// tagsPage reads one page of the OCI tag listing, starting after last, or the page a previous cursor (next) points at.
func (c *IndexCache) tagsPage(ctx context.Context, client *http.Client, repo name.Repository, last, next string) ([]string, string, error) {
	target := next
	if target == "" {
		q := url.Values{"n": {fmt.Sprint(c.limits.tagPageSize)}}
		if last != "" {
			q.Set("last", last)
		}
		target = registryURL(repo, "tags/list") + "?" + q.Encode()
	}

	var body struct {
		Tags []string `json:"tags"`
		Next string   `json:"next"`
	}
	header, err := c.getJSON(ctx, client, target, "application/json", &body)
	if err != nil {
		return nil, "", err
	}
	// Registries differ on where the cursor lives: Quay answers with a Link header, the in-memory reference registry and GHCR use the JSON field.
	cursor := nextLink(header.Get("Link"), target)
	if cursor == "" {
		cursor = body.Next
	}
	return body.Tags, cursor, nil
}

// nextLink extracts the rel="next" URL from an OCI Link header and resolves it against the request it came from.
func nextLink(header, from string) string {
	for _, part := range strings.Split(header, ",") {
		rest, params, found := strings.Cut(strings.TrimSpace(part), ";")
		if !found {
			continue
		}
		if !strings.Contains(params, `rel="next"`) && !strings.Contains(params, "rel=next") {
			continue
		}
		ref := strings.Trim(strings.TrimSpace(rest), "<>")
		if ref == "" {
			return ""
		}
		base, err := url.Parse(from)
		if err != nil {
			return ref
		}
		u, err := url.Parse(ref)
		if err != nil {
			return ref
		}
		return base.ResolveReference(u).String()
	}
	return ""
}

// sameFamily reports whether a page of tags still holds variants of the checked tag, i.e. whether paging further can still find candidates.
func sameFamily(tags []string, curTag string) bool {
	prefix := shapePrefix(classifyTag(curTag))
	if prefix == "" {
		return true
	}
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// registryClient parses repository and returns an HTTP client for its registry's /v2 API.
func (c *IndexCache) registryClient(ctx context.Context, repository string) (name.Repository, *http.Client, error) {
	repo, err := name.NewRepository(repository)
	if err != nil {
		return repo, nil, err
	}
	key := repo.Name()
	c.mu.RLock()
	client, ok := c.imgClients[key]
	c.mu.RUnlock()
	if ok {
		return repo, client, nil
	}

	auth, err := c.keychain.Resolve(repo.Registry)
	if err != nil {
		return repo, nil, err
	}
	rt, err := transport.NewWithContext(ctx, repo.Registry, auth, c.transport, []string{repo.Scope("pull")})
	if err != nil {
		return repo, nil, err
	}
	client = newHTTPClient(30*time.Second, rt)
	c.mu.Lock()
	c.imgClients[key] = client
	c.mu.Unlock()
	return repo, client, nil
}

// registryURL is the /v2/<repository>/<path> endpoint of an OCI registry.
func registryURL(repo name.Repository, path string) string {
	return (&url.URL{Scheme: repo.Scheme(), Host: repo.RegistryStr(), Path: "/v2/" + repo.RepositoryStr() + "/" + path}).String()
}
