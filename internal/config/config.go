// Package config loads and validates the application configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"

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
	if cfg.UpdateType == "" {
		cfg.UpdateType = "all"
	}
	if cfg.ParallelChecks <= 0 {
		cfg.ParallelChecks = DefaultParallelChecks
	}
	return &cfg, nil
}
