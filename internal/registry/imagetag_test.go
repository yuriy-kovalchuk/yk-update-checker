package registry

import (
	"reflect"
	"testing"
)

func TestClassifyTag(t *testing.T) {
	cases := []struct {
		tag  string
		kind TagKind
		pre  string
		core []int
		post string
	}{
		{"3.21", TagSemver, "", []int{3, 21}, ""},
		{"v1.12.1", TagSemver, "v", []int{1, 12, 1}, ""},
		{"2026.9.3", TagCalver, "", []int{2026, 9, 3}, ""},
		{"20260927", TagDate, "", []int{20260927}, ""},
		{"29.8.0-dind", TagSemver, "", []int{29, 8, 0}, "-dind"},
		{"0.49.0-kafka-4.2.0", TagSemver, "", []int{0, 49, 0}, "-kafka-4.2.0"},
		{"server-cuda12-b10689", TagBuild, "server-cuda12-b", []int{10689}, ""},
		{"477ac13-gfx1201-rocm7.2.4-c40a550", TagOpaque, "", nil, ""},
		{"9", TagSemver, "", []int{9}, ""},
		{"latest", TagFloating, "", nil, ""},
		{"release", TagFloating, "", nil, ""},
		{"main", TagFloating, "", nil, ""},
		{"master-vulkan", TagOpaque, "", nil, ""},
		{"", TagUnknown, "", nil, ""},
	}
	for _, c := range cases {
		got := classifyTag(c.tag)
		if got.Kind != c.kind || got.Pre != c.pre || !reflect.DeepEqual(got.Core, c.core) || got.Post != c.post {
			t.Errorf("classifyTag(%q) = kind=%s pre=%q core=%v post=%q, want kind=%s pre=%q core=%v post=%q",
				c.tag, got.Kind, got.Pre, got.Core, got.Post, c.kind, c.pre, c.core, c.post)
		}
	}
}

// Corpora below are trimmed from real registry listings (Docker Hub, GHCR, Quay, registry.k8s.io) for the images in the homelab this feature was developed against.

var alpineTags = []string{
	"2.20.6", "3.9.6", "3.21", "3.21.0", "3.21.1", "3.21.2", "3.21.3", "3.21.4", "3.21.5", "3.21.6", "3.21.7", "3.21.8",
	"3.22", "3.22.0", "3.22.1", "3.22.2", "3.23.3", "3.23.4", "3.24", "3.24.1", "3.24.2",
	"20190228", "20240713", "20260805", "edge", "latest", "3.21.0-patched",
}

var dockerTags = []string{
	"20.10.24-dind", "28.5.1-dind", "28.5.2-dind", "29.8.0", "29.8.0-alpine3.24", "29.8.0-dind", "29.8.0-dind-alpine3.24",
	"29.8.1-dind", "29.9.0-dind", "29.9.1-dind", "29.9.2", "29.9.2-alpine3.24", "29.9.2-dind", "29.9.2-dind-rootless",
	"29.9.2-rc.1", "29.9.2-windowsservercore-ltsc2025", "cli", "dind", "edge", "20260817",
}

var llamaTags = []string{
	"server-cuda12-b10689", "server-cuda12-b10711", "server-cuda12-b10775", "server-cuda12-b11506", "server-cuda12-b11783",
	"server-cuda13-b11783", "server-b11783", "full-cuda12-b11783", "light-cuda12-b11783", "server-vulkan-b11783",
	"server-cuda12-latest",
}

var homepageTags = []string{"latest", "v1.0.0", "v2.3.0", "v2.4.0", "v2.5.0-rc.1", "v2.5.0", "v2.5.1", "v2.6.0"}

var virtOperatorTags = []string{
	"v1.9.0", "v1.9.1", "v1.9.1-amd64", "v1.9.1-arm64", "v1.9.2", "v1.9.3", "v1.9.4", "v1.9.5",
	"v1.10.0", "v1.10.1", "v1.11.0", "v1.12.0", "v1.13.2", "v17.42.37", "v0.0.0-20250207", "amd64-stamp",
}

var ingressNginxTags = []string{
	"v1.12.0", "v1.12.1", "v1.12.1-beta.0", "v1.13.0", "v1.13.1", "v1.13.2", "v1.13.3", "v1.13.4",
	"v1.13.5", "v1.13.6", "v1.14.0", "v1.14.1", "v1.14.2", "v1.14.3", "v1.14.4", "v1.15.0", "v1.15.1",
	"controller", "defaultbackend-amd64", "sha256-016a25cf89bf7f930869ccd7cb3dd4acbe106cd4da1419804951ef9c8636f053.sig",
}

var cloudflaredTags = []string{"2023.10.0", "2025.10.4", "2026.7.3", "2026.8.2", "2026.9.2", "2026.9.3", "2026.10.1", "2026.10.3", "edge", "latest"}

var sdCppTags = []string{"master-cuda-b1928", "master-vulkan-b2033", "master-sycl-fp16-b2033", "master-sycl-b2033", "latest"}

var paperlessTags = []string{"1.17.7", "2.14.1", "3.3.0", "3.3.1", "3.3.2", "3.3.3", "43.0", "43.0.0", "latest", "edge", "3.3.4-rc1"}

var portfolioTags = []string{"20250207", "20250211", "20260927", "20261003", "latest"}

var strataTags = []string{
	"477ac13-gfx1201-rocm7.2.4-c40a550", "9a51837-gfx1102-rocm7.2.4-c40a550",
	"a6b4e89-gfx1151-rocm7.2.4-c40a550", "165c434-gfx1010-rocm7.2.4-c40a550", "latest",
}

var valkeyTags = []string{"5.0.2", "6.2.15", "7.2.11", "8.1", "8.1.3", "9", "9.0.0", "9.0.1", "9.1", "9.1.0-rc1", "9.2", "latest", "bookworm"}

func TestBestTagOf(t *testing.T) {
	cases := []struct {
		name  string
		cur   string
		tags  []string
		scope Scope
		want  string
	}{
		{"alpine minor skips date tags", "3.21", alpineTags, ScopeMinor, "3.24.2"},
		{"alpine patch stays in branch", "3.21", alpineTags, ScopePatch, "3.21.8"},
		{"docker keeps -dind variant", "29.8.0-dind", dockerTags, ScopeMinor, "29.9.2-dind"},
		{"docker patch", "29.8.0-dind", dockerTags, ScopePatch, "29.8.1-dind"},
		{"llama keeps cuda12 variant", "server-cuda12-b10689", llamaTags, ScopeMinor, "server-cuda12-b11783"},
		{"homepage semver", "v2.4.0", homepageTags, ScopeMinor, "v2.6.0"},
		{"kubevirt minor allows v1.10", "v1.9.1", virtOperatorTags, ScopeMinor, "v1.13.2"},
		{"kubevirt patch stays v1.9.x", "v1.9.1", virtOperatorTags, ScopePatch, "v1.9.5"},
		{"ingress-nginx minor", "v1.12.1", ingressNginxTags, ScopeMinor, "v1.15.1"},
		{"ingress-nginx patch has no newer v1.12.x", "v1.12.1", ingressNginxTags, ScopePatch, ""},
		{"cloudflared calver", "2026.9.3", cloudflaredTags, ScopeMinor, "2026.10.3"},
		{"valkey single digit", "9", valkeyTags, ScopeMinor, "9.2"},
		{"portfolio date tags", "20260927", portfolioTags, ScopeMinor, "20261003"},
		{"composite kafka tag", "0.49.0-kafka-4.2.0", []string{"0.48.0-kafka-4.2.0", "0.50.0-kafka-4.2.0", "0.50.0-kafka-4.1.0"}, ScopeMinor, "0.50.0-kafka-4.2.0"},
		{"no cross-variant sd-cpp", "master-cuda-b1928", sdCppTags, ScopeMinor, ""},
		{"paperless already latest stable", "3.3.3", paperlessTags, ScopeMinor, ""},
		{"strata git shas not comparable", "477ac13-gfx1201-rocm7.2.4-c40a550", strataTags, ScopeMinor, ""},
		{"floating tag has no version", "latest", homepageTags, ScopeMinor, ""},
		{"untagged has no version", "", alpineTags, ScopeMinor, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cur := classifyTag(c.cur)
			best, ok := topCandidate(cur, c.tags, c.scope)
			if !ok {
				if c.want != "" {
					t.Fatalf("no candidate, want %q", c.want)
				}
				return
			}
			if best.Raw != c.want {
				t.Fatalf("got %q, want %q", best.Raw, c.want)
			}
		})
	}
}

func TestKubevirtJunkTagOnlyReachesAllScope(t *testing.T) {
	cur := classifyTag("v1.9.1")
	best, ok := topCandidate(cur, virtOperatorTags, ScopeMajor)
	if !ok || best.Raw != "v17.42.37" {
		t.Fatalf("major scope: got %+v ok=%v, want v17.42.37 (documented false positive, same as chart checks)", best, ok)
	}
}

func TestIsPrerelease(t *testing.T) {
	prerelease := []string{"v1.12.1-beta.0", "3.3.4-rc1", "v1.14.0-rc.0", "9.1.0-rc1", "sha256-abc.sig", "v0.0.0-20250207"}
	for _, tag := range prerelease {
		if !isPrerelease(classifyTag(tag)) {
			t.Errorf("%q should be treated as pre-release", tag)
		}
	}
	release := []string{"3.21.0-patched", "29.9.2-dind", "29.9.2-windowsservercore-ltsc2025", "v1.9.1-amd64", "1.27.0-r0", "0.49.0-kafka-4.2.0", "stable-lts"}
	for _, tag := range release {
		if isPrerelease(classifyTag(tag)) {
			t.Errorf("%q should not be treated as pre-release", tag)
		}
	}
}

func TestSameVariantKeepsDatePicksInsideOneBuildVariant(t *testing.T) {
	cases := []struct {
		cur, cand string
		want      bool
	}{
		{"master-vulkan", "master-cuda", false},
		{"master-vulkan", "master-vulkan-b1234", true},
		{"477ac13-gfx1201-rocm7.2.4-c40a550", "9a51837-gfx1201-rocm7.2.4-bb0a11b", true},
		{"477ac13-gfx1201-rocm7.2.4-c40a550", "9a51837-gfx1102-rocm7.2.4-bb0a11b", false},
		{"a1b2c3d", "d4e5f67", true}, // pure commit hashes carry no variant
	}
	for _, tc := range cases {
		if got := sameVariant(tc.cur, tc.cand); got != tc.want {
			t.Errorf("sameVariant(%q, %q) = %v, want %v", tc.cur, tc.cand, got, tc.want)
		}
	}
}

// topCandidate is the head of rankedCandidates: the update a lookup would offer.
func topCandidate(cur tagShape, tags []string, scope Scope) (tagShape, bool) {
	ranked := rankedCandidates(cur, tags, scope)
	if len(ranked) == 0 {
		return tagShape{}, false
	}
	return ranked[0], true
}
