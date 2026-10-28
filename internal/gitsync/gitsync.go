// Package gitsync clones and fast-forwards GitOps repositories with their per-repo credentials.
package gitsync

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/config"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
)

// DefaultCommandTimeout bounds one git subprocess (clone, fetch, set-url).
const DefaultCommandTimeout = 5 * time.Minute

// Syncer brings a local checkout of a repository up to date.
type Syncer struct {
	commandTimeout time.Duration // 0 = no per-command limit
}

// New returns a Syncer whose git subprocesses each get commandTimeout (0 disables the per-command limit; the caller's context still applies).
func New(commandTimeout time.Duration) *Syncer {
	return &Syncer{commandTimeout: commandTimeout}
}

// Sync clones repo into dest, or fetches and resets an existing clone there.
func (s *Syncer) Sync(ctx context.Context, repo config.Repo, dest string) error {
	env, err := authEnv(repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		return s.fetch(ctx, repo, dest, env)
	}
	return s.clone(ctx, repo, dest, env)
}

func (s *Syncer) clone(ctx context.Context, repo config.Repo, dest string, env []string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// "--" ends option parsing: without it a URL beginning with "-" is read by git as an option ("-c protocol.ext.allow=always") rather than a URL.
	return s.git(ctx, env, "clone", "--depth=1", "--single-branch", "--", repo.URL, dest)
}

func (s *Syncer) fetch(ctx context.Context, repo config.Repo, dest string, env []string) error {
	// Reset the remote URL in case a cached clone predates the current config (older versions embedded credentials in the URL; tokens also rotate).
	if err := s.git(ctx, gitEnv(nil), "-C", dest, "remote", "set-url", "origin", "--", repo.URL); err != nil {
		return err
	}
	if err := s.git(ctx, env, "-C", dest, "fetch", "--depth=1", "origin"); err != nil {
		return err
	}
	return s.git(ctx, gitEnv(nil), "-C", dest, "reset", "--hard", "FETCH_HEAD")
}

// git runs one git command with env as its whole environment.
func (s *Syncer) git(ctx context.Context, env []string, args ...string) error {
	if s.commandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.commandTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		sub := args[0]
		if sub == "-C" {
			sub = args[2]
		}
		output := strings.TrimSpace(string(out))
		return errkind.Wrap(gitKind(ctx, output), fmt.Errorf("git %s: %w\n%s", sub, err, output))
	}
	return nil
}

// gitOutputKinds maps what git prints to a failure kind, most specific first.
var gitOutputKinds = []struct {
	kind     errkind.Kind
	patterns []string
}{
	{errkind.HostKey, []string{"Host key verification failed", "REMOTE HOST IDENTIFICATION HAS CHANGED", "host key is known for", "Host key for"}},
	// A private GitHub repo without credentials reads as a prompt git may not show, not as a 404: GitHub does not confirm that a private repo exists.
	{errkind.Auth, []string{"Authentication failed", "could not read Username", "could not read Password", "terminal prompts disabled",
		"Permission denied (publickey", "Access denied", "returned error: 401", "returned error: 403", "Invalid username or password"}},
	{errkind.RateLimited, []string{"returned error: 429"}},
	{errkind.ServerError, []string{"returned error: 50"}},
	{errkind.NotFound, []string{"returned error: 404", "not found", "does not appear to be a git repository", "does not exist"}},
	{errkind.Timeout, []string{"Connection timed out", "Operation timed out"}},
	{errkind.Unreachable, []string{"Could not resolve host", "Could not resolve hostname", "Connection refused", "Failed to connect",
		"Network is unreachable", "No route to host", "SSL certificate problem", "Connection reset"}},
	{errkind.Unsupported, []string{"not allowed", "uses plaintext credentials"}},
}

// gitKind classifies a failed git command: an expired deadline first (the process was killed, its output says nothing), then git's own output.
func gitKind(ctx context.Context, output string) errkind.Kind {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errkind.Timeout
	}
	for _, k := range gitOutputKinds {
		for _, p := range k.patterns {
			if strings.Contains(output, p) {
				return k.kind
			}
		}
	}
	return errkind.Other
}

// authEnv builds the git environment for one repo.
func authEnv(repo config.Repo) ([]string, error) {
	var pairs [][2]string
	var extra []string
	auth := repo.Auth
	switch auth.Type {
	case "ssh":
		if auth.SSHKeyPath != "" {
			cmd, err := sshCommand(repo)
			if err != nil {
				return nil, err
			}
			extra = append(extra, "GIT_SSH_COMMAND="+cmd)
		}
	case "token":
		if tok := secret(auth.Token, auth.TokenFile); tok != "" {
			pairs = append(pairs, authHeader(repo.URL, "git", tok))
		}
	case "basic":
		if pass := secret(auth.Password, auth.PasswordFile); pass != "" {
			pairs = append(pairs, authHeader(repo.URL, auth.Username, pass))
		}
	}
	return append(gitEnv(pairs), extra...), nil
}

// sshCommand builds what git runs for the ssh transport.
func sshCommand(repo config.Repo) (string, error) {
	auth := repo.Auth
	cmd := "ssh -i " + shellQuote(auth.SSHKeyPath) + " -o IdentitiesOnly=yes -o BatchMode=yes"
	if auth.KnownHostsFile != "" {
		if _, err := os.Stat(auth.KnownHostsFile); err != nil {
			return "", errkind.Wrap(errkind.HostKey, fmt.Errorf("auth.known_hosts_file is set but cannot be read, refusing to clone without host key verification: %w", err))
		}
		return cmd + " -o UserKnownHostsFile=" + shellQuote(auth.KnownHostsFile) +
			" -o StrictHostKeyChecking=yes", nil
	}
	// accept-new really means "believe the first key this host shows me": a scan pod remembers nothing between runs.
	slog.Warn("ssh host key cannot be verified: this repo has no known_hosts file",
		"repo", repo.Name,
		"hint", "set auth.known_hosts_file (the Helm chart mounts one from scanner.ssh.knownHosts)")
	return cmd + " -o StrictHostKeyChecking=accept-new", nil
}

// shellQuote quotes one argument for the shell running GIT_SSH_COMMAND.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gitEnv is the environment of every git subprocess: the hardening baseline plus extra config pairs.
func gitEnv(pairs [][2]string) []string {
	hard := []string{
		"GIT_TERMINAL_PROMPT=0",
		// Ignore /etc/gitconfig: it belongs to the image rather than to this config, and it is one way for a layer to change git's behaviour behind this process's back.
		"GIT_CONFIG_NOSYSTEM=1",
		// NOSYSTEM covers only /etc/gitconfig; /dev/null here also excludes ~/.gitconfig and $XDG_CONFIG_HOME/git/config.
		"GIT_CONFIG_GLOBAL=/dev/null",
		// Exactly the transports config.Load accepts.
		"GIT_ALLOW_PROTOCOL=https:ssh:file:http:git",
	}
	all := append([][2]string{{"transfer.credentialsInUrl", "die"}}, pairs...)
	return append(os.Environ(), append(hard, gitConfig(all)...)...)
}

// gitConfig encodes config pairs the way git reads them from the environment: GIT_CONFIG_COUNT plus indexed KEY/VALUE pairs.
func gitConfig(pairs [][2]string) []string {
	out := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(pairs))}
	for i, kv := range pairs {
		out = append(out,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return out
}

// authHeader returns one git config pair: http.extraheader scoped to the repo's URL.
func authHeader(url, user, pass string) [2]string {
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return [2]string{"http." + url + ".extraheader", "Authorization: Basic " + cred}
}

// secret returns the inline credential, or else the contents of its file.
func secret(inline, file string) string {
	if inline != "" || file == "" {
		return inline
	}
	return readCredFile(file)
}

func readCredFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		// Do not log err or path: CodeQL (go/clear-text-logging) treats values flowing from credential-file configuration as sensitive.
		slog.Warn("credential file unreadable")
		return ""
	}
	return strings.TrimSpace(string(data))
}

// RedactURL drops a user[:password]@ from a URL before it is logged.
func RedactURL(u string) string {
	scheme, rest := "", u
	if i := strings.Index(u, "://"); i >= 0 {
		scheme, rest = u[:i+3], u[i+3:]
	}
	authority, tail := rest, ""
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		authority, tail = rest[:j], rest[j:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	return scheme + authority + tail
}
