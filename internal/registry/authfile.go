package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// WithRegistryAuthFile authenticates to registries from a Docker config file (.dockerconfigjson).
func WithRegistryAuthFile(path string) CacheOption {
	return func(c *IndexCache) {
		c.keychain = authn.NewMultiKeychain(authFileKeychain{path: path}, c.keychain)
	}
}

// authFileKeychain resolves credentials from the static "auths" entries of a Docker config file.
type authFileKeychain struct{ path string }

// dockerConfig is the part of ~/.docker/config.json this keychain reads.
type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

type dockerAuth struct {
	Auth          string `json:"auth"` // base64("user:password")
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// Resolve implements authn.Keychain.
func (k authFileKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	data, err := os.ReadFile(k.path)
	if err != nil {
		// An unreadable credential file is an error, not anonymous access: dropping it would look like a 401.
		return nil, fmt.Errorf("registry_auth_file: %w", err)
	}
	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("registry_auth_file is not a Docker config file: %w", err)
	}
	host := target.RegistryStr()
	for key, a := range cfg.Auths {
		if registryHost(key) != host {
			continue
		}
		user, pass := a.Username, a.Password
		if a.Auth != "" && user == "" {
			raw, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				return nil, fmt.Errorf("registry_auth_file: auth for %s is not base64: %w", key, err)
			}
			user, pass, _ = strings.Cut(string(raw), ":")
		}
		return authn.FromConfig(authn.AuthConfig{
			Username: user, Password: pass,
			IdentityToken: a.IdentityToken, RegistryToken: a.RegistryToken,
		}), nil
	}
	return authn.Anonymous, nil
}

// registryHost turns a Docker config "auths" key into the host go-containerregistry reports.
func registryHost(key string) string {
	if i := strings.Index(key, "://"); i >= 0 {
		key = key[i+3:]
	}
	key, _, _ = strings.Cut(key, "/")
	switch key {
	case "docker.io", "registry-1.docker.io":
		return name.DefaultRegistry
	}
	return key
}

// basicAuthFor gives vendor APIs (Harbor) the plain basic auth the keychain holds for a host.
func (c *IndexCache) basicAuthFor(host string) (user, pass string, ok bool) {
	reg, err := name.NewRegistry(host)
	if err != nil {
		return "", "", false
	}
	a, err := c.keychain.Resolve(reg)
	if err != nil || a == authn.Anonymous {
		return "", "", false
	}
	cfg, err := a.Authorization()
	if err != nil || cfg.Username == "" || cfg.Password == "" {
		return "", "", false
	}
	return cfg.Username, cfg.Password, true
}
