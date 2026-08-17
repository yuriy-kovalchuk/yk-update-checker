package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: https://example.com/app.git\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UpdateType != "all" {
		t.Errorf("UpdateType = %q, want all", cfg.UpdateType)
	}
	if cfg.ParallelChecks != DefaultParallelChecks {
		t.Errorf("ParallelChecks = %d, want %d", cfg.ParallelChecks, DefaultParallelChecks)
	}
}

func TestLoadRejectsCollidingRepoNames(t *testing.T) {
	_, err := Load(writeConfig(t, `repos:
  - name: team/app
    repo: https://example.com/a.git
  - name: team-app
    repo: https://example.com/b.git
`))
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Errorf("err = %v, want name collision error", err)
	}
}

func TestLoadRejectsUnnamedRepo(t *testing.T) {
	_, err := Load(writeConfig(t, "repos:\n  - repo: https://example.com/a.git\n"))
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("err = %v, want missing-name error", err)
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName("team/app one"); got != "team-app-one" {
		t.Errorf("SafeName = %q, want team-app-one", got)
	}
}

func TestRegistryCacheDuration(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: DefaultRegistryCacheTTL}, // unset = default
		{raw: "15m", want: 15 * time.Minute},
		{raw: "0", want: 0}, // explicit opt-out
		{raw: "90s", want: 90 * time.Second},
		{raw: "garbage", wantErr: true},
		{raw: "-5m", wantErr: true},
	}
	for _, tc := range cases {
		cfg := &Config{RegistryCacheTTL: tc.raw}
		got, err := cfg.RegistryCacheDuration()
		if tc.wantErr {
			if err == nil {
				t.Errorf("RegistryCacheDuration(%q): want error, got %v", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("RegistryCacheDuration(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("RegistryCacheDuration(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestLoadParsesRegistryCacheTTL(t *testing.T) {
	cfg, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: https://example.com/app.git\nregistry_cache_ttl: 30m\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RegistryCacheTTL != "30m" {
		t.Errorf("RegistryCacheTTL = %q, want 30m", cfg.RegistryCacheTTL)
	}
}
