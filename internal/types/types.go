package types

import "time"

// Result represents a dependency check outcome.
// This is the canonical type used across the application.
type Result struct {
	Source          string    `json:"source"`
	Chart           string    `json:"chart"`
	Dependency      string    `json:"dependency"`
	Type            string    `json:"type"`
	Protocol        string    `json:"protocol"`
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	Scope           string    `json:"scope"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at"`
}

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
