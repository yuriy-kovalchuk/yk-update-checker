package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
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

// TestLoadRejectsEscapingRepoNames covers names whose SafeName output would address the workspace itself or a parent of it (B5).
func TestLoadRejectsEscapingRepoNames(t *testing.T) {
	for _, name := range []string{`"."`, `".."`, `"..."`, `"..-nested"`} {
		_, err := Load(writeConfig(t, "repos:\n  - name: "+name+"\n    repo: https://example.com/a.git\n"))
		if err == nil || !strings.Contains(err.Error(), "checkout directory") {
			t.Errorf("name %s: err = %v, want unusable-directory error", name, err)
		}
	}
}

func TestLoadRejectsOverlongRepoName(t *testing.T) {
	name := strings.Repeat("a", maxRepoDirLen+1)
	_, err := Load(writeConfig(t, "repos:\n  - name: "+name+"\n    repo: https://example.com/a.git\n"))
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("err = %v, want too-long error", err)
	}
}

// TestLoadRepoURLTransport checks that git option- and command-shaped URLs are rejected while every transport the tool documents is accepted (B4).
func TestScanDeadline(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
		err   bool
	}{
		{"", DefaultScanTimeout, false},
		{"45m", 45 * time.Minute, false},
		{"0", 0, false},
		{"-5m", 0, true},
		{"half an hour", 0, true},
	}
	for _, c := range cases {
		cfg := &Config{ScanTimeout: c.value}
		got, err := cfg.ScanDeadline()
		if c.err {
			if err == nil {
				t.Errorf("scan_timeout %q: want error, got %s", c.value, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("scan_timeout %q: got %s (%v), want %s", c.value, got, err, c.want)
		}
	}
}

func TestLoadRejectsCredentialsInRepoURL(t *testing.T) {
	// A credentialed URL is a credential in the config file, in the git argv, in the cached clone's .git/config and in any log line that names the repo.
	for _, url := range []string{
		"https://user:ghp_secret@github.com/org/repo.git",
		"https://x-access-token:ghp_secret@github.com/org/repo.git",
		"https://tok@github.com/org/repo.git",
		"http://user:pass@example.com/a.git",
		"ssh://git:pw@example.com/a.git",
		"user:pw@example.com:org/repo.git", // scp-style with a password
	} {
		if _, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: "+url+"\n")); err == nil {
			t.Errorf("Load accepted a credentialed URL %q", url)
		}
	}
	// A login without a password stays legal where it means the ssh user.
	for _, url := range []string{
		"https://github.com/org/repo.git",
		"ssh://git@example.com/a.git",
		"git@github.com:org/repo.git",
		"/srv/git/a.git",
		"file:///srv/git/a.git",
		"https://example.com/a@b.git", // @ inside the path is not a login
	} {
		if _, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: "+url+"\n")); err != nil {
			t.Errorf("Load rejected a plain URL %q: %v", url, err)
		}
	}
}

func TestLoadRequiresHTTPSForCredentialAuth(t *testing.T) {
	// token and basic send an Authorization header; anywhere but https it is plaintext on the wire.
	cases := []struct{ url, auth string }{
		{"http://example.com/a.git", "type: token\n        token_file: /tmp/t"},
		{"git@example.com:org/repo.git", "type: token\n        token: x"},
		{"/srv/git/a.git", "type: basic\n        password_file: /tmp/p"},
		{"git://example.com/a.git", "type: basic\n        username: u\n        password: p"},
	}
	for _, c := range cases {
		cfg := "repos:\n  - name: app\n    repo: " + c.url + "\n    auth:\n        " + c.auth + "\n"
		if _, err := Load(writeConfig(t, cfg)); err == nil {
			t.Errorf("Load accepted credential auth over %s", c.url)
		} else if !strings.Contains(err.Error(), "unencrypted") {
			t.Errorf("error for %s should name the transport, got %v", c.url, err)
		}
	}
	ok := "repos:\n  - name: app\n    repo: https://example.com/a.git\n    auth:\n        type: token\n        token_file: /tmp/t\n"
	if _, err := Load(writeConfig(t, ok)); err != nil {
		t.Errorf("https + token should load, got %v", err)
	}
	// ssh and unauthenticated http are unaffected by the rule.
	for _, url := range []string{"http://example.com/a.git", "git@example.com:org/repo.git"} {
		if _, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: "+url+"\n")); err != nil {
			t.Errorf("%s without credentials should load, got %v", url, err)
		}
	}
}

func TestLoadRejectsCredentialsWithoutAuthType(t *testing.T) {
	// authEnv keys off the type; without it the credential is read and then never used, and the scan fails as an unauthenticated permission error.
	for _, auth := range []string{
		"token_file: /tmp/t",
		"token: x",
		"password_file: /tmp/p",
		"username: u",
		"ssh_key_path: /keys/id",
	} {
		cfg := "repos:\n  - name: app\n    repo: https://example.com/a.git\n    auth:\n        " + auth + "\n"
		if _, err := Load(writeConfig(t, cfg)); err == nil {
			t.Errorf("Load accepted %q with no auth.type", auth)
		}
	}
}

// TestLoadNormalizesAuthTypeInPlace: normalisation must reach the stored config, not only the validated copy.
func TestLoadNormalizesAuthTypeInPlace(t *testing.T) {
	cfg := "repos:\n  - name: app\n    repo: https://example.com/a.git\n    auth:\n        type:  basic \n        password_file: /tmp/p\n"
	got, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if got.Repos[0].Auth.Type != "basic" {
		t.Errorf("stored auth.type = %q, want basic", got.Repos[0].Auth.Type)
	}
	// auth.type stays case-sensitive: "Basic" errors instead of silently disabling the credential.
	if _, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: https://example.com/a.git\n    auth:\n        type: Basic\n        token_file: /tmp/t\n")); err == nil {
		t.Error("auth.type \"Basic\" should be rejected, not silently lowercased")
	}
}

func TestLoadRepoURLTransport(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{url: "https://example.com/a.git"},
		{url: "http://example.com/a.git"},
		{url: "ssh://git@example.com/a.git"},
		{url: "git://example.com/a.git"},
		{url: "file:///srv/git/a.git"},
		{url: "git@github.com:org/repo.git"}, // scp-style ssh carries no scheme
		{url: "/srv/git/a.git"},              // local checkout (tests, local testing)
		{url: `"ext::sh -c touch%20/tmp/pwned"`, wantErr: true},
		{url: `"-cprotocol.ext.allow=always"`, wantErr: true},
		{url: `"smb://host/share"`, wantErr: true},
		{url: `""`, wantErr: true},
	}
	for _, c := range cases {
		_, err := Load(writeConfig(t, "repos:\n  - name: app\n    repo: "+c.url+"\n"))
		if c.wantErr && err == nil {
			t.Errorf("repo %s: err = nil, want rejection", c.url)
		}
		if !c.wantErr && err != nil {
			t.Errorf("repo %s: err = %v, want accepted", c.url, err)
		}
	}
}

// Both image switches are tri-state: unset means on.
func TestLoadImageSwitches(t *testing.T) {
	const repoOnly = "repos:\n  - name: app\n    repo: https://example.com/app.git\n"
	cases := []struct {
		name, body string
		verify     bool
		images     bool
		wantErr    bool
	}{
		{name: "unset", body: repoOnly, verify: true, images: true},
		{name: "explicit on", body: repoOnly + "verify_image_tags: true\ncheck_images: true\n", verify: true, images: true},
		{name: "explicit off", body: repoOnly + "verify_image_tags: false\ncheck_images: false\n", verify: false, images: false},
		{name: "not a bool", body: repoOnly + "verify_image_tags: careful\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "verify_image_tags") {
					t.Fatalf("err = %v, want it to name verify_image_tags", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.VerifyImageTagsEnabled(); got != tc.verify {
				t.Errorf("VerifyImageTagsEnabled = %v, want %v", got, tc.verify)
			}
			if got := cfg.ImagesEnabled(); got != tc.images {
				t.Errorf("ImagesEnabled = %v, want %v", got, tc.images)
			}
		})
	}
}

func TestLoadDockerfileSwitch(t *testing.T) {
	const repoOnly = "repos:\n  - name: app\n    repo: https://example.com/app.git\n"
	cases := []struct {
		name, body string
		want       bool
		wantErr    bool
	}{
		{name: "unset", body: repoOnly, want: true},
		{name: "explicit on", body: repoOnly + "check_dockerfiles: true\n", want: true},
		{name: "explicit off", body: repoOnly + "check_dockerfiles: false\n", want: false},
		{name: "not a bool", body: repoOnly + "check_dockerfiles: sure\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "check_dockerfiles") {
					t.Fatalf("err = %v, want it to name check_dockerfiles", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.DockerfilesEnabled(); got != tc.want {
				t.Errorf("DockerfilesEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoadWarnsAboutUnknownKeys is B6.
func TestLoadWarnsAboutUnknownKeys(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(prev)

	path := writeConfig(t, `
repos:
  - name: p
    repo: https://github.com/x/y.git
    branch: master
    auth:
      type: token
      existingSecret: token
      existingSecretKey: token
git_cache_dirr: /tmp/git-cache
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("unknown keys must not fail the load: %v", err)
	}
	out := logged.String()
	for _, want := range []string{"repos[0].auth.existingSecret", "repos[0].auth.existingSecretKey", "git_cache_dirr"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning does not name %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not read by this build") {
		t.Errorf("warning does not say what is wrong:\n%s", out)
	}
}

func TestLoadRejectsInvalidUpdateType(t *testing.T) {
	for _, bad := range []string{"pathc", "majorr", "none", "all-the-versions"} {
		path := writeConfig(t, "repos:\n  - name: a\n    repo: https://github.com/x/y.git\nupdate_type: "+bad+"\n")
		if _, err := Load(path); err == nil {
			t.Errorf("update_type %q accepted", bad)
		} else if !strings.Contains(err.Error(), "update_type") {
			t.Errorf("error for %q does not name the key: %v", bad, err)
		}
	}
}

// TestLoadNormalizesCaseAndWhitespace: rejecting typos must not reject formatting that works today.
func TestLoadNormalizesCaseAndWhitespace(t *testing.T) {
	for _, in := range []string{"ALL", " Minor ", "patch\t"} {
		path := writeConfig(t, "repos:\n  - name: a\n    repo: https://github.com/x/y.git\nupdate_type: "+in+"\n")
		cfg, err := Load(path)
		if err != nil {
			t.Errorf("update_type %q rejected: %v", in, err)
			continue
		}
		if strings.TrimSpace(cfg.UpdateType) != strings.ToLower(strings.TrimSpace(in)) {
			t.Errorf("update_type %q stored as %q", in, cfg.UpdateType)
		}
	}
}

func TestLoadRejectsInvalidAuthType(t *testing.T) {
	// `tokne` used to mean "no auth header at all": the clone succeeded against public repos and failed confusingly against private ones.
	for _, bad := range []string{"tokne", "bearer", "Basic"} {
		path := writeConfig(t, "repos:\n  - name: a\n    repo: https://github.com/x/y.git\n    auth:\n      type: "+bad+"\n      token: x\n")
		if _, err := Load(path); err == nil {
			t.Errorf("auth.type %q accepted", bad)
		} else if !strings.Contains(err.Error(), "auth.type") {
			t.Errorf("error for %q does not name the key: %v", bad, err)
		}
	}
	for _, good := range []string{"token", "basic", "ssh"} {
		path := writeConfig(t, "repos:\n  - name: a\n    repo: https://github.com/x/y.git\n    auth:\n      type: "+good+"\n      token: x\n")
		if _, err := Load(path); err != nil {
			t.Errorf("auth.type %q rejected: %v", good, err)
		}
	}
}

// TestEnumsMatchTheCodeTheyFeed keeps the two enum lists in step with the code that interprets them.
func TestEnumsMatchTheCodeTheyFeed(t *testing.T) {
	for _, v := range validUpdateTypes {
		if got := registry.ParseScope(v); string(got) != v {
			t.Errorf("update_type %q is valid in config but registry.ParseScope yields %q", v, string(got))
		}
	}
	for _, v := range []string{"pathc", "major-only", "none"} {
		if got := registry.ParseScope(v); string(got) != string(registry.ScopeAll) {
			t.Errorf("registry.ParseScope(%q) = %q, want the all-scope fallback this test documents", v, string(got))
		}
	}
	// The scan-side switch is package-private, so the alignment is asserted by name: these are the three cases authEnv handles.
	if diff := []string{"token", "basic", "ssh"}; !slices.Equal(validAuthTypes, diff) {
		t.Errorf("validAuthTypes = %v, want %v (see scan.authEnv)", validAuthTypes, diff)
	}
}

// TestShippedConfigsHaveNoUnknownKeys: every config this repo ships must parse against the real struct.
func TestShippedConfigsHaveNoUnknownKeys(t *testing.T) {
	for _, f := range []string{
		"../../examples/config.yaml",
		"../../examples/config.example.yaml",
		"../../charts/yk-update-checker/values.yaml",
	} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.HasSuffix(f, "values.yaml") {
			// scanner.config is emitted verbatim as the ConfigMap payload, so its keys are config keys.
			var v struct {
				Scanner struct {
					Config map[string]any `yaml:"config"`
				} `yaml:"scanner"`
			}
			if err := yaml.Unmarshal(data, &v); err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			var keys []string
			for k := range v.Scanner.Config {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				if !configKeys[k] {
					t.Errorf("%s: scanner.config key %q is not read by internal/config", f, k)
				}
			}
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if unknown := unknownConfigKeys(&doc); len(unknown) > 0 {
			t.Errorf("%s: keys not read by this build: %v", f, unknown)
		}
	}
}

// TestLoadRejectsBadDurations: a bad scan_timeout fails at load, not mid-scan.
func TestLoadRejectsBadDurations(t *testing.T) {
	for _, line := range []string{"scan_timeout: 30 minutes", "scan_timeout: -5m"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		body := line + "\nrepos:\n  - name: a\n    repo: https://example.com/a.git\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("Load accepted %q", line)
		}
	}
}

func TestLoadRejectsScanPathsOutsideTheRepository(t *testing.T) {
	for _, p := range []string{"../other", "/etc", "apps/../../x"} {
		body := "repos:\n  - name: a\n    repo: https://example.com/a.git\n    path: " + p + "\n"
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("Load accepted path %q", p)
		}
	}
	if _, err := Load(writeConfig(t, "repos:\n  - name: a\n    repo: https://example.com/a.git\n    path: kubernetes/apps\n")); err != nil {
		t.Errorf("Load rejected a normal sub-path: %v", err)
	}
}

func TestLoadRegistryAuthFile(t *testing.T) {
	missing := "registry_auth_file: /nonexistent/config.json\nrepos:\n  - name: a\n    repo: https://example.com/a.git\n"
	if _, err := Load(writeConfig(t, missing)); err == nil {
		t.Error("Load accepted a registry_auth_file that does not exist")
	}
	auth := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeConfig(t, "registry_auth_file: "+auth+"\nrepos:\n  - name: a\n    repo: https://example.com/a.git\n"))
	if err != nil || cfg.RegistryAuthFile != auth {
		t.Errorf("Load = %v, %v; want registry_auth_file kept", cfg, err)
	}
}
