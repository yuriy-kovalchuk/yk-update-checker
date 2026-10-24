// Package registry resolves the newest versions of Helm charts and container images.
package registry

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"golang.org/x/sync/singleflight"
)

// cached is a memoized fetch outcome; errors too, so a broken registry is asked once per scan.
type cached[V any] struct {
	val V
	err error
}

// IndexCache memoizes registry lookups for the length of one scan.
type IndexCache struct {
	mu      sync.RWMutex
	indexes map[string]cached[*helmIndex]
	tags    map[string]cached[[]string]

	// image look-ups: check outcome, manifest/config metadata, vendor tag reports, referrers and the authenticated client per repository.
	imgChecks  map[string]cached[ImageInfo]
	imgMetas   map[string]cached[imageMeta]
	imgDates   map[string]cached[tagDates]
	imgInfos   map[string]cached[tagInfo]
	imgRefs    map[string]cached[int]
	imgSigs    map[string]cached[sigStatus]
	imgClients map[string]*http.Client

	// verifyTags decides whether winning candidate tags are checked for being real releases before being reported (config: verify_image_tags).
	verifyTags bool

	limits       limits
	transport    http.RoundTripper // policyTransport: every request goes through it
	httpClient   *http.Client      // Helm index.yaml fetches
	vendorClient *http.Client      // anonymous vendor tag APIs (Docker Hub, Quay, Harbor)
	keychain     authn.Keychain
	// vendors overrides the vendor tag API per registry host; hosts absent here fall back to defaultVendorAPIs and then to vendorAPIFor's guess.
	vendors map[string]vendorAPI

	sf       singleflight.Group // one group for every cache below; getOrDo qualifies keys with the store name
	fetchSem chan struct{}
}

// NewIndexCache creates an empty IndexCache.
func NewIndexCache(opts ...CacheOption) *IndexCache {
	c := &IndexCache{
		indexes:    make(map[string]cached[*helmIndex]),
		tags:       make(map[string]cached[[]string]),
		imgChecks:  make(map[string]cached[ImageInfo]),
		imgMetas:   make(map[string]cached[imageMeta]),
		imgDates:   make(map[string]cached[tagDates]),
		imgInfos:   make(map[string]cached[tagInfo]),
		imgRefs:    make(map[string]cached[int]),
		imgSigs:    make(map[string]cached[sigStatus]),
		imgClients: make(map[string]*http.Client),
		verifyTags: true,
		fetchSem:   make(chan struct{}, maxConcurrentIndexFetches),

		limits:    defaultLimits(),
		transport: policyTransport{next: http.DefaultTransport},
		keychain:  authn.DefaultKeychain,
		vendors:   make(map[string]vendorAPI),
	}
	c.httpClient = newHTTPClient(30*time.Second, c.transport)
	c.vendorClient = newHTTPClient(20*time.Second, c.transport)
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// fresh returns the memoized outcome for key, if any.
func fresh[V any](c *IndexCache, store map[string]cached[V], key string) (val V, ok bool, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, exists := store[key]
	return entry.val, exists, entry.err
}

// getOrDo memoizes and singleflight-collapses concurrent misses for the same key.
func getOrDo[V any](ctx context.Context, c *IndexCache, storeName string, store map[string]cached[V], key string, fetch func(context.Context) (V, error)) (V, error) {
	k := storeName + "|" + key
	if val, ok, err := fresh(c, store, k); ok {
		return val, err
	}

	v, err, _ := c.sf.Do(k, func() (any, error) {
		// Re-check: an entry may have been filled while we waited for the key.
		if val, ok, ferr := fresh(c, store, k); ok {
			return val, ferr
		}
		val, ferr := fetch(ctx)
		// Don't memoize cancellation: it says nothing about the registry.
		if !errors.Is(ferr, context.Canceled) && !errors.Is(ferr, context.DeadlineExceeded) {
			c.mu.Lock()
			store[k] = cached[V]{val: val, err: ferr}
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
