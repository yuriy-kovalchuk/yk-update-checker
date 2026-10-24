package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

// limits bounds what one IndexCache fetches and decodes.
type limits struct {
	// indexBytes caps an index.yaml.
	indexBytes int64
	// jsonBytes caps any registry JSON response. Tag listings are paged and small, so anything near this is a hostile or compromised registry.
	jsonBytes int64

	tagPageSize       int // OCI tags/list?n= (GHCR honours 1000, Quay clamps to 100 with a cursor)
	maxPages          int // cursor pages followed per anchor before giving up
	pageBudget        int // tag-list requests per image lookup, shared by all anchors
	dateCandidates    int // metadata look-ups for date-based comparison
	vendorTagPageSize int // vendor tag API page size
	quayListingPages  int // Quay tag-listing pages per image lookup
	hubHarborPageSize int // Docker Hub and Harbor API page_size; both cap it at 100
	verifyCandidates  int // winning candidates one image look-up verifies, best first

	// pushGapSignal is how much older than the current tag a candidate's last push must be before the push date counts against it.
	pushGapSignal time.Duration
}

func defaultLimits() limits {
	return limits{
		indexBytes:        20 << 20,
		jsonBytes:         10 << 20,
		tagPageSize:       1000,
		maxPages:          3,
		pageBudget:        8,
		dateCandidates:    15,
		vendorTagPageSize: 1000,
		quayListingPages:  4,
		hubHarborPageSize: 100,
		verifyCandidates:  3,
		pushGapSignal:     90 * 24 * time.Hour,
	}
}

// maxConcurrentIndexFetches bounds how many index.yaml downloads are decoded at once. See limits.indexBytes for why it is one.
const maxConcurrentIndexFetches = 1

// CacheOption tunes an IndexCache without changing the constructor's arity.
type CacheOption func(*IndexCache)

// WithVerifyTags turns candidate tag verification on or off (on by default).
func WithVerifyTags(on bool) CacheOption {
	return func(c *IndexCache) { c.verifyTags = on }
}

// userAgent identifies the checker to every registry it talks to.
const userAgent = "yk-update-checker"

// maxRedirects is how many hops any registry request may follow.
const maxRedirects = 5

// newHTTPClient is the one way this package builds an HTTP client, so the timeout and the request policy are the same for all of them.
func newHTTPClient(timeout time.Duration, rt http.RoundTripper) *http.Client {
	if rt == nil {
		rt = policyTransport{next: http.DefaultTransport}
	}
	return &http.Client{Timeout: timeout, Transport: rt}
}

// policyTransport enforces the request policy in a transport, the only hook go-containerregistry accepts.
type policyTransport struct{ next http.RoundTripper }

func (t policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkRedirectChain(req); err != nil {
		return nil, err
	}
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", userAgent)
	}
	return t.next.RoundTrip(req)
}

// checkRedirectChain walks back from a redirected request to the first one.
func checkRedirectChain(req *http.Request) error {
	hops, first := 0, req
	for first.Response != nil && first.Response.Request != nil {
		hops++
		first = first.Response.Request
	}
	if hops == 0 {
		return nil
	}
	if hops > maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if first.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// httpStatusError reports a non-200 answer, classified by its status code.
func httpStatusError(u string, code int) error {
	return errkind.Wrap(errkind.FromHTTPStatus(code), fmt.Errorf("%s: HTTP %d", u, code))
}

// classify gives an error leaving this package a kind: the attached one, else a transport status, else the network.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var te *transport.Error
	if errors.As(err, &te) && te.StatusCode != 0 {
		return errkind.Wrap(errkind.FromHTTPStatus(te.StatusCode), err)
	}
	var badName *name.ErrBadName
	if errors.As(err, &badName) {
		return errkind.Wrap(errkind.Unsupported, err)
	}
	return errkind.Wrap(errkind.Of(err), err)
}

// remoteOptions are what every go-containerregistry call uses: the caller's context, the cache's keychain, and the policy transport.
func (c *IndexCache) remoteOptions(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
		remote.WithTransport(c.transport),
	}
}

// getJSON GETs u and decodes a 200 response into out, returning the response headers (the OCI tag listing keeps its paging cursor there).
func (c *IndexCache) getJSON(ctx context.Context, client *http.Client, u, accept string, out any, edits ...func(*http.Request)) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	for _, edit := range edits {
		edit(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError(u, resp.StatusCode)
	}
	body := &io.LimitedReader{R: resp.Body, N: c.limits.jsonBytes + 1}
	err = json.NewDecoder(body).Decode(out)
	// The limit is checked even on a successful decode, as in fetchIndex: JSON truncated by the cap can still leave a partially populated value behind.
	if body.N <= 0 {
		return resp.Header, errkind.Wrap(errkind.TooLarge, fmt.Errorf("%s exceeds the %d byte size limit", u, c.limits.jsonBytes))
	}
	return resp.Header, err
}
