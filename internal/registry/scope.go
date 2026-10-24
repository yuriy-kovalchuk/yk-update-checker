package registry

import "github.com/Masterminds/semver/v3"

// Bump sizes reported for an available update.
const (
	BumpMajor   = "major"
	BumpMinor   = "minor"
	BumpPatch   = "patch"
	BumpUnknown = "unknown" // no version boundary to measure (dates, builds, git SHAs)
)

// IsNewerChart reports whether latest is a newer chart version than current.
func IsNewerChart(latest, current string) bool {
	if latest == "" {
		return false
	}
	l, c, ok := parseVersionPair(current, latest)
	if !ok {
		return latest != current
	}
	return l.GreaterThan(c)
}

// ChartScope classifies the bump from current to latest for a chart version, which Helm requires to be semver.
func ChartScope(current, latest string) string {
	if latest == "" || current == latest {
		return ""
	}
	l, c, ok := parseVersionPair(current, latest)
	if !ok {
		return BumpUnknown
	}
	return bumpOf(l.Major(), c.Major(), l.Minor(), c.Minor())
}

// ImageScope classifies an image bump with the same tag-shape rules that chose latest.
func ImageScope(current, latest string) string {
	if latest == "" || current == latest {
		return ""
	}
	cur, cand := classifyTag(current), classifyTag(latest)
	if !comparableShapes(cur, cand) || cur.Kind != TagSemver || len(cur.Core) < 2 {
		return BumpUnknown
	}
	return bumpOf(group(cand.Core, 0), group(cur.Core, 0), group(cand.Core, 1), group(cur.Core, 1))
}

// bumpOf names the first version group that differs.
func bumpOf[N uint64 | int](latestMajor, curMajor, latestMinor, curMinor N) string {
	switch {
	case latestMajor != curMajor:
		return BumpMajor
	case latestMinor != curMinor:
		return BumpMinor
	default:
		return BumpPatch
	}
}

// parseVersionPair parses current and latest as semver; ok is false when either fails to parse.
func parseVersionPair(current, latest string) (l, c *semver.Version, ok bool) {
	l, err1 := semver.NewVersion(latest)
	c, err2 := semver.NewVersion(current)
	if err1 != nil || err2 != nil {
		return nil, nil, false
	}
	return l, c, true
}

// Scope controls which upgrade levels are eligible.
type Scope string

// Eligible upgrade levels for version comparisons.
const (
	ScopeAll   Scope = "all"
	ScopeMajor Scope = "major"
	ScopeMinor Scope = "minor"
	ScopePatch Scope = "patch"
)

var validScopes = map[Scope]bool{
	ScopeAll: true, ScopeMajor: true, ScopeMinor: true, ScopePatch: true,
}

// IsValid reports whether s is a recognised scope value.
func (s Scope) IsValid() bool { return validScopes[s] }

// ParseScope converts a string to a Scope, falling back to ScopeAll.
func ParseScope(s string) Scope {
	sc := Scope(s)
	if sc.IsValid() {
		return sc
	}
	return ScopeAll
}
