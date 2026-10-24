package registry

import (
	"cmp"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TagKind classifies how an image tag can be compared.
type TagKind string

const (
	// TagSemver is a dotted numeric version, with or without a "v" prefix: 1.24.2, v1.12.1.
	TagSemver TagKind = "semver"
	// TagCalver is a date-prefixed version: 2026.10.3, 202506.
	TagCalver TagKind = "calver"
	// TagDate is a bare date with no separators: 20260927.
	TagDate TagKind = "date"
	// TagBuild is a variant tag whose version is its last numeric group: server-cuda12-b10689.
	TagBuild TagKind = "build"
	// TagFloating is a mutable name: latest, release, main.
	TagFloating TagKind = "floating"
	// TagOpaque carries no comparable version (git shas, plain variant names).
	TagOpaque TagKind = "opaque"
	// TagUnknown is an empty or unclassifiable tag.
	TagUnknown TagKind = ""
)

// TagKindOf classifies a tag without exposing its internal decomposition.
func TagKindOf(tag string) TagKind { return classifyTag(tag).Kind }

// tagShape is the comparable decomposition of a tag.
type tagShape struct {
	Raw  string
	Kind TagKind
	Pre  string // literal text before the version core, including any "v"
	Core []int  // numeric groups of the version core
	Post string // literal text after the version core, never contains digits
}

// comparable reports whether the shape carries a version to compare.
func (t tagShape) comparable() bool { return len(t.Core) > 0 }

var (
	// formMulti: non-digit prefix, a dotted number run of two or more groups, then anything.
	formMulti = regexp.MustCompile(`^([^\d]*)(\d+(?:\.\d+)+)(.*)$`)
	// formSingle: a single numeric group with a digit-free suffix.
	formSingle = regexp.MustCompile(`^([^\d]*)(\d+)([^\d]*)$`)
	// formLast: everything up to the last numeric group, that group, then a digit-free suffix.
	formLast = regexp.MustCompile(`^(.*\D)?(\d+)(\D*)$`)
	alphaRun = regexp.MustCompile(`^[a-zA-Z]+`)
)

// floatingTags are mutable names that always point at "whatever is newest".
var floatingTags = map[string]bool{
	"latest": true, "edge": true, "main": true, "master": true, "trunk": true, "head": true,
	"release": true, "stable": true, "stable-lts": true, "lts": true, "current": true,
	"nightly": true, "dev": true, "development": true, "canary": true, "next": true,
	"preview": true, "snapshot": true, "prod": true, "production": true, "live": true, "sts": true,
}

// prereleaseWords mark a candidate as not worth offering as an update.
var prereleaseWords = map[string]bool{
	"rc": true, "beta": true, "alpha": true, "dev": true, "develop": true, "development": true,
	"snapshot": true, "preview": true, "nightly": true, "wip": true, "tp": true, "pre": true,
	"test": true, "testing": true, "debug": true, "canary": true, "next": true, "edge": true,
	"master": true, "main": true,
}

// classifyTag decomposes an image tag into a comparable shape.
func classifyTag(tag string) tagShape {
	tag = strings.TrimSpace(tag)
	shape := tagShape{Raw: tag, Kind: TagUnknown}
	if tag == "" {
		return shape
	}
	if floatingTags[strings.ToLower(tag)] {
		shape.Kind = TagFloating
		return shape
	}
	// "477ac13-gfx1201-rocm7.2.4-c40a550" style tags: the numbers inside a mixed word are source hashes, not a version, so only dates can rank them.
	if shaLike(tag) {
		shape.Kind = TagOpaque
		return shape
	}
	if m := formMulti.FindStringSubmatch(tag); m != nil {
		shape.Pre, shape.Post, shape.Core = m[1], m[3], parseIntGroups(m[2])
		shape.Kind = numericKind(m[2])
		return shape
	}
	if m := formSingle.FindStringSubmatch(tag); m != nil {
		shape.Pre, shape.Post, shape.Core = m[1], m[3], parseIntGroups(m[2])
		shape.Kind = numericKind(m[2])
		return shape
	}
	if m := formLast.FindStringSubmatch(tag); m != nil && m[2] != "" {
		shape.Pre, shape.Post, shape.Core = m[1], m[3], parseIntGroups(m[2])
		shape.Kind = TagBuild
		return shape
	}
	shape.Kind = TagOpaque
	return shape
}

// isTagSep splits a tag into its words.
func isTagSep(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '/' || r == '+' }

// shaLike reports whether any word in the tag interleaves digits and letters (477ac13, c40a550), the signature of a source commit hash.
func shaLike(tag string) bool {
	for _, word := range strings.FieldsFunc(tag, isTagSep) {
		var sawDigit, sawLetterAfterDigit, sawDigitAfterLetter bool
		prevDigit := false
		for _, r := range word {
			switch {
			case r >= '0' && r <= '9':
				sawDigit = true
				if sawLetterAfterDigit {
					sawDigitAfterLetter = true
				}
				prevDigit = true
			case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
				if prevDigit {
					sawLetterAfterDigit = true
				}
				prevDigit = false
			}
			if sawDigit && sawDigitAfterLetter {
				return true
			}
		}
	}
	return false
}

// numericKind distinguishes semver from calendar/date tags.
func numericKind(digits string) TagKind {
	groups := strings.Split(digits, ".")
	if len(groups) == 1 {
		if len(groups[0]) == 8 && inYearRange(groups[0][:4]) {
			return TagDate
		}
		return TagSemver
	}
	if inYearRange(groups[0]) {
		return TagCalver
	}
	return TagSemver
}

func inYearRange(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1900 && n <= 2999
}

func parseIntGroups(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// comparableShapes reports whether two shapes may be compared at all.
func comparableShapes(cur, cand tagShape) bool {
	return cur.Kind == cand.Kind && cur.Pre == cand.Pre && cur.Post == cand.Post &&
		len(cur.Core) > 0 && len(cand.Core) > 0
}

// compareCore compares two version cores group by group, padding the shorter one with zeros (9.1 == 9.1.0): -1, 0 or +1.
func compareCore(a, b []int) int {
	for i := range max(len(a), len(b)) {
		if c := cmp.Compare(group(a, i), group(b, i)); c != 0 {
			return c
		}
	}
	return 0
}

// coreGreater reports whether a sorts after b.
func coreGreater(a, b []int) bool { return compareCore(a, b) > 0 }

// tagScopeOK reports whether cand is within the allowed scope of cur. Numeric groups are compared positionally.
func tagScopeOK(cur, cand tagShape, scope Scope) bool {
	switch scope {
	case ScopeMajor, ScopeAll:
		return true
	case ScopeMinor:
		return len(cur.Core) < 2 || group(cand.Core, 0) == group(cur.Core, 0)
	case ScopePatch:
		return len(cur.Core) < 2 ||
			(group(cand.Core, 0) == group(cur.Core, 0) && group(cand.Core, 1) == group(cur.Core, 1))
	}
	return true
}

// group returns core[i], or 0 when the tag has no such group.
func group(core []int, i int) int {
	if i < len(core) {
		return core[i]
	}
	return 0
}

// isPrerelease reports whether a tag's variant marker denotes a pre-release, a placeholder release (0.0.0-<date/sha>) or an attestation tag (.sig).
func isPrerelease(shape tagShape) bool {
	if allZero(shape.Core) {
		return true
	}
	for _, s := range []string{shape.Post, shape.Pre} {
		if s == "" {
			continue
		}
		if strings.Contains(strings.ToLower(s), ".sig") {
			return true
		}
		trimmed := strings.TrimLeft(s, "-._+ ")
		run := alphaRun.FindString(trimmed)
		if run != "" && prereleaseWords[strings.ToLower(run)] {
			return true
		}
	}
	return false
}

func allZero(core []int) bool {
	if len(core) == 0 {
		return false
	}
	for _, n := range core {
		if n != 0 {
			return false
		}
	}
	return true
}

// rankedCandidates returns every tag that can beat cur within scope, greatest first.
func rankedCandidates(cur tagShape, tags []string, scope Scope) []tagShape {
	var out []tagShape
	for _, raw := range tags {
		if raw == cur.Raw {
			continue
		}
		cand := classifyTag(raw)
		if !comparableShapes(cur, cand) || isPrerelease(cand) || !tagScopeOK(cur, cand, scope) {
			continue
		}
		if !coreGreater(cand.Core, cur.Core) {
			continue
		}
		out = append(out, cand)
	}
	// Greatest first; for the same version, the more precise spelling (9.1.0 before 9.1).
	sort.SliceStable(out, func(i, j int) bool {
		if c := compareCore(out[i].Core, out[j].Core); c != 0 {
			return c > 0
		}
		return len(out[i].Core) > len(out[j].Core)
	})
	return out
}

// shapePrefix is the literal prefix of a variant tag, used to prefer same-family date candidates.
func shapePrefix(shape tagShape) string {
	if shape.Pre == "" {
		return ""
	}
	if i := strings.LastIndexAny(shape.Pre, "-_.+/"); i > 0 {
		return shape.Pre[:i+1]
	}
	return shape.Pre
}

// variantSegments returns the identifying words of a tag: alphabetic or alphanumeric segments that are neither pure numbers nor commit hashes.
func variantSegments(tag string) []string {
	var out []string
	for _, seg := range strings.FieldsFunc(tag, isTagSep) {
		if shaLike(seg) {
			continue
		}
		stem := strings.Trim(seg, "0123456789")
		if stem == "" {
			continue
		}
		out = append(out, strings.ToLower(seg))
	}
	return out
}

// sameVariant keeps a date-based pick inside one build variant: master-vulkan never matches master-cuda.
func sameVariant(cur, cand string) bool {
	segs := variantSegments(cur)
	if len(segs) == 0 {
		return true
	}
	candSegs := map[string]bool{}
	for _, s := range variantSegments(cand) {
		candSegs[s] = true
	}
	for _, s := range segs {
		if !candSegs[s] {
			return false
		}
	}
	return true
}

// widthProbe anchors a search in the numeric family one digit-group wider: 2.0.0 looks after "1".
func widthProbe(tag string) string {
	shape := classifyTag(tag)
	if !shape.comparable() {
		return ""
	}
	return shape.Pre + "1"
}
