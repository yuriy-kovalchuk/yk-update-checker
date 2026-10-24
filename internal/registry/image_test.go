package registry

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ociregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// recordingRegistry serves an in-memory OCI registry and records every request path, so tests can assert that no image layer is ever downloaded.
type recordingRegistry struct {
	mu      sync.Mutex
	paths   []string
	handler http.Handler
}

func (r *recordingRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	r.handler.ServeHTTP(w, req)
}

func (r *recordingRegistry) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = nil
}

func (r *recordingRegistry) requestPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

func newTestRegistry(t *testing.T) (host string, reg *recordingRegistry) {
	t.Helper()
	// The in-memory registry logs every request to stderr; tests assert on the recorded paths instead.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })
	rec := &recordingRegistry{handler: ociregistry.New()}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return u.Host, rec
}

func writeImage(t *testing.T, host, repository, tag, version string, created time.Time) v1.Layer {
	t.Helper()
	ctx := context.Background()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	cf.Created = v1.Time{Time: created}
	if version != "" {
		cf.Config.Labels = map[string]string{labelVersion: version}
	}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatalf("mutate config: %v", err)
	}
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repository, tag), name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag: %v", err)
	}
	if err := remote.Write(ref, img, remote.WithContext(ctx)); err != nil {
		t.Fatalf("write %s:%s: %v", repository, tag, err)
	}
	layers, err := img.Layers()
	if err != nil || len(layers) == 0 {
		t.Fatalf("layers: %v", err)
	}
	return layers[0]
}

func assertNoLayerFetched(t *testing.T, reg *recordingRegistry, layer v1.Layer) {
	t.Helper()
	dgst, err := layer.Digest()
	if err != nil {
		t.Fatalf("layer digest: %v", err)
	}
	diff, err := layer.DiffID()
	if err != nil {
		t.Fatalf("layer diff id: %v", err)
	}
	var blobs int
	for _, p := range reg.requestPaths() {
		if strings.Contains(p, dgst.Hex) || strings.Contains(p, diff.Hex) {
			t.Errorf("layer blob was requested: %s", p)
		}
		if strings.Contains(p, "/blobs/") {
			blobs++
		}
	}
	if blobs == 0 {
		t.Error("expected at least one config blob request")
	}
}

func TestLatestImageSemver(t *testing.T) {
	host, _ := newTestRegistry(t)
	for i, tag := range []string{"1.0.0", "1.1.0", "1.2.0", "2.0.0", "1.2.0-rc1"} {
		writeImage(t, host, "app", tag, tag, time.Unix(int64(1700000000+i*86400), 0))
	}
	cache := NewIndexCache()

	info, err := cache.LatestImage(context.Background(), host+"/app", "1.0.0", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	if info.Latest != "1.2.0" {
		t.Errorf("Latest = %q, want 1.2.0 (2.0.0 out of minor scope, rc filtered)", info.Latest)
	}
	if info.Kind != TagSemver {
		t.Errorf("Kind = %q, want semver", info.Kind)
	}
	// Version tags are immutable per tag, so no metadata request is made for them and no digest is reported; only floating/opaque tags need drift.
	if info.Digest != "" {
		t.Errorf("Digest = %q, want none for an immutable version tag", info.Digest)
	}

	full, err := cache.LatestImage(context.Background(), host+"/app", "1.0.0", "", ScopeAll)
	if err != nil {
		t.Fatalf("LatestImage(all): %v", err)
	}
	if full.Latest != "2.0.0" {
		t.Errorf("Latest at ScopeAll = %q, want 2.0.0", full.Latest)
	}
}

func TestLatestImageFloatingTagResolvesVersion(t *testing.T) {
	host, reg := newTestRegistry(t)
	layer := writeImage(t, host, "homepage", "latest", "v2.6.0", time.Unix(1760000000, 0))
	writeImage(t, host, "homepage", "v2.6.0", "v2.6.0", time.Unix(1760000000, 0))
	writeImage(t, host, "homepage", "v2.4.0", "v2.4.0", time.Unix(1740000000, 0))
	writeImage(t, host, "homepage", "v2.7.1", "v2.7.1", time.Unix(1770000000, 0))
	reg.reset() // the push phase HEADs layer blobs; the guard is about check traffic

	cache := NewIndexCache()
	info, err := cache.LatestImage(context.Background(), host+"/homepage", "latest", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	if info.Resolved != "v2.6.0" {
		t.Errorf("Resolved = %q, want v2.6.0 from the OCI version label", info.Resolved)
	}
	if info.Latest != "v2.7.1" {
		t.Errorf("Latest = %q, want v2.7.1", info.Latest)
	}
	if !strings.HasPrefix(info.Digest, "sha256:") {
		t.Errorf("Digest = %q, want a digest for drift tracking", info.Digest)
	}
	assertNoLayerFetched(t, reg, layer)
}

// The version label is inherited from the base image unless the packager overrides it, so a self-built image can claim "24.04" (Ubuntu) as its own version.
func TestLatestImageIgnoresVersionLabelThatIsNotATag(t *testing.T) {
	host, _ := newTestRegistry(t)
	writeImage(t, host, "homelab", "477ac13-gfx1201-rocm7.2.4", "24.04", time.Unix(1760000000, 0))
	writeImage(t, host, "homelab", "latest", "24.04", time.Unix(1760000000, 0))

	cache := NewIndexCache()
	info, err := cache.LatestImage(context.Background(), host+"/homelab", "latest", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	if info.Resolved != "" {
		t.Errorf("Resolved = %q, want the phantom 24.04 version to be ignored", info.Resolved)
	}
	if info.Latest != "" {
		t.Errorf("Latest = %q, want no candidate driven by a phantom version", info.Latest)
	}
	if !strings.Contains(info.Note, "24.04") {
		t.Errorf("Note = %q, want it to explain the ignored version label", info.Note)
	}
}

func TestLatestImageOpaqueTagsUseBuildDates(t *testing.T) {
	host, _ := newTestRegistry(t)
	base := time.Unix(1750000000, 0)
	writeImage(t, host, "strata", "a1b2c3d-gfx1201", "", base)
	writeImage(t, host, "strata", "d4e5f67-gfx1201", "", base.Add(48*time.Hour))
	writeImage(t, host, "strata", "0a9b8c7-gfx1201", "", base.Add(24*time.Hour))

	cache := NewIndexCache()
	info, err := cache.LatestImage(context.Background(), host+"/strata", "a1b2c3d-gfx1201", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	if info.Latest != "d4e5f67-gfx1201" {
		t.Errorf("Latest = %q, want d4e5f67-gfx1201 (newest build date)", info.Latest)
	}
	if info.Kind != TagOpaque {
		t.Errorf("Kind = %q, want opaque for a source-hash tag", info.Kind)
	}
	if !strings.Contains(info.Note, "build date") {
		t.Errorf("Note = %q, want an explanation of the date-based pick", info.Note)
	}
}

func TestLatestImageCursorWindow(t *testing.T) {
	host, _ := newTestRegistry(t)
	// 12 tags, page size 3: the cursor anchored at the current tag must still find a newer candidate without paging the whole listing.
	tags := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0", "1.6.0", "1.7.0", "1.8.0", "1.9.0", "1.10.0", "2.0.0"}
	for i, tag := range tags {
		writeImage(t, host, "windowed", tag, tag, time.Unix(1700000000+int64(i)*86400, 0))
	}

	cache := NewIndexCache(withLimits(func(l *limits) { l.tagPageSize = 3 }))
	repo := host + "/windowed"
	got, exhaustive, err := cache.imageTags(context.Background(), repo, "1.9.0")
	if err != nil {
		t.Fatalf("imageTags: %v", err)
	}
	if exhaustive {
		t.Errorf("exhaustive = true, want a bounded window for a 12 tag repo with page size 3")
	}
	if len(got) == 0 || len(got) > cache.limits.tagPageSize*cache.limits.maxPages {
		t.Errorf("got %d tags, want a bounded window", len(got))
	}
	// The anchored window supplies "newer than the anchor" tags and the rollover probe supplies the v1.10.0 that sorts lexicographically before it.
	if !contains(got, "2.0.0") || !contains(got, "1.10.0") {
		t.Errorf("windows = %v, want both 2.0.0 and the rolled-over 1.10.0", got)
	}
	if contains(got, "1.5.0") {
		t.Errorf("windows = %v, want the middle of the listing untouched", got)
	}

	info, err := cache.LatestImage(context.Background(), repo, "1.9.0", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	_ = info
	if info.Latest != "1.10.0" {
		t.Errorf("Latest = %q, want 1.10.0", info.Latest)
	}
}

func TestLatestImageCachesOutcome(t *testing.T) {
	host, reg := newTestRegistry(t)
	writeImage(t, host, "cached", "1.0.0", "1.0.0", time.Unix(1750000000, 0))
	writeImage(t, host, "cached", "1.1.0", "1.1.0", time.Unix(1760000000, 0))

	cache := NewIndexCache()
	ctx := context.Background()
	first, err := cache.LatestImage(ctx, host+"/cached", "1.0.0", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	before := len(reg.requestPaths())
	second, err := cache.LatestImage(ctx, host+"/cached", "1.0.0", "", ScopeMinor)
	if err != nil {
		t.Fatalf("LatestImage (cached): %v", err)
	}
	if after := len(reg.requestPaths()); after != before {
		t.Errorf("cached lookup issued %d extra requests", after-before)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("cached result differs: %+v vs %+v", first, second)
	}
}

func TestVendorTarget(t *testing.T) {
	cases := []struct {
		in, path string
		kind     apiKind
	}{
		{"index.docker.io/library/alpine", "library/alpine", apiDocker},
		{"alpine", "library/alpine", apiDocker},
		{"ghcr.io/foo/bar", "foo/bar", apiNone},
		{"harbor.example.com/homelab/strata", "homelab/strata", apiHarbor},
		{"localhost:5000/app", "app", apiHarbor},
	}
	for _, c := range cases {
		api, path, err := NewIndexCache().vendorTarget(c.in)
		if err != nil || path != c.path || api.kind != c.kind {
			t.Errorf("vendorTarget(%q) = %v, %q, %v; want %q, %q", c.in, api.kind, path, err, c.kind, c.path)
		}
	}
}

// Registries with no vendor tag API at all must not be probed for one.
func TestBulkTagDatesSkipsHostsWithoutAPI(t *testing.T) {
	cache := NewIndexCache()
	ctx := context.Background()
	for _, repository := range []string{"ghcr.io/foo/bar", "gcr.io/foo/bar", "public.ecr.aws/foo/bar"} {
		if _, ok := cache.bulkTagDates(ctx, repository); ok {
			t.Errorf("%s should have no vendor tag API", repository)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestLatestImagePinnedDigestReportsWhatTheTagServes: a reference pinned to a digest runs that image, but the drift check needs what the tag serves today.
func TestLatestImagePinnedDigestReportsWhatTheTagServes(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	const repo = "pinned/app"
	platforms := []string{"linux/amd64", "linux/arm64"}

	old := p.index(t, host, repo, "1.0.0", "1.0.0", time.Unix(1760000000, 0), platforms...)
	cur := p.index(t, host, repo, "1.0.0", "1.0.0", time.Unix(1765000000, 0), platforms...) // the tag moved
	oldDigest, _ := old.Digest()
	curDigest, _ := cur.Digest()
	curManifest, err := cur.IndexManifest()
	if err != nil {
		t.Fatalf("index manifest: %v", err)
	}
	child := curManifest.Manifests[0].Digest

	cases := []struct {
		name, pinned, want string
	}{
		{"tag moved away from the pin", oldDigest.String(), curDigest.String()},
		{"pin is a platform manifest the tag still serves", child.String(), child.String()},
		{"pin is the tag's current index", curDigest.String(), curDigest.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := NewIndexCache().LatestImage(context.Background(), host+"/"+repo, "1.0.0", tc.pinned, ScopeAll)
			if err != nil {
				t.Fatalf("LatestImage: %v", err)
			}
			if info.Digest != tc.want {
				t.Errorf("Digest = %s, want %s", info.Digest, tc.want)
			}
		})
	}
}

// TestLatestImageReachesNewestInBusyRepository: llama.cpp on GHCR pushes ~20 tags per build, so hundreds of tags sit between the current build and the newest.
func TestLatestImageReachesNewestInBusyRepository(t *testing.T) {
	host, _ := newTestRegistry(t)
	const repo = "busy/server"
	for b := 1000; b <= 1350; b++ {
		tag := fmt.Sprintf("server-b%d", b)
		writeImage(t, host, repo, tag, tag, time.Unix(1700000000+int64(b), 0))
	}

	info, err := NewIndexCache(WithVerifyTags(false)).LatestImage(context.Background(), host+"/"+repo, "server-b1000", "", ScopeAll)
	if err != nil {
		t.Fatalf("LatestImage: %v", err)
	}
	if info.Latest != "server-b1350" {
		t.Errorf("Latest = %q, want server-b1350 (350 tags after the current one)", info.Latest)
	}
}

// TestJSONBodySizeLimit covers S3: every getJSON response body is capped.
func TestJSONBodySizeLimit(t *testing.T) {
	cache := NewIndexCache(withLimits(func(l *limits) { l.jsonBytes = 128 }))

	cases := []struct {
		name     string
		body     string
		wantErr  bool
		wantTags int
	}{
		{name: "under the cap", body: `{"tags":["1.0.0","1.1.0"]}`, wantTags: 2},
		{name: "over the cap", body: `{"tags":[` + strings.Repeat(`"1.0.0",`, 40) + `"9.9.9"]}`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()

			var out struct {
				Tags []string `json:"tags"`
			}
			_, err := cache.getJSON(context.Background(), ts.Client(), ts.URL+"/tags", "", &out)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "size limit") {
					t.Errorf("err = %v, want size limit error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("getJSON: %v", err)
			}
			if len(out.Tags) != c.wantTags {
				t.Errorf("tags = %v, want %d entries", out.Tags, c.wantTags)
			}
		})
	}
}
