package gitsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

func mustAuthEnv(t *testing.T, repo config.Repo) []string {
	t.Helper()
	env, err := authEnv(repo)
	if err != nil {
		t.Fatalf("authEnv: %v", err)
	}
	return env
}

func TestAuthEnvTokenUsesHeaderNotURL(t *testing.T) {
	repo := config.Repo{Name: "r", URL: "https://example.invalid/r.git", Auth: config.RepoAuth{Type: "token", Token: "s3cret"}}
	env := mustAuthEnv(t, repo)

	// The header is scoped to the repo URL: an unscoped http.extraheader is attached to every request the git process makes.
	wantKey := "GIT_CONFIG_KEY_1=http." + repo.URL + ".extraheader"
	wantVal := "GIT_CONFIG_VALUE_1=Authorization: Basic Z2l0OnMzY3JldA==" // base64("git:s3cret")
	var key, val bool
	for _, e := range env {
		if e == wantKey {
			key = true
		}
		if e == wantVal {
			val = true
		}
		if strings.Contains(e, "s3cret") {
			t.Errorf("plaintext credential leaked into env: %s", e)
		}
	}
	if !key || !val {
		t.Errorf("env missing the scoped auth header (key=%v value=%v)", key, val)
	}
}

func TestGitEnvHardening(t *testing.T) {
	env := gitEnv(nil)
	want := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ALLOW_PROTOCOL=https:ssh:file:http:git",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=transfer.credentialsInUrl",
		"GIT_CONFIG_VALUE_0=die",
	}
	have := map[string]bool{}
	for _, e := range env {
		have[e] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("git env missing %q", w)
		}
	}
	// A COUNT that disagrees with the pairs makes git ignore the whole set, so the indices and the count must agree.
	count := -1
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			count, _ = strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT="))
		}
	}
	for _, e := range env {
		for _, kind := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
			if !strings.HasPrefix(e, kind) {
				continue
			}
			i, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(e, kind), "=", 2)[0])
			if err != nil || i >= count {
				t.Errorf("%s has index %q outside GIT_CONFIG_COUNT=%d", e, strings.TrimPrefix(e, kind), count)
			}
		}
	}
	if count != 1 {
		t.Errorf("GIT_CONFIG_COUNT = %d, want 1 for the hardening pair alone", count)
	}
}

// TestGitRefusesExtTransport: GIT_ALLOW_PROTOCOL also refuses ext::, which would run a command.
func TestGitRefusesExtTransport(t *testing.T) {
	err := New(0).git(context.Background(), gitEnv(nil), "ls-remote", "--", "ext::sh -c echo pwned")
	if err == nil {
		t.Fatal("git accepted the ext:: transport")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("want a protocol-allowlist refusal, got %v", err)
	}
}

func TestAuthEnvQuotesSSHPath(t *testing.T) {
	repo := config.Repo{Name: "r", Auth: config.RepoAuth{
		Type:       "ssh",
		SSHKeyPath: "/keys/$(touch /tmp/pwned) id",
	}}
	var cmd string
	for _, e := range mustAuthEnv(t, repo) {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			cmd = e
		}
	}
	if cmd == "" {
		t.Fatal("GIT_SSH_COMMAND not set")
	}
	// Quoted, the shell sees one argument; unquoted it is command substitution and a path split.
	if !strings.Contains(cmd, "-i '/keys/$(touch /tmp/pwned) id'") {
		t.Errorf("GIT_SSH_COMMAND = %q, want the key path shell-quoted", cmd)
	}
	if !strings.Contains(cmd, "IdentitiesOnly=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, want IdentitiesOnly=yes so only the configured key is offered", cmd)
	}
}

func TestAuthEnvSSHStrictWithKnownHosts(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(kh, []byte("github.com ssh-ed25519 AAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := config.Repo{Name: "r", Auth: config.RepoAuth{
		Type: "ssh", SSHKeyPath: "/keys/id", KnownHostsFile: kh,
	}}
	var cmd string
	for _, e := range mustAuthEnv(t, repo) {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			cmd = e
		}
	}
	if !strings.Contains(cmd, "StrictHostKeyChecking=yes") || !strings.Contains(cmd, "UserKnownHostsFile='"+kh+"'") {
		t.Errorf("GIT_SSH_COMMAND = %q, want strict checking against %s", cmd, kh)
	}
	if strings.Contains(cmd, "accept-new") {
		t.Errorf("GIT_SSH_COMMAND = %q, want trust-on-first-use dropped once a known_hosts file exists", cmd)
	}
}

// TestCredentialHeaderStaysWithItsHost is S4: the header must never reach another host, not even on a redirect.
func TestCredentialHeaderStaysWithItsHost(t *testing.T) {
	mu := sync.Mutex{}
	seen := map[string][]string{}
	note := func(where, h string) {
		mu.Lock()
		seen[where] = append(seen[where], h)
		mu.Unlock()
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			note("target", a)
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "not-a-pack")
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			note("origin", a)
		}
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer origin.Close()

	for _, tc := range []struct {
		name         string
		pairs        [][2]string
		wantAtOrigin bool
	}{
		{"scoped header", [][2]string{authHeader(origin.URL+"/repo.git", "git", "s3cret")}, true},
		// Documents what git 2.54 does with an unscoped header of its own accord: it re-authenticates for the redirect target instead of replaying it.
		{"unscoped header (the form this repo used to send)", [][2]string{{"http.extraheader", "Authorization: Basic Z2l0OnMzY3JldA=="}}, true},
	} {
		mu.Lock()
		seen = map[string][]string{}
		mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "ls-remote", "--", origin.URL+"/repo.git")
		cmd.Env = gitEnv(tc.pairs)
		_, _ = cmd.CombinedOutput() // the servers deliberately fail the protocol

		mu.Lock()
		originHdrs, targetHdrs := seen["origin"], seen["target"]
		mu.Unlock()

		if tc.wantAtOrigin && len(originHdrs) == 0 {
			t.Errorf("%s: the configured host got no Authorization header, so the test proves nothing", tc.name)
		}
		if len(targetHdrs) > 0 {
			t.Errorf("%s: credential followed the redirect to another host: %v", tc.name, targetHdrs)
		}
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://u:topsecret@github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"http://tok@127.0.0.1:8080/x", "http://127.0.0.1:8080/x"},
		{"git@github.com:org/repo.git", "github.com:org/repo.git"},
		{"/srv/git/a.git", "/srv/git/a.git"},
		{"https://example.com/a@b.git", "https://example.com/a@b.git"}, // @ in the path, not a login
	} {
		if got := RedactURL(tc.in); got != tc.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuthEnvSSHAcceptNew(t *testing.T) {
	repo := config.Repo{Auth: config.RepoAuth{Type: "ssh", SSHKeyPath: "/keys/id_ed25519"}}
	for _, e := range mustAuthEnv(t, repo) {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			if !strings.Contains(e, "StrictHostKeyChecking=accept-new") {
				t.Errorf("GIT_SSH_COMMAND = %q, want accept-new host key policy", e)
			}
			return
		}
	}
	t.Error("GIT_SSH_COMMAND not set for ssh auth")
}

// TestAuthEnvSSHUnreadableKnownHostsFails pins the fail-closed rule for a broken known_hosts mount.
func TestAuthEnvSSHUnreadableKnownHostsFails(t *testing.T) {
	repo := config.Repo{Name: "r", Auth: config.RepoAuth{
		Type: "ssh", SSHKeyPath: "/keys/id", KnownHostsFile: filepath.Join(t.TempDir(), "missing"),
	}}
	env, err := authEnv(repo)
	if err == nil {
		t.Fatalf("authEnv succeeded with an unreadable known_hosts file: %v", env)
	}
	if !strings.Contains(err.Error(), "known_hosts_file") {
		t.Errorf("err = %v, want it to name auth.known_hosts_file", err)
	}
	if errkind.Of(err) != errkind.HostKey {
		t.Errorf("kind = %q, want host_key", errkind.Of(err))
	}
}

func TestGitKind(t *testing.T) {
	cases := []struct {
		output string
		want   errkind.Kind
	}{
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", errkind.Auth},
		{"remote: HTTP Basic: Access denied\nfatal: Authentication failed for 'https://gitlab.example/x.git/'", errkind.Auth},
		{"git@github.com: Permission denied (publickey).", errkind.Auth},
		{"Host key verification failed.\nfatal: Could not read from remote repository.", errkind.HostKey},
		{"fatal: repository 'https://example.com/x.git/' not found", errkind.NotFound},
		{"fatal: '/tmp/missing' does not appear to be a git repository", errkind.NotFound},
		{"fatal: unable to access 'https://x.invalid/': Could not resolve host: x.invalid", errkind.Unreachable},
		{"fatal: unable to access 'https://h/': The requested URL returned error: 429", errkind.RateLimited},
		{"fatal: unable to access 'https://h/': The requested URL returned error: 502", errkind.ServerError},
		{"fatal: transport 'ext' not allowed", errkind.Unsupported},
		{"fatal: something new", errkind.Other},
	}
	for _, tc := range cases {
		if got := gitKind(context.Background(), tc.output); got != tc.want {
			t.Errorf("gitKind(%q) = %q, want %q", tc.output, got, tc.want)
		}
	}
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	if got := gitKind(expired, "signal: killed"); got != errkind.Timeout {
		t.Errorf("killed by its deadline: gitKind = %q, want timeout", got)
	}
}

// TestSyncErrorsCarryKinds runs real git against real failures: a missing local repository and a host that does not resolve.
func TestSyncErrorsCarryKinds(t *testing.T) {
	s := New(30 * time.Second)
	missing := config.Repo{Name: "m", URL: filepath.Join(t.TempDir(), "missing")}
	if err := s.Sync(context.Background(), missing, filepath.Join(t.TempDir(), "d")); errkind.Of(err) != errkind.NotFound {
		t.Errorf("missing repo: kind = %q (%v), want not_found", errkind.Of(err), err)
	}
	unresolvable := config.Repo{Name: "u", URL: "https://host.invalid/x.git"}
	if err := s.Sync(context.Background(), unresolvable, filepath.Join(t.TempDir(), "d")); errkind.Of(err) != errkind.Unreachable {
		t.Errorf("unresolvable host: kind = %q (%v), want unreachable", errkind.Of(err), err)
	}
}
