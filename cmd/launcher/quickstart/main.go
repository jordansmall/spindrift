package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/term"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/driver/bundle" // the bundled Drivers; its init registers the backends and runner families
	"spindrift.dev/launcher/internal/driver/driverrows"
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

	baked, err := decodeBakedGithubRow(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}

	connectors := connectorruntime.New(baked, hostExportedCredentials(baked), nil)
	// Quickstart reads no input document, so it offers the bundled Drivers and
	// their generated Descriptors alone.
	rows, err := driverrows.Parse(nil, bundle.Drivers())
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}
	connectors.UseDriverRows(rows)
	interactive := term.IsTerminal(os.Stdin.Fd())

	err = runQuickstart(connectors, dir, hostEnvironment{}, hostCommandRunner{}, buildForge, os.Stdout, os.Stdin, interactive, *force)
	_ = connectors.Close() // os.Exit skips defers
	if err != nil {
		fmt.Fprintf(os.Stderr, "quickstart: %s\n", err)
		os.Exit(1)
	}
}

// hostExportedCredentials is the credential of each row in rows, read from the
// process environment at each use: the wizard exports every token it collects
// (the gh CLI reads it there too) after the runtime is built.
func hostExportedCredentials(rows backendrows.Set) map[string]credsource.Row {
	creds := map[string]credsource.Row{}
	for _, row := range rows.All() {
		creds[row.Name] = credsource.EnvRow(row.CredentialEnv, row.IdentityEnv)
	}
	return creds
}
