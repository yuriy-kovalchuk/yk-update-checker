// Package config loads and validates the application configuration.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// DefaultParallelChecks is the default number of concurrent version checks.
const DefaultParallelChecks = 5

// DefaultScanTimeout bounds one scan.
const DefaultScanTimeout = 30 * time.Minute

// RepoAuth holds credentials for a repository.
type RepoAuth struct {
	Type         string `yaml:"type"`
	Token        string `yaml:"token"`
	TokenFile    string `yaml:"token_file"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	SSHKeyPath   string `yaml:"ssh_key_path"`
	// KnownHostsFile enables real SSH host key verification.
	KnownHostsFile string `yaml:"known_hosts_file"`
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
	// CheckImages scans Kubernetes workloads for container image updates. Nil (unset) means true; set it to false to keep chart-only scanning.
	CheckImages *bool `yaml:"check_images"`
	// CheckDockerfiles scans container build definitions (Dockerfile, Containerfile) for the base images in their FROM lines.
	CheckDockerfiles *bool `yaml:"check_dockerfiles"`
	// VerifyImageTags rejects image tags that look like a newer version but are not a release (stray nightly tags, aliases of old builds).
	VerifyImageTags *bool `yaml:"verify_image_tags"`
	// RegistryAuthFile is a Docker config file (.dockerconfigjson) with credentials for private registries.
	RegistryAuthFile string `yaml:"registry_auth_file"`
	// ScanTimeout is a duration string (e.g. "30m") bounding one whole scan: clones, fetches and every registry lookup.
	ScanTimeout string `yaml:"scan_timeout"`
}

// ImagesEnabled reports whether container image checks are enabled.
func (c *Config) ImagesEnabled() bool { return c.CheckImages == nil || *c.CheckImages }

// DockerfilesEnabled reports whether Dockerfile base image checks are enabled.
func (c *Config) DockerfilesEnabled() bool {
	return c.CheckDockerfiles == nil || *c.CheckDockerfiles
}

// VerifyImageTagsEnabled reports whether candidate image tags are verified before they are reported as updates.
func (c *Config) VerifyImageTagsEnabled() bool {
	return c.VerifyImageTags == nil || *c.VerifyImageTags
}

// validUpdateTypes and validAuthTypes are validated, not defaulted: a typo silently changes what is reported.
var (
	validUpdateTypes = []string{"all", "major", "minor", "patch"}
	validAuthTypes   = []string{"token", "basic", "ssh"}
)

// SetupLogger configures the default slog handler.
func SetupLogger(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// Load reads, parses and validates a YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if err := checkBooleans(&doc); err != nil {
		return nil, err
	}
	var cfg Config
	if err := doc.Decode(&cfg); err != nil {
		return nil, err
	}
	// Unknown keys are warned about by path but still load, so a newer config runs on an older image.
	if unknown := unknownConfigKeys(&doc); len(unknown) > 0 {
		slog.Warn("config: keys in the config file are not read by this build",
			"keys", strings.Join(unknown, ", "),
			"hint", "misspelled, or not supported by this version; the default is used instead")
	}
	if err := cfg.validateRepos(); err != nil {
		return nil, err
	}
	// Case and whitespace are normalized here, so a working config like `ALL` keeps working.
	cfg.UpdateType = strings.ToLower(strings.TrimSpace(cfg.UpdateType))
	if cfg.UpdateType == "" {
		cfg.UpdateType = "all"
	}
	if !slices.Contains(validUpdateTypes, cfg.UpdateType) {
		return nil, fmt.Errorf("config: update_type %q is not one of %s", cfg.UpdateType, strings.Join(validUpdateTypes, ", "))
	}
	if cfg.ParallelChecks <= 0 {
		cfg.ParallelChecks = DefaultParallelChecks
	}
	if _, err := cfg.ScanDeadline(); err != nil {
		return nil, err
	}
	// Configured credentials that cannot be read fail now, not as a 401 on every private image later.
	if cfg.RegistryAuthFile != "" {
		if _, err := os.Stat(cfg.RegistryAuthFile); err != nil {
			return nil, fmt.Errorf("config: registry_auth_file: %w", err)
		}
	}
	return &cfg, nil
}

// checkBooleans names the switch it rejects, which yaml type errors do not.
func checkBooleans(doc *yaml.Node) error {
	root := documentMapping(doc)
	if root == nil {
		return nil
	}
	for _, key := range []string{"check_images", "check_dockerfiles", "verify_image_tags"} {
		v := child(root, key)
		if v == nil || v.Tag == "!!null" {
			continue
		}
		if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
			return fmt.Errorf("config: %s must be true or false, got %v", key, v.Value)
		}
	}
	return nil
}

// validateRepos checks every repo entry and normalises it in place.
func (c *Config) validateRepos() error {
	if len(c.Repos) == 0 {
		return fmt.Errorf("config: at least one repo is required")
	}
	// Repos are checked out into directories derived from their names; a collision would make two repos clone into the same path concurrently.
	seen := make(map[string]string, len(c.Repos))
	for i := range c.Repos {
		repo := &c.Repos[i] // through a pointer: normalising a copy would be lost
		if repo.Name == "" {
			return fmt.Errorf("config: every repo needs a name")
		}
		dir := SafeName(repo.Name)
		// SafeName keeps '.', so "." and ".." are the only names that address the workspace itself or its parent.
		if strings.HasPrefix(dir, ".") {
			return fmt.Errorf("config: repo name %q is not usable as a checkout directory name", repo.Name)
		}
		if len(dir) > maxRepoDirLen {
			return fmt.Errorf("config: repo name %q is too long: it maps to a %d character directory name, limit %d", repo.Name, len(dir), maxRepoDirLen)
		}
		// path selects a directory inside the checkout; an absolute or ".." path would point the scan at the pod's own filesystem.
		if repo.Path != "" && !filepath.IsLocal(repo.Path) {
			return fmt.Errorf("config: repo %q path %q must be a relative path inside the repository", repo.Name, repo.Path)
		}
		scheme, err := classifyRepoURL(repo.URL)
		if err != nil {
			return err
		}
		if err := repo.Auth.validate(repo, scheme); err != nil {
			return err
		}
		if other, ok := seen[dir]; ok {
			return fmt.Errorf("config: repo names %q and %q collide (both map to directory %q)", other, repo.Name, dir)
		}
		seen[dir] = repo.Name
	}
	return nil
}

// validate checks one repo's auth block against the transport it will use.
func (a *RepoAuth) validate(repo *Repo, scheme string) error {
	// TrimSpace is assigned through the pointer: writing it to a copy would validate the trimmed value and store the untrimmed one.
	a.Type = strings.TrimSpace(a.Type)
	if a.Type != "" && !slices.Contains(validAuthTypes, a.Type) {
		return fmt.Errorf("config: repo %q auth.type %q is not one of %s", repo.Name, a.Type, strings.Join(validAuthTypes, ", "))
	}
	// The mirror image of the typo check: credentials configured but no auth.type.
	if a.Type == "" && (a.Token != "" || a.TokenFile != "" || a.Password != "" ||
		a.PasswordFile != "" || a.Username != "" || a.SSHKeyPath != "") {
		return fmt.Errorf("config: repo %q configures credentials but no auth.type (token | basic | ssh)", repo.Name)
	}
	// token and basic hand git an Authorization header; on any transport but https that header leaves the machine in plaintext.
	if (a.Type == "token" || a.Type == "basic") && scheme != "https" {
		return fmt.Errorf("config: repo %q auth.type %q sends an Authorization header, but %q is a %s location - the credential would travel unencrypted; use https, or drop the credential", repo.Name, a.Type, repo.URL, scheme)
	}
	return nil
}

// ScanDeadline parses ScanTimeout. Zero means "no deadline" (opt-out); empty means DefaultScanTimeout.
func (c *Config) ScanDeadline() (time.Duration, error) {
	if c.ScanTimeout == "" {
		return DefaultScanTimeout, nil
	}
	d, err := time.ParseDuration(c.ScanTimeout)
	if err != nil {
		return 0, fmt.Errorf("config: invalid scan_timeout %q: %w", c.ScanTimeout, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: scan_timeout must not be negative, got %s", d)
	}
	return d, nil
}

// yamlFieldNames lists the yaml keys a struct accepts, read from its tags so the list cannot drift away from the type that consumes them.
func yamlFieldNames(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			name = strings.ToLower(t.Field(i).Name)
		}
		out[name] = true
	}
	return out
}

var (
	configKeys = yamlFieldNames(Config{})
	repoKeys   = yamlFieldNames(Repo{})
	authKeys   = yamlFieldNames(RepoAuth{})
)

// unknownConfigKeys returns the keys in a config document that this build does not read, as paths (repos[0].auth.existingSecret).
func unknownConfigKeys(doc *yaml.Node) []string {
	root := documentMapping(doc)
	if root == nil {
		return nil
	}
	var unknown []string
	add := func(path string) { unknown = append(unknown, path) }
	scanKeys(root, configKeys, "", add)
	if repos := child(root, "repos"); repos != nil && repos.Kind == yaml.SequenceNode {
		for i, item := range repos.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			prefix := fmt.Sprintf("repos[%d]", i)
			scanKeys(item, repoKeys, prefix, add)
			if auth := child(item, "auth"); auth != nil && auth.Kind == yaml.MappingNode {
				scanKeys(auth, authKeys, prefix+".auth", add)
			}
		}
	}
	return unknown
}

// documentMapping returns the top-level mapping of a parsed document, or nil.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	return doc.Content[0]
}

// scanKeys reports every key of a mapping node that known does not contain.
func scanKeys(n *yaml.Node, known map[string]bool, prefix string, add func(string)) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if !known[key] {
			if prefix == "" {
				add(key)
			} else {
				add(prefix + "." + key)
			}
		}
	}
}

// child returns the value node for key in a mapping node, or nil.
func child(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// maxRepoDirLen bounds a repo's derived checkout directory name.
const maxRepoDirLen = 100

// classifyRepoURL rejects option-, command- and credentialed URLs and returns the transport: https, http, ssh, git, file, local.
func classifyRepoURL(u string) (string, error) {
	if u == "" {
		return "", fmt.Errorf("config: every repo needs a repo URL")
	}
	if strings.HasPrefix(u, "-") || strings.Contains(u, "::") {
		return "", fmt.Errorf("config: repo URL %q is not a valid repository location", u)
	}
	scheme := "local"
	if i := strings.Index(u, "://"); i >= 0 {
		scheme = strings.ToLower(u[:i])
		if !slices.Contains([]string{"https", "http", "ssh", "git", "file"}, scheme) {
			return "", fmt.Errorf("config: repo URL %q uses an unsupported transport %q — use https, http, ssh, git or file", u, u[:i+3])
		}
	} else if strings.Contains(u, ":") && !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, ".") {
		scheme = "ssh" // scp-style: [user@]host:path
	}
	_, pass, found := userinfo(u)
	if found && (pass != "" || scheme == "http" || scheme == "https") {
		return "", fmt.Errorf("config: repo URL %q carries credentials in the URL — use the repo's auth block (type + token_file / password_file), which keeps them out of the config file, the argv, .git/config and the log", u)
	}
	return scheme, nil
}

// userinfo splits the user[:password] that a URL or scp-style location puts before the host.
func userinfo(u string) (user, pass string, found bool) {
	rest := u
	if i := strings.Index(u, "://"); i >= 0 {
		rest = u[i+3:]
	}
	authority := rest
	if j := strings.IndexByte(authority, '/'); j >= 0 {
		authority = authority[:j]
	}
	// scp-style locations end in ":path", which is part of the URL, not a login.
	if !strings.Contains(u, "://") {
		if k := strings.LastIndexByte(authority, ':'); k > strings.LastIndexByte(authority, '@') {
			authority = authority[:k]
		}
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return "", "", false
	}
	user = authority[:at]
	if c := strings.IndexByte(user, ':'); c >= 0 {
		return user[:c], user[c+1:], true
	}
	return user, "", true
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
