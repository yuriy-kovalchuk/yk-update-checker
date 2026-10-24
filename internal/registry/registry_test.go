package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

func TestGetOrFetchDeduplicatesConcurrentFetches(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond) // widen the window so cache misses overlap
		_, _ = w.Write([]byte("entries:\n  app:\n  - version: 1.2.3\n  - version: 1.1.0\n"))
	}))
	defer ts.Close()

	repoURL := strings.TrimPrefix(ts.URL, "https://")
	cache := NewIndexCache(withHTTPClient(ts.Client()))

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			latest, err := Latest(context.Background(), cache, "https", repoURL, "app", "1.0.0", ScopeAll)
			if err != nil {
				t.Errorf("Latest: %v", err)
				return
			}
			if latest != "1.2.3" {
				t.Errorf("latest = %q, want 1.2.3", latest)
			}
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("index fetched %d times for 10 concurrent checks, want 1", got)
	}
}

func TestLatestOverPlainHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("entries:\n  app:\n  - version: 2.0.0\n"))
	}))
	defer ts.Close()

	repoURL := strings.TrimPrefix(ts.URL, "http://")
	latest, err := Latest(context.Background(), NewIndexCache(), "http", repoURL, "app", "1.0.0", ScopeAll)
	if err != nil {
		t.Fatalf("Latest over http: %v", err)
	}
	if latest != "2.0.0" {
		t.Errorf("latest = %q, want 2.0.0", latest)
	}
}

func TestFailedFetchIsNotRetriedWithinScan(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	repoURL := strings.TrimPrefix(ts.URL, "http://")
	cache := NewIndexCache()
	for range 3 {
		if _, err := Latest(context.Background(), cache, "http", repoURL, "app", "1.0.0", ScopeAll); err == nil {
			t.Fatal("Latest: want error from failing registry, got nil")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("failing index fetched %d times, want 1 (negative caching)", got)
	}
}

func TestIndexSizeLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("entries:\n  app:\n"))
		for range 100 {
			_, _ = w.Write([]byte("  - version: 1.0.0\n"))
		}
	}))
	defer ts.Close()

	repoURL := strings.TrimPrefix(ts.URL, "http://")
	cache := NewIndexCache(withLimits(func(l *limits) { l.indexBytes = 256 }))
	_, err := Latest(context.Background(), cache, "http", repoURL, "app", "1.0.0", ScopeAll)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Errorf("err = %v, want size limit error", err)
	}
}

func TestOCITagsAreCached(t *testing.T) {
	// Anonymous auth: the default keychain shells out to docker credential helpers, which can hang in test environments.
	var tagCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/charts/app/tags/list", func(w http.ResponseWriter, _ *http.Request) {
		tagCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"charts/app","tags":["1.0.0","1.1.0","2.0.0-rc.1"]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// go-containerregistry uses plain http for localhost registries.
	host := "localhost:" + strings.Split(ts.Listener.Addr().String(), ":")[1]
	cache := NewIndexCache(withAnonymousAuth())
	for range 3 {
		latest, err := Latest(context.Background(), cache, "oci", host+"/charts", "app", "1.0.0", ScopeAll)
		if err != nil {
			t.Fatalf("Latest over oci: %v", err)
		}
		if latest != "1.1.0" { // 2.0.0-rc.1 is a prerelease and must be skipped
			t.Errorf("latest = %q, want 1.1.0", latest)
		}
	}
	if got := tagCalls.Load(); got != 1 {
		t.Errorf("tags listed %d times for 3 checks, want 1", got)
	}
}

func TestResolveCurrent(t *testing.T) {
	tags := []string{"29.1.0", "29.5.0", "30.0.0", "30.1.0-rc.1", "not-semver"}
	cases := []struct {
		current string
		want    string
		wantErr bool
	}{
		{current: "29.1.0", want: "29.1.0"}, // exact versions pass through untouched
		{current: "29.x", want: "29.5.0"},   // range resolves to newest match
		{current: "~29.1.0", want: "29.1.0"},
		{current: ">=29 <31", want: "30.0.0"}, // 30.1.0-rc.1 is a prerelease
		{current: "99.x", wantErr: true},      // nothing matches
		{current: "garbage version", wantErr: true},
	}
	for _, tc := range cases {
		got, err := resolveCurrent(tc.current, tags)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveCurrent(%q): want error, got %v", tc.current, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveCurrent(%q): %v", tc.current, err)
			continue
		}
		if got.Original() != tc.want {
			t.Errorf("resolveCurrent(%q) = %s, want %s", tc.current, got.Original(), tc.want)
		}
	}
}

func TestLatestWithRangeVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`entries:
  prometheus-operator-crds:
  - version: 29.1.0
  - version: 29.5.0
  - version: 30.0.0
`))
	}))
	defer ts.Close()
	repoURL := strings.TrimPrefix(ts.URL, "http://")

	// Scope all: deployed is 29.5.0 (newest matching 29.x), so 30.0.0 is an update.
	latest, err := Latest(context.Background(), NewIndexCache(), "http", repoURL, "prometheus-operator-crds", "29.x", ScopeAll)
	if err != nil {
		t.Fatalf("Latest(29.x, all): %v", err)
	}
	if latest != "30.0.0" {
		t.Errorf("latest = %q, want 30.0.0", latest)
	}

	// Scope minor: nothing newer within major 29 — up to date.
	latest, err = Latest(context.Background(), NewIndexCache(), "http", repoURL, "prometheus-operator-crds", "29.x", ScopeMinor)
	if err != nil {
		t.Fatalf("Latest(29.x, minor): %v", err)
	}
	if latest != "" {
		t.Errorf("latest = %q, want up to date (empty)", latest)
	}
}

func TestLatestFromTags(t *testing.T) {
	tags := []string{"1.0.0", "1.0.5", "1.2.0", "2.1.0", "3.0.0-rc.1", "not-semver"}
	cases := []struct {
		scope   Scope
		current string
		want    string
	}{
		{ScopeAll, "1.0.0", "2.1.0"},   // prerelease 3.0.0-rc.1 skipped
		{ScopeMajor, "1.0.0", "2.1.0"}, // major behaves like all
		{ScopeMinor, "1.0.0", "1.2.0"}, // stays within major 1
		{ScopePatch, "1.0.0", "1.0.5"}, // stays within 1.0.x
		{ScopeAll, "2.1.0", ""},        // already newest stable
	}
	for _, tc := range cases {
		current, err := semver.NewVersion(tc.current)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.current, err)
		}
		if got := latestFromTags(current, tags, tc.scope); got != tc.want {
			t.Errorf("latestFromTags(%s, scope=%s) = %q, want %q", tc.current, tc.scope, got, tc.want)
		}
	}
}

// TestGetOrDoDoesNotShareKeysAcrossStores covers B8: one group, two stores, no shared in-flight call.
func TestGetOrDoDoesNotShareKeysAcrossStores(t *testing.T) {
	c := NewIndexCache()
	const key = "registry.example.com/app|sha256:0000000000000000000000000000000000000000000000000000000000000000"

	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := getOrDo(context.Background(), c, "tag-info", c.imgInfos, key, func(context.Context) (tagInfo, error) {
			close(entered)
			// Long enough for the second caller to arrive while this call is still in flight, which is the only moment singleflight can share it.
			time.Sleep(300 * time.Millisecond)
			return tagInfo{Digest: "sha256:aaa"}, nil
		}); err != nil {
			t.Errorf("tag-info: %v", err)
		}
	}()

	<-entered // the tag-info call is registered under its key and in flight
	n, err := getOrDo(context.Background(), c, "referrers", c.imgRefs, key, func(context.Context) (int, error) {
		return 7, nil
	})
	<-done

	if err != nil {
		t.Fatalf("referrers: %v", err)
	}
	if n != 7 {
		t.Errorf("referrers = %d, want 7 — its own fetch, not the tag-info call's result", n)
	}
}

// TestScopeMajorBehavesAsScopeAll pins the documented contract: `update_type: major` is an alias of `all`, not a major-bumps-only filter.
func TestScopeMajorBehavesAsScopeAll(t *testing.T) {
	cases := []struct {
		current string
		tags    []string
	}{
		{"1.2.3", []string{"2.0.0", "1.3.0", "1.2.4"}},
		{"2.4.0", []string{"2.4.1", "3.1.0", "2.5.0"}},
		{"1.0.0", []string{"1.0.1"}},
	}
	for _, c := range cases {
		cur, err := semver.NewVersion(c.current)
		if err != nil {
			t.Fatalf("semver.NewVersion(%q): %v", c.current, err)
		}
		all := latestFromTags(cur, c.tags, ScopeAll)
		major := latestFromTags(cur, c.tags, ScopeMajor)
		if all != major {
			t.Errorf("scope major yielded %q where all yielded %q (tags %v from %s): the docs promise these are the same", major, all, c.tags, c.current)
		}
		if all == "" {
			t.Errorf("no candidate for %s from %v", c.current, c.tags)
		}
	}
}

// TestErrorsCarryKinds: every failure Latest reports is classified, so the dashboard can say "auth failed" or "not found" instead of a raw string.
func TestErrorsCarryKinds(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/private/index.yaml":
			w.WriteHeader(http.StatusUnauthorized)
		case "/throttled/index.yaml":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/ok/index.yaml":
			_, _ = w.Write([]byte("entries:\n  app:\n  - version: 1.0.0\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	cases := []struct {
		repo, chart, version string
		want                 errkind.Kind
	}{
		{host + "/private", "app", "1.0.0", errkind.Auth},
		{host + "/throttled", "app", "1.0.0", errkind.RateLimited},
		{host + "/missing", "app", "1.0.0", errkind.NotFound},
		{host + "/ok", "other", "1.0.0", errkind.NotFound}, // chart not in the index
		{host + "/ok", "app", "not-a-version", errkind.InvalidVersion},
		{"127.0.0.1:1/none", "app", "1.0.0", errkind.Unreachable},
	}
	for _, tc := range cases {
		_, err := Latest(context.Background(), NewIndexCache(), "http", tc.repo, tc.chart, tc.version, ScopeAll)
		if got := errkind.Of(err); got != tc.want {
			t.Errorf("%s %s@%s: kind = %q (%v), want %q", tc.repo, tc.chart, tc.version, got, err, tc.want)
		}
	}
	if _, err := Latest(context.Background(), NewIndexCache(), "git", host, "app", "1.0.0", ScopeAll); errkind.Of(err) != errkind.Unsupported {
		t.Errorf("unsupported protocol: kind = %q, want unsupported", errkind.Of(err))
	}
}

func TestImageErrorsCarryKinds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v2/private/app/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	host := "localhost:" + strings.Split(ts.Listener.Addr().String(), ":")[1]

	cache := NewIndexCache(withAnonymousAuth())
	if _, err := cache.LatestImage(context.Background(), host+"/private/app", "1.0.0", "", ScopeAll); errkind.Of(err) != errkind.Auth {
		t.Errorf("403 from the registry: kind = %q (%v), want auth", errkind.Of(err), err)
	}
	if _, err := cache.LatestImage(context.Background(), "127.0.0.1:1/app", "1.0.0", "", ScopeAll); errkind.Of(err) != errkind.Unreachable {
		t.Errorf("closed port: kind = %q (%v), want unreachable", errkind.Of(err), err)
	}
}
