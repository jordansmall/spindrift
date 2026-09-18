package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/term"
)

// forceFlagUsage deliberately names no fixed "*.bak" backup file: the scheme
// uses numbered suffixes like .bak.000001 so each backup is unique (ADR 0027).
const forceFlagUsage = "overwrite an existing flake.nix/harness.env, backing each up first"

// hostEnvironment is the real Environment: host PATH, ambient env vars, host
// git config, and the git remote repoSlug guess (ADR 0027).
type hostEnvironment struct{}

func (hostEnvironment) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (hostEnvironment) LookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

func (hostEnvironment) Getenv(key string) string {
	return os.Getenv(key)
}

// TokenScopes reads the X-OAuth-Scopes header from `gh api -i`, since no forge
// method exposes it (ADR 0027). GH_TOKEN carries the token under audit so the
// probe checks the pasted token, not the host gh CLI's own credential.
func (hostEnvironment) TokenScopes(token string) ([]string, error) {
	cmd := exec.Command("gh", "api", "-i", "user")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api -i user: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "x-oauth-scopes") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, nil
		}
		var scopes []string
		for _, s := range strings.Split(value, ",") {
			scopes = append(scopes, strings.TrimSpace(s))
		}
		return scopes, nil
	}
	return nil, nil
}

// GHAuthToken returns the operator's own `gh auth token`, the fallback when
// they decline to paste a fine-grained PAT.
func (hostEnvironment) GHAuthToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GitConfig reads a host git config key, returning "" if unset or git is
// unavailable. The caller reads "" as no default offered.
func (hostEnvironment) GitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GitRemoteRepoSlug guesses "owner/repo" from the "origin" remote, returning ""
// when there is no remote or it is not a github.com URL.
func (hostEnvironment) GitRemoteRepoSlug() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return parseGitHubRepoSlug(strings.TrimSpace(string(out)))
}

// parseGitHubRepoSlug extracts "owner/repo" from a github.com remote URL in
// scp-like ssh, ssh://, or https form, returning "" for anything else.
func parseGitHubRepoSlug(remoteURL string) string {
	s := strings.TrimSuffix(remoteURL, ".git")
	const marker = "github.com"
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	// Only "@" (scp-like or ssh://) or "/" (https://) may precede the match.
	// Anything else is a different host, such as "notgithub.com" or the SSH
	// alias "github.com-work".
	if i > 0 && s[i-1] != '@' && s[i-1] != '/' {
		return ""
	}
	rest := s[i+len(marker):]
	if rest == "" || (rest[0] != ':' && rest[0] != '/') {
		return ""
	}
	rest = strings.TrimSuffix(rest[1:], "/")
	if rest == "" || strings.Count(rest, "/") != 1 {
		return ""
	}
	return rest
}

// GitRemoteURL returns the raw "origin" remote URL, or "" if there is none.
func (hostEnvironment) GitRemoteURL() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// InsideGitWorkTree reports whether dir sits inside a git work tree, so the
// finish line can tell the operator to `git add` the scaffold files: an
// untracked flake.nix silently breaks `nix develop` and direnv (issue #2567).
func (hostEnvironment) InsideGitWorkTree(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// hostCommandRunner is the real CommandRunner: it runs the named command on
// the process's own stdio, so `claude setup-token` can prompt the operator
// (ADR 0027).
type hostCommandRunner struct{}

func (hostCommandRunner) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	force := flag.Bool("force", false, forceFlagUsage)
	flag.Parse()

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}

	interactive := term.IsTerminal(os.Stdin.Fd())

	if err := runQuickstart(dir, hostEnvironment{}, hostCommandRunner{}, buildForge, os.Stdout, os.Stdin, interactive, *force); err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}
}
