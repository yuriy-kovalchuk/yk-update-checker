package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

func writeAuthFile(t *testing.T, cfg map[string]any) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestAuthFileKeychain(t *testing.T) {
	path := writeAuthFile(t, map[string]any{
		"auths": map[string]any{
			"https://harbor.example.com/": map[string]string{"auth": b64("robot$scan:s3cret")},
			"https://index.docker.io/v1/": map[string]string{"username": "hubuser", "password": "hubpass"},
		},
		// Must never be consulted: it names a program to run.
		"credHelpers": map[string]string{"ghcr.io": "evil"},
	})
	kc := authFileKeychain{path: path}
	resolve := func(host string) authn.AuthConfig {
		reg, err := name.NewRegistry(host)
		if err != nil {
			t.Fatal(err)
		}
		a, err := kc.Resolve(reg)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", host, err)
		}
		cfg, err := a.Authorization()
		if err != nil {
			t.Fatal(err)
		}
		return *cfg
	}
	if got := resolve("harbor.example.com"); got.Username != "robot$scan" || got.Password != "s3cret" {
		t.Errorf("harbor: %+v, want robot$scan / s3cret from the base64 auth field", got)
	}
	if got := resolve("index.docker.io"); got.Username != "hubuser" {
		t.Errorf("docker hub: %+v, want hubuser via the https://index.docker.io/v1/ key", got)
	}
	if got := resolve("ghcr.io"); got.Username != "" {
		t.Errorf("ghcr.io: %+v, want anonymous (credHelpers are ignored)", got)
	}

	reg, _ := name.NewRegistry("harbor.example.com")
	if _, err := (authFileKeychain{path: filepath.Join(t.TempDir(), "missing")}).Resolve(reg); err == nil {
		t.Error("a configured but missing auth file resolved silently")
	}
}

// TestPrivateRegistryWithAuthFile: a registry that requires basic auth answers with the credentials from the file, and without them the failure is "auth".
func TestPrivateRegistryWithAuthFile(t *testing.T) {
	const user, pass = "robot", "s3cret"
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			_, _ = w.Write([]byte(`{"name":"private/app","tags":["1.0.0","1.1.0"]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	host := "localhost:" + strings.Split(ts.Listener.Addr().String(), ":")[1]
	path := writeAuthFile(t, map[string]any{"auths": map[string]any{host: map[string]string{"auth": b64(user + ":" + pass)}}})

	withCreds := NewIndexCache(withAnonymousAuth(), WithRegistryAuthFile(path), WithVerifyTags(false))
	info, err := withCreds.LatestImage(context.Background(), host+"/private/app", "1.0.0", "", ScopeAll)
	if err != nil || info.Latest != "1.1.0" {
		t.Errorf("with the auth file: latest %q, err %v; want 1.1.0", info.Latest, err)
	}

	anonymous := NewIndexCache(withAnonymousAuth(), WithVerifyTags(false))
	if _, err := anonymous.LatestImage(context.Background(), host+"/private/app", "1.0.0", "", ScopeAll); errkind.Of(err) != errkind.Auth {
		t.Errorf("without credentials: kind %q (%v), want auth", errkind.Of(err), err)
	}
}

// TestHarborAPIGetsRegistryCredentials: the artifacts API of a private Harbor project is called with the robot account from the auth file.
func TestHarborAPIGetsRegistryCredentials(t *testing.T) {
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"digest": "sha256:aa", "push_time": "2026-10-01T08:00:00Z"})
	}))
	defer api.Close()
	host := strings.TrimPrefix(api.URL, "http://")
	path := writeAuthFile(t, map[string]any{"auths": map[string]any{host: map[string]string{"auth": b64("robot:s3cret")}}})

	cache := NewIndexCache(withAnonymousAuth(), WithRegistryAuthFile(path),
		withVendorAPI(host, vendorAPI{kind: apiHarbor, base: api.URL}))
	if _, err := cache.fetchTagInfo(context.Background(), host+"/homelab/app", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if want := "Basic " + b64("robot:s3cret"); gotAuth != want {
		t.Errorf("Authorization = %q, want the robot account", gotAuth)
	}
}
