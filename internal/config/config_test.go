package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
