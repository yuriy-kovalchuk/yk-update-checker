package registry

import "testing"

func TestChartScope(t *testing.T) {
	tests := []struct {
		current, latest, want string
	}{
		{"1.0.0", "2.0.0", BumpMajor},
		{"3.5.1", "4.0.0", BumpMajor},
		{"1.0.0", "1.1.0", BumpMinor},
		{"1.2.3", "1.99.0", BumpMinor},
		{"1.0.0", "1.0.1", BumpPatch},
		{"1.0.0", "1.0.42", BumpPatch},
		{"1.0.0", "1.0.0", ""},          // same version → no bump
		{"1.0.0", "", ""},               // no latest
		{"abc", "def", BumpUnknown},     // non-semver, different strings
		{"v1.2.3", "v1.2.4", BumpPatch}, // v-prefixed semver
		{"0.1.0", "0.2.0", BumpMinor},   // major=0 minor change
		{"0.1.0", "0.1.1", BumpPatch},   // major=0 patch change
	}
	for _, tt := range tests {
		if got := ChartScope(tt.current, tt.latest); got != tt.want {
			t.Errorf("ChartScope(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestIsNewerChart(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"2.0.0", "1.0.0", true},
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "2.0.0", false},
		{"", "1.0.0", false},  // empty latest → never newer
		{"abc", "def", true},  // non-semver, different strings
		{"abc", "abc", false}, // non-semver, same string
	}
	for _, tt := range tests {
		if got := IsNewerChart(tt.latest, tt.current); got != tt.want {
			t.Errorf("IsNewerChart(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

// TestImageScope pins the rule that the reported bump uses the same tag shapes as the look-up.
func TestImageScope(t *testing.T) {
	tests := []struct {
		current, latest, want string
	}{
		{"1.24.2", "2.0.0", BumpMajor},
		{"v1.9.1", "v1.10.0", BumpMinor},
		{"1.25-alpine", "1.26-alpine", BumpMinor},
		{"3.21.0", "3.21.3", BumpPatch},
		{"9", "10", BumpUnknown}, // one group: same shape as a build counter, no boundary to filter on
		{"20240102", "20240203", BumpUnknown},
		{"2024.01.02", "2024.02.01", BumpUnknown},
		{"server-b4567", "server-b4890", BumpUnknown},
		{"1.0.0", "1.0.0", ""},
		{"1.0.0", "", ""},
		{"1.0.0-alpine", "1.1.0-debian", BumpUnknown}, // different variants never compare
	}
	for _, tt := range tests {
		if got := ImageScope(tt.current, tt.latest); got != tt.want {
			t.Errorf("ImageScope(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
		}
	}
}
