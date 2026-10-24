package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ociregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// startVendorRegistry mounts vendor tag APIs on the in-memory OCI registry's host, as Harbor does.
func startVendorRegistry(t *testing.T, mounts map[string]http.Handler) (host string, reg *recordingRegistry) {
	t.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard) // the in-memory registry logs every request
	t.Cleanup(func() { log.SetOutput(prev) })

	mux := http.NewServeMux()
	for prefix, h := range mounts {
		mux.Handle(prefix, h)
	}
	mux.Handle("/", ociregistry.New(ociregistry.Logger(log.New(io.Discard, "", 0))))
	rec := &recordingRegistry{handler: mux}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return u.Host, rec
}

// tagReport is what a vendor tag API claims about a single tag.
type tagReport struct {
	Pushed  time.Time
	Digest  string
	Size    int64
	Missing bool // answer as if the tag did not exist
}

// quayTagAPI answers both halves of Quay's tag API from one report map: the repository listing (no specificTag) and the single-tag query.
func quayTagAPI(reports map[string]tagReport, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api v1 repository <ns> <name> tag
		if len(parts) < 6 || parts[2] != "repository" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		list := func(names ...string) []any {
			out := make([]any, 0, len(names))
			for _, name := range names {
				rep, ok := reports[name]
				if !ok || rep.Missing {
					continue
				}
				out = append(out, map[string]any{
					"name":            name,
					"manifest_digest": rep.Digest,
					"size":            rep.Size,
					"start_ts":        rep.Pushed.Unix(),
				})
			}
			return out
		}
		body := map[string]any{
			"namespace":      parts[3],
			"repository":     parts[4],
			"page":           1,
			"has_additional": false,
		}
		if tag := r.URL.Query().Get("specificTag"); tag != "" {
			body["tags"] = list(tag)
		} else {
			names := make([]string, 0, len(reports))
			for name := range reports {
				names = append(names, name)
			}
			sort.Strings(names)
			body["tags"] = list(names...)
		}
		_ = json.NewEncoder(w).Encode(body)
	})
}

// harborArtifactAPI answers Harbor's single-artifact endpoint the way current Harbor does: the digest at the top level, extra_info without one.
func harborArtifactAPI(reports map[string]tagReport) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		rep, ok := reports[parts[len(parts)-1]]
		if !ok || rep.Missing {
			http.Error(w, `{"errors":[{"code":"NOT_FOUND"}]}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"digest":     rep.Digest,
			"push_time":  rep.Pushed.Format(time.RFC3339),
			"extra_info": map[string]any{},
		})
	})
}

// newTestImage builds an image carrying an OCI version label and, when platform ("linux/amd64") is set, a platform in its config.
func newTestImage(t *testing.T, version string, created time.Time, platform string) v1.Image {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	cf.Created = v1.Time{Time: created}
	if platform != "" {
		cf.OS, cf.Architecture, _ = strings.Cut(platform, "/")
	}
	if version != "" {
		cf.Config.Labels = map[string]string{labelVersion: version}
	}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatalf("mutate config: %v", err)
	}
	return img
}

// pushed remembers what went into the registry so tests can tell config blobs (allowed) apart from layer blobs (never allowed).
type pushed struct {
	layers map[string]bool
}

func newPushed() *pushed { return &pushed{layers: map[string]bool{}} }

func (p *pushed) tag(t *testing.T, host, repository, tag string, img v1.Image) {
	t.Helper()
	p.note(t, img)
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repository, tag), name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag ref: %v", err)
	}
	if err := remote.Write(ref, img, remote.WithContext(context.Background())); err != nil {
		t.Fatalf("push %s:%s: %v", repository, tag, err)
	}
}

// index writes a multi-platform OCI index and returns it, so a test can name the cosign tag that belongs to its digest.
func (p *pushed) index(t *testing.T, host, repository, tag, version string, created time.Time, platforms ...string) v1.ImageIndex {
	t.Helper()
	adds := make([]mutate.IndexAddendum, 0, len(platforms))
	for _, pl := range platforms {
		osName, arch, _ := strings.Cut(pl, "/")
		img := newTestImage(t, version, created, pl)
		p.note(t, img)
		adds = append(adds, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				MediaType: types.OCIManifestSchema1,
				Platform:  &v1.Platform{OS: osName, Architecture: arch},
			},
		})
	}
	idx := mutate.AppendManifests(empty.Index, adds...)
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", host, repository, tag), name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag ref: %v", err)
	}
	if err := remote.WriteIndex(ref, idx, remote.WithContext(context.Background())); err != nil {
		t.Fatalf("push index %s:%s: %v", repository, tag, err)
	}
	return idx
}

// note records an image's layer digests without pushing it, so a fixture can write the same image under two tags (a digest alias).
func (p *pushed) note(t *testing.T, img v1.Image) {
	t.Helper()
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	for _, l := range layers {
		h, err := l.Digest()
		if err != nil {
			t.Fatalf("layer digest: %v", err)
		}
		p.layers[h.String()] = true
	}
}

func mustLookup(t *testing.T, cache *IndexCache, repository, curTag string, scope Scope) ImageInfo {
	t.Helper()
	info, err := cache.LatestImage(context.Background(), repository, curTag, "", scope)
	if err != nil {
		t.Fatalf("LatestImage(%s:%s): %v", repository, curTag, err)
	}
	return info
}

func joined(s SkippedTag) string { return strings.Join(s.Reasons, "; ") }

func skippedFor(info ImageInfo, tag string) (SkippedTag, bool) {
	for _, s := range info.Skipped {
		if s.Tag == tag {
			return s, true
		}
	}
	return SkippedTag{}, false
}

// assertFetchShape checks the request budget: bounded tag pages, bounded blob requests, and never a layer.
func assertFetchShape(t *testing.T, reg *recordingRegistry, p *pushed) {
	t.Helper()
	var tagsListed, manifests, blobs, layers int
	for _, path := range reg.requestPaths() {
		switch {
		case strings.HasSuffix(path, "/tags/list"):
			tagsListed++
		case strings.Contains(path, "/blobs/"):
			blobs++
			for layer := range p.layers {
				if strings.Contains(path, layer) {
					layers++
				}
			}
		default:
			manifests++
		}
	}
	if layers != 0 {
		t.Errorf("%d layer blobs downloaded, verification must read manifests and configs only", layers)
	}
	// search windows (limits.pageBudget pages) + one extra sibling page per verified candidate.
	lim := defaultLimits()
	if limit := lim.pageBudget + lim.verifyCandidates; tagsListed > limit {
		t.Errorf("%d tag pages fetched, want at most %d", tagsListed, limit)
	}
	// one manifest + one config per image inspected (current + candidates).
	if limit := 2 * (1 + lim.verifyCandidates) * 4; blobs > limit {
		t.Errorf("%d blob requests, want well under %d", blobs, limit)
	}
	if manifests == 0 {
		t.Error("no manifests fetched, the fixture is not reachable")
	}
}

// The reported bug.
const virtRepo = "kubevirt/virt-operator"

var virtPlatforms = []string{"linux/amd64", "linux/arm64", "linux/s390x"}

func kubevirtFixture(t *testing.T, host string, p *pushed) (curDate, strayDate time.Time) {
	t.Helper()
	curDate = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	strayDate = time.Date(2021, 2, 18, 4, 0, 0, 0, time.UTC)

	p.index(t, host, virtRepo, "v1.9.1", "v1.9.1", curDate, virtPlatforms...)
	p.tag(t, host, virtRepo, "v1.9.0", newTestImage(t, "v1.9.0", curDate.AddDate(0, -1, 0), "linux/amd64"))
	for _, rc := range []string{"v1.9.0-rc.0", "v1.9.0-rc.1", "v1.9.0-rc.2"} {
		for _, pl := range virtPlatforms {
			arch := strings.Split(pl, "/")[1]
			p.tag(t, host, virtRepo, rc+"-"+arch, newTestImage(t, "", curDate.AddDate(0, -2, 0), pl))
		}
	}
	// The stray, byte-identical to the nightly pushed the same day.
	stray := newTestImage(t, "v17.42.37", strayDate, "linux/amd64")
	p.tag(t, host, virtRepo, "v17.42.37", stray)
	p.tag(t, host, virtRepo, "nightly", stray)
	return curDate, strayDate
}

// TestVerifyStrayKubeVirtTag covers the reported bug twice: with Quay push dates available, and on a registry that offers no vendor date API at all.
func TestVerifyStrayKubeVirtTag(t *testing.T) {
	cases := []struct {
		name        string
		quay        map[string]tagReport
		wantReasons []string
		denyReasons []string
	}{
		{
			name: "quay push dates add to the soft signals",
			quay: map[string]tagReport{
				"v1.9.1":    {Pushed: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
				"v17.42.37": {Pushed: time.Date(2021, 2, 18, 4, 0, 0, 0, time.UTC)},
				"nightly":   {Pushed: time.Date(2021, 2, 18, 4, 5, 0, 0, time.UTC)},
			},
			wantReasons: []string{
				"pushed 2021-02-18, before v1.9.1 pushed 2026-10-12",
				"no sibling tags",
				"skips v2..v16, none of them listed",
				"single-arch, v1.9.1 ships linux/amd64,linux/arm64,linux/s390x",
			},
		},
		{
			name:        "a registry with no date API rejects it on soft signals",
			denyReasons: []string{"pushed"},
			wantReasons: []string{"no sibling tags", "skips v2..v16", "single-arch"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPushed()
			var mounts map[string]http.Handler
			if tc.quay != nil {
				mounts = map[string]http.Handler{"/api/v1/": quayTagAPI(tc.quay, 0)}
			}
			host, reg := startVendorRegistry(t, mounts)
			var opts []CacheOption
			if tc.quay != nil {
				opts = append(opts, withVendorAPI(host, vendorAPI{kind: apiQuay, base: "http://" + host}))
			}
			curDate, strayDate := kubevirtFixture(t, host, p)
			if tc.quay != nil && (!tc.quay["v1.9.1"].Pushed.Equal(curDate) || !tc.quay["v17.42.37"].Pushed.Equal(strayDate)) {
				t.Error("fixture dates and vendor report disagree")
			}
			reg.reset()

			info := mustLookup(t, NewIndexCache(opts...), host+"/"+virtRepo, "v1.9.1", ScopeAll)
			if info.Latest != "" {
				t.Errorf("Latest = %q, want empty: v1.9.1 is current once the stray is rejected", info.Latest)
			}
			s, ok := skippedFor(info, "v17.42.37")
			if !ok || len(info.Skipped) != 1 {
				t.Fatalf("Skipped = %+v, want exactly v17.42.37", info.Skipped)
			}
			reasons := joined(s)
			for _, want := range tc.wantReasons {
				if !strings.Contains(reasons, want) {
					t.Errorf("reasons %q missing %q", reasons, want)
				}
			}
			for _, deny := range tc.denyReasons {
				if strings.Contains(reasons, deny) {
					t.Errorf("reasons %q must not mention %q when no dates exist", reasons, deny)
				}
			}
			if !strings.Contains(info.Note, "skipped v17.42.37") {
				t.Errorf("Note = %q, want the skipped tag summarized", info.Note)
			}
			assertFetchShape(t, reg, p)
		})
	}
}

func TestVerifyAcceptsLegitMajorBump(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, map[string]http.Handler{
		"/api/v1/": quayTagAPI(map[string]tagReport{
			"v1.9.1": {Pushed: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
			"v2.0.0": {Pushed: time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)},
		}, 0),
	})
	vendor := withVendorAPI(host, vendorAPI{kind: apiQuay, base: "http://" + host})

	p.index(t, host, virtRepo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), virtPlatforms...)
	p.index(t, host, virtRepo, "v2.0.0", "v2.0.0", time.Unix(1765000000, 0), virtPlatforms...)
	p.tag(t, host, virtRepo, "v2.0.0-rc.1", newTestImage(t, "v2.0.0-rc.1", time.Unix(1764000000, 0), "linux/amd64"))

	info := mustLookup(t, NewIndexCache(vendor), host+"/"+virtRepo, "v1.9.1", ScopeMajor)
	if info.Latest != "v2.0.0" {
		t.Errorf("Latest = %q, want v2.0.0", info.Latest)
	}
	if len(info.Skipped) != 0 {
		t.Errorf("Skipped = %+v, want none", info.Skipped)
	}
	if info.Note != "" {
		t.Errorf("Note = %q, want no caveats for a clean major bump", info.Note)
	}
}

func TestVerifyAcceptsBuildCounterBump(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	const repo = "ggml-org/llama.cpp"

	p.tag(t, host, repo, "server-vulkan-b11347", newTestImage(t, "server-vulkan-b11347", time.Unix(1760000000, 0), "linux/amd64"))
	p.tag(t, host, repo, "server-vulkan-b11783", newTestImage(t, "server-vulkan-b11783", time.Unix(1765000000, 0), "linux/amd64"))

	info := mustLookup(t, NewIndexCache(), host+"/"+repo, "server-vulkan-b11347", ScopeMinor)
	if info.Latest != "server-vulkan-b11783" {
		t.Errorf("Latest = %q, want server-vulkan-b11783: build counters jump by design", info.Latest)
	}
	if len(info.Skipped) != 0 {
		t.Errorf("Skipped = %+v, want none", info.Skipped)
	}
	if info.Note != "" {
		t.Errorf("Note = %q, want none", info.Note)
	}
}

func TestVerifyFallsBackToNextCandidate(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	const repo = "stray/two"

	p.index(t, host, repo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), "linux/amd64", "linux/arm64")
	p.tag(t, host, repo, "v99.0.0", newTestImage(t, "v99.0.0", time.Unix(1700000000, 0), "linux/amd64"))
	p.tag(t, host, repo, "v50.1.2", newTestImage(t, "v50.1.2", time.Unix(1700000000, 0), "linux/amd64"))
	p.index(t, host, repo, "v2.0.0", "v2.0.0", time.Unix(1765000000, 0), "linux/amd64", "linux/arm64")
	p.tag(t, host, repo, "v2.0.0-rc.1", newTestImage(t, "v2.0.0-rc.1", time.Unix(1764000000, 0), "linux/amd64"))

	info := mustLookup(t, NewIndexCache(), host+"/"+repo, "v1.9.1", ScopeAll)
	if info.Latest != "v2.0.0" {
		t.Errorf("Latest = %q, want v2.0.0 as the only real release", info.Latest)
	}
	var got []string
	for _, s := range info.Skipped {
		got = append(got, s.Tag)
	}
	if strings.Join(got, ",") != "v99.0.0,v50.1.2" {
		t.Errorf("Skipped = %v, want [v99.0.0 v50.1.2] best-looking first", got)
	}
	for _, s := range info.Skipped {
		if len(s.Reasons) < 2 {
			t.Errorf("%s rejected on %v: want at least two soft signals", s.Tag, s.Reasons)
		}
		if !strings.Contains(info.Note, "skipped "+s.Tag) {
			t.Errorf("Note %q missing skipped %s", info.Note, s.Tag)
		}
	}
}

// imageVerifyCandidates bounds the work per look-up: only the best-looking candidates are inspected, the rest are left alone.
func TestVerifyStopsAfterTheCandidateBudget(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	const repo = "stray/cap"

	p.index(t, host, repo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), "linux/amd64", "linux/arm64")
	for _, tag := range []string{"v99.0.0", "v98.0.0", "v97.0.0", "v96.0.0"} {
		p.tag(t, host, repo, tag, newTestImage(t, tag, time.Unix(1700000000, 0), "linux/amd64"))
	}

	info := mustLookup(t, NewIndexCache(), host+"/"+repo, "v1.9.1", ScopeAll)
	if info.Latest != "" {
		t.Errorf("Latest = %q, want nothing offered", info.Latest)
	}
	if want := defaultLimits().verifyCandidates; len(info.Skipped) != want {
		t.Errorf("Skipped = %+v, want exactly %d rejections", info.Skipped, want)
	}
	// "current" here only means "no candidate survived the budget": say so.
	if want := "stopped after verifying 3 candidates; 1 more tags rank above v1.9.1"; !strings.Contains(info.Note, want) {
		t.Errorf("Note = %q, want it to admit the unchecked candidates (%q)", info.Note, want)
	}
}

// TestVerifyToleratesRepushedCurrentTag mirrors alpine: 3.21 re-pushed after 3.24.x.
func TestVerifyToleratesRepushedCurrentTag(t *testing.T) {
	p := newPushed()
	reports := map[string]tagReport{
		"3.21":   {Pushed: time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)},
		"3.24":   {Pushed: time.Date(2026, 9, 18, 17, 24, 0, 0, time.UTC)},
		"3.24.1": {Pushed: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)},
		"3.24.2": {Pushed: time.Date(2026, 9, 18, 17, 24, 0, 0, time.UTC)},
	}
	host, _ := startVendorRegistry(t, map[string]http.Handler{"/api/v1/": quayTagAPI(reports, 0)})
	vendor := withVendorAPI(host, vendorAPI{kind: apiQuay, base: "http://" + host})
	const repo = "library/alpine"
	platforms := []string{"linux/amd64", "linux/arm64"}
	for tag, rep := range reports {
		p.index(t, host, repo, tag, "", rep.Pushed, platforms...)
	}

	info := mustLookup(t, NewIndexCache(vendor), host+"/"+repo, "3.21", ScopeAll)
	if info.Latest != "3.24.2" {
		t.Errorf("Latest = %q, want 3.24.2: a re-pushed current tag must not hide its successors", info.Latest)
	}
	if len(info.Skipped) != 0 {
		t.Errorf("Skipped = %+v, want none", info.Skipped)
	}
	if strings.Contains(info.Note, "pushed") {
		t.Errorf("Note = %q, a re-push within weeks is not worth a caveat", info.Note)
	}
}

// A vendor API that fails - rate limited, or simply absent - must never change the verdict on its own, and must never fail the look-up.
func TestVerifyVendorAPIFailureIsNoSignal(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, map[string]http.Handler{
		"/api/v1/": quayTagAPI(nil, http.StatusTooManyRequests),
	})
	vendor := withVendorAPI(host, vendorAPI{kind: apiQuay, base: "http://" + host})

	p.index(t, host, virtRepo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), virtPlatforms...)
	p.index(t, host, virtRepo, "v2.0.0", "v2.0.0", time.Unix(1765000000, 0), virtPlatforms...)
	p.tag(t, host, virtRepo, "v2.0.0-rc.1", newTestImage(t, "v2.0.0-rc.1", time.Unix(1764000000, 0), "linux/amd64"))

	info := mustLookup(t, NewIndexCache(vendor), host+"/"+virtRepo, "v1.9.1", ScopeMajor)
	if info.Latest != "v2.0.0" {
		t.Errorf("Latest = %q, want v2.0.0 despite the rate limit", info.Latest)
	}
	if info.Note != "" {
		t.Errorf("Note = %q, want no caveats from a failed date look-up", info.Note)
	}
}

func TestVerifyDisabledKeepsOldBehaviour(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	kubevirtFixture(t, host, p)

	info := mustLookup(t, NewIndexCache(WithVerifyTags(false)), host+"/"+virtRepo, "v1.9.1", ScopeAll)
	if info.Latest != "v17.42.37" {
		t.Errorf("Latest = %q, want the stray v17.42.37 with verification off", info.Latest)
	}
	if len(info.Skipped) != 0 {
		t.Errorf("Skipped = %+v, want none when verification is off", info.Skipped)
	}
}

// Signatures are a soft signal, and only when the current tag has them.
func TestVerifyUnsignedCandidateAgainstSignedCurrent(t *testing.T) {
	p := newPushed()
	host, _ := startVendorRegistry(t, nil)
	const repo = "signed/app"

	cur := p.index(t, host, repo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), "linux/amd64", "linux/arm64")
	dgst, err := cur.Digest()
	if err != nil {
		t.Fatalf("index digest: %v", err)
	}
	p.tag(t, host, repo, fmt.Sprintf("sha256-%s.sig", dgst.Hex), newTestImage(t, "", time.Unix(1760000001, 0), "linux/amd64"))
	p.index(t, host, repo, "v2.0.0", "v2.0.0", time.Unix(1765000000, 0), "linux/amd64", "linux/arm64")
	p.tag(t, host, repo, "v2.0.0-rc.1", newTestImage(t, "v2.0.0-rc.1", time.Unix(1764000000, 0), "linux/amd64"))

	info := mustLookup(t, NewIndexCache(), host+"/"+repo, "v1.9.1", ScopeMajor)
	if info.Latest != "v2.0.0" {
		t.Errorf("Latest = %q, want v2.0.0: one soft signal is not a rejection", info.Latest)
	}
	if !strings.Contains(info.Note, "unsigned, while v1.9.1 is signed") {
		t.Errorf("Note = %q, want the signature caveat", info.Note)
	}
	if len(info.Skipped) != 0 {
		t.Errorf("Skipped = %+v, want none", info.Skipped)
	}
}

// A candidate that is byte-identical to an opaque nightly is an alias.
func TestVerifyDigestAliasOfNightly(t *testing.T) {
	p := newPushed()
	nightly := newTestImage(t, "v2.0.0", time.Unix(1765000000, 0), "linux/amd64")
	dgst, err := nightly.Digest()
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	host, _ := startVendorRegistry(t, map[string]http.Handler{
		"/api/v1/": quayTagAPI(map[string]tagReport{
			"v1.9.1":             {Pushed: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
			"v2.0.0":             {Pushed: time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)},
			"20210218_4b576cb71": {Pushed: time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC), Digest: dgst.String()},
		}, 0),
	})
	vendor := withVendorAPI(host, vendorAPI{kind: apiQuay, base: "http://" + host})
	const repo = "alias/app"

	p.tag(t, host, repo, "v1.9.1", newTestImage(t, "v1.9.1", time.Unix(1760000000, 0), "linux/amd64"))
	p.tag(t, host, repo, "v2.0.0", nightly)
	p.tag(t, host, repo, "20210218_4b576cb71", nightly)

	info := mustLookup(t, NewIndexCache(vendor), host+"/"+repo, "v1.9.1", ScopeMajor)
	if info.Latest != "" {
		t.Errorf("Latest = %q, want v2.0.0 rejected as an alias of a nightly", info.Latest)
	}
	s, ok := skippedFor(info, "v2.0.0")
	if !ok {
		t.Fatalf("Skipped = %+v, want v2.0.0", info.Skipped)
	}
	if !strings.Contains(joined(s), "same digest as 20210218_4b576cb71") {
		t.Errorf("reasons %q want the digest alias", joined(s))
	}
	if !strings.Contains(info.Note, "skipped v2.0.0") {
		t.Errorf("Note = %q, want the skipped tag summarized", info.Note)
	}
}

// The version label is decisive only when it names another version this image really has; a base-image label (Ubuntu "24.04") is no signal at all.
func TestVerifyVersionLabelSignals(t *testing.T) {
	cases := []struct {
		name       string
		label      string
		wantLatest string
	}{
		{name: "label matches the tag", label: "v2.0.0", wantLatest: "v2.0.0"},
		{name: "label is a base image version", label: "24.04", wantLatest: "v2.0.0"},
		{name: "label names the current tag", label: "v1.9.1", wantLatest: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPushed()
			host, _ := startVendorRegistry(t, nil)
			const repo = "labels/app"

			p.index(t, host, repo, "v1.9.1", "v1.9.1", time.Unix(1760000000, 0), "linux/amd64", "linux/arm64")
			p.index(t, host, repo, "v2.0.0", tc.label, time.Unix(1765000000, 0), "linux/amd64", "linux/arm64")
			p.tag(t, host, repo, "v2.0.0-rc.1", newTestImage(t, "v2.0.0-rc.1", time.Unix(1764000000, 0), "linux/amd64"))

			info := mustLookup(t, NewIndexCache(), host+"/"+repo, "v1.9.1", ScopeMajor)
			if info.Latest != tc.wantLatest {
				t.Errorf("Latest = %q, want %q", info.Latest, tc.wantLatest)
			}
			if tc.wantLatest == "" {
				s, ok := skippedFor(info, "v2.0.0")
				if !ok || !strings.Contains(joined(s), "metadata claims version v1.9.1") {
					t.Errorf("Skipped = %+v, want the version-label signal", info.Skipped)
				}
			}
		})
	}
}

func TestVendorAPIFor(t *testing.T) {
	cases := []struct {
		scheme, host string
		want         apiKind
	}{
		{"https", "quay.io", apiQuay},
		{"https", "quay.redhat.com", apiQuay},
		{"https", "index.docker.io", apiDocker},
		{"https", "registry-1.docker.io", apiDocker},
		{"https", "ghcr.io", apiNone},
		{"https", "gcr.io", apiNone},
		{"https", "public.ecr.aws", apiNone},
		{"https", "harbor.example.com", apiHarbor},
		{"http", "127.0.0.1:5000", apiHarbor},
	}
	for _, tc := range cases {
		if got := vendorAPIFor(tc.scheme, tc.host); got.kind != tc.want {
			t.Errorf("vendorAPIFor(%q) = %q, want %q", tc.host, got.kind, tc.want)
		}
	}
}

// Vendor tag APIs report push dates, which the OCI protocol does not.
func TestFetchTagInfoPerVendor(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		repository string // without host
		tag        string
		handler    http.Handler
		wantDate   time.Time
		wantDigest string
		wantSize   int64
		wantErr    bool
	}{
		{
			name:       "quay",
			host:       "quay.io",
			repository: "kubevirt/virt-operator",
			tag:        "v1.9.1",
			handler: quayTagAPI(map[string]tagReport{
				"v1.9.1": {Pushed: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC), Digest: "sha256:aa", Size: 51365435},
			}, 0),
			wantDate:   time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
			wantDigest: "sha256:aa",
			wantSize:   51365435,
		},
		{
			name:       "quay does not know the tag",
			host:       "quay.io",
			repository: "kubevirt/virt-operator",
			tag:        "v9.9.9",
			handler: quayTagAPI(map[string]tagReport{
				"v1.9.1": {Pushed: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)},
			}, 0),
			wantErr: true,
		},
		{
			name:       "harbor",
			host:       "harbor.example.com",
			repository: "homelab/app",
			tag:        "1.0.0",
			handler: harborArtifactAPI(map[string]tagReport{
				"1.0.0": {Pushed: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), Digest: "sha256:bb"},
			}),
			wantDate:   time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			wantDigest: "sha256:bb",
		},
		{
			name:       "harbor release with the digest only in extra_info",
			host:       "harbor.example.com",
			repository: "homelab/app",
			tag:        "1.0.0",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"push_time":  time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339),
					"extra_info": map[string]any{"digest": "sha256:old"},
				})
			}),
			wantDate:   time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			wantDigest: "sha256:old",
		},
		{
			name:       "harbor nested repository is encoded twice",
			host:       "harbor.example.com",
			repository: "homelab/team/app",
			tag:        "1.0.0",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if want := "/api/v2.0/projects/homelab/repositories/team%252Fapp/artifacts/1.0.0"; r.URL.EscapedPath() != want {
					http.Error(w, "unexpected path "+r.URL.EscapedPath(), http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"digest":    "sha256:dd",
					"push_time": time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339),
				})
			}),
			wantDate:   time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			wantDigest: "sha256:dd",
		},
		{
			name:       "docker hub library image",
			host:       "index.docker.io",
			repository: "library/app",
			tag:        "1.0.0",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/repositories/library/app/tags/1.0.0" {
					http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"last_updated": time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339),
					"full_size":    1000,
					"images":       []any{map[string]any{"digest": "sha256:cc"}},
				})
			}),
			wantDate:   time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			wantDigest: "sha256:cc",
			wantSize:   1000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			cache := NewIndexCache(withVendorAPI(tc.host, vendorAPI{
				kind: vendorAPIFor("https", tc.host).kind,
				base: srv.URL,
			}))

			info, err := cache.fetchTagInfo(context.Background(), tc.host+"/"+tc.repository, tc.tag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error for an unknown tag, got %+v", info)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchTagInfo: %v", err)
			}
			if !info.HasDate || !info.Pushed.Equal(tc.wantDate) {
				t.Errorf("pushed = %v (hasDate %v), want %v", info.Pushed, info.HasDate, tc.wantDate)
			}
			if info.Digest != tc.wantDigest {
				t.Errorf("digest = %q, want %q", info.Digest, tc.wantDigest)
			}
			if info.Size != tc.wantSize {
				t.Errorf("size = %d, want %d", info.Size, tc.wantSize)
			}
		})
	}
}

// The pure signal helpers, no registry involved.
func TestMajorJumpAndSiblings(t *testing.T) {
	cases := []struct {
		name      string
		cur, cand string
		tags      []string
		wantJump  bool
		wantSibs  int
	}{
		{"v1 to v17 with nothing in between", "v1.9.1", "v17.42.37", []string{"v1.9.1", "v1.9.0"}, true, 0},
		{"v1 to v17 with v10 listed", "v1.9.1", "v17.42.37", []string{"v1.9.1", "v10.0.0"}, false, 0},
		{"adjacent majors are never a jump", "v1.9.1", "v2.0.0", []string{"v1.9.1", "v2.0.0-rc.1"}, false, 1},
		{"build counters are exempt", "server-vulkan-b11347", "server-vulkan-b11783", []string{"server-vulkan-b11347"}, false, 1},
		{"calver is exempt", "2025.10.4", "2026.10.3", []string{"2025.10.4", "2026.10.3"}, false, 0},
		{"arch siblings count", "v1.9.0", "v1.9.1", []string{"v1.9.1-amd64", "v1.9.1-arm64"}, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, cand := classifyTag(tc.cur), classifyTag(tc.cand)
			if _, _, jumped := majorJump(cur, cand, tc.tags); jumped != tc.wantJump {
				t.Errorf("majorJump(%s -> %s) = %v, want %v", tc.cur, tc.cand, jumped, tc.wantJump)
			}
			if sibs := len(siblingsOf(cand, tc.tags)); sibs != tc.wantSibs {
				t.Errorf("siblingsOf(%s) = %d, want %d", tc.cand, sibs, tc.wantSibs)
			}
		})
	}
}

func TestRankedCandidatesOrdersBestFirst(t *testing.T) {
	var got []string
	for _, c := range rankedCandidates(classifyTag("v1.9.1"), virtOperatorTags, ScopeAll) {
		got = append(got, c.Raw)
	}
	want := "v17.42.37,v1.13.2,v1.12.0,v1.11.0,v1.10.1,v1.10.0,v1.9.5,v1.9.4,v1.9.3,v1.9.2"
	if strings.Join(got, ",") != want {
		t.Errorf("ranked = %v, want %s", got, want)
	}
}
