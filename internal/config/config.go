package config

import (
	"log/slog"
	"os"
	"time"

	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/constants"
	"github.com/yuriy-kovalchuk/yk-helm-update-checker/internal/types"
	"gopkg.in/yaml.v3"
)

// Build metadata — set via -ldflags at build time.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

type Repo struct {
	Name string        `yaml:"name"`
	URL  string        `yaml:"repo"`
	Path string        `yaml:"path"`
	Auth types.RepoAuth `yaml:"auth"`
}

type Config struct {
	Repos          []Repo        `yaml:"repos"`
	UpdateType     string        `yaml:"update_type"`
	ParallelChecks int           `yaml:"parallel_checks"`
	GitCacheDir    string        `yaml:"git_cache_dir"`
	ScanInterval   time.Duration `yaml:"scan_interval"`
	StartupScan    bool          `yaml:"startup_scan"`
	StartupDelay   time.Duration `yaml:"startup_delay"`
}

// SetupLogger configures the default slog handler based on the verbose flag.
func SetupLogger(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.UpdateType == "" {
		cfg.UpdateType = "all"
	}
	if cfg.ParallelChecks <= 0 {
		cfg.ParallelChecks = constants.DefaultParallelChecks
	}
	return &cfg, nil
}
