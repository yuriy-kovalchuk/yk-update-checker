package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainerLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	missing := filepath.Join(dir, "missing")

	cases := []struct {
		name   string
		files  []string
		want   int64
		wantOK bool
	}{
		{"cgroup v2 limit", []string{write("v2", "536870912\n")}, 536870912, true},
		{"cgroup v2 unlimited", []string{write("v2max", "max\n")}, 0, false},
		{"falls through to v1", []string{missing, write("v1", "268435456")}, 268435456, true},
		{"v1 unlimited sentinel", []string{write("v1max", "9223372036854771712")}, 0, false},
		{"no cgroup files", []string{missing}, 0, false},
	}
	for _, tc := range cases {
		got, ok := containerLimit(tc.files)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: containerLimit = %d, %v; want %d, %v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestApplyRespectsGOMEMLIMIT(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "100MiB")
	if _, applied := Apply(); applied {
		t.Error("Apply overrode an explicit GOMEMLIMIT")
	}
}
