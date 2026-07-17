package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// hostEnvironment is the real Environment: host PATH lookups and ambient env
// var reads. LookPath is unused by runQuickstart until host detection lands
// (ADR 0027); wired here so the seam is ready.
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

// TokenScopes reads the X-OAuth-Scopes response header `gh api -i` returns
// for a classic/OAuth token, since no forge method exposes it (ADR 0027).
// The token under audit is passed via GH_TOKEN so the probe checks the
// pasted token, not whatever credential the host's gh CLI is already
// authenticated with.
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

// GHAuthToken shells out to `gh auth token` for the operator's own
// authenticated token — the fallback path when they decline to paste a
// fine-grained PAT.
func (hostEnvironment) GHAuthToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// hostCommandRunner is the real CommandRunner: runs the named command with
// the process's own stdio. Used for the `claude setup-token` finish-line
// step; `spindrift build` wiring is still unbuilt (ADR 0027).
type hostCommandRunner struct{}

func (hostCommandRunner) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	force := flag.Bool("force", false, "overwrite an existing flake.nix/harness.env, backing each up to *.bak first")
	flag.Parse()

	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}

	stat, statErr := os.Stdin.Stat()
	interactive := statErr == nil && (stat.Mode()&os.ModeCharDevice) != 0

	if err := runQuickstart(dir, hostEnvironment{}, hostCommandRunner{}, os.Stdout, os.Stdin, interactive, *force); err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}
}
