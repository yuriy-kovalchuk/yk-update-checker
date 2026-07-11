// Package config loads and validates the application configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// DefaultParallelChecks is the default number of concurrent version checks.
const DefaultParallelChecks = 5

// RepoAuth holds credentials for a repository.
type RepoAuth struct {
	Type         string `yaml:"type"`
	Token        string `yaml:"token"`
	TokenFile    string `yaml:"token_file"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	SSHKeyPath   string `yaml:"ssh_key_path"`
}

// Repo is a single GitOps repository to scan.
type Repo struct {
	Name string   `yaml:"name"`
	URL  string   `yaml:"repo"`
	Path string   `yaml:"path"`
	Auth RepoAuth `yaml:"auth"`
}

// Config is loaded from the YAML config file.
type Config struct {
	Repos          []Repo `yaml:"repos"`
	UpdateType     string `yaml:"update_type"` // all | major | minor | patch
	ParallelChecks int    `yaml:"parallel_checks"`
	GitCacheDir    string `yaml:"git_cache_dir"`
}

// SetupLogger configures the default slog handler.
func SetupLogger(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// Load reads and parses a YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config: at least one repo is required")
	}
	// Repos are checked out into directories derived from their names; a
	// collision would make two repos clone into the same path concurrently.
	seen := make(map[string]string, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		if repo.Name == "" {
			return nil, fmt.Errorf("config: every repo needs a name")
		}
		dir := SafeName(repo.Name)
		if other, ok := seen[dir]; ok {
			return nil, fmt.Errorf("config: repo names %q and %q collide (both map to directory %q)", other, repo.Name, dir)
		}
		seen[dir] = repo.Name
	}
	if cfg.UpdateType == "" {
		cfg.UpdateType = "all"
	}
	if cfg.ParallelChecks <= 0 {
		cfg.ParallelChecks = DefaultParallelChecks
	}
	return &cfg, nil
}

// SafeName maps a repo name to a filesystem-safe directory name.
func SafeName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}
