package main

import (
	"os/exec"
	"strings"
)

const (
	// defaultForgejoBaseURL mirrors the FORGEJO_BASE_URL schema default.
	defaultForgejoBaseURL = "https://codeberg.org"
	defaultGitUserName    = "spindrift-agent"
)

// configureForgejoCLI wires FORGEJO_TOKEN into fj so the agent's `fj issue`
// and `fj pr` commands (issue #1963) run non-interactively. A no-op when fj
// isn't baked or no token is set: FORGEJO_TOKEN only reaches a Forgejo-backend
// Box and fj is only baked into a Forgejo-backend image. A failed fj aborts
// the Box, as it did under the entrypoint's `set -e`.
func (r *boxRun) configureForgejoCLI() error {
	d := r.d
	token := d.Getenv("FORGEJO_TOKEN")
	if token == "" {
		return nil
	}
	if _, err := d.LookPath("fj"); err != nil {
		return nil
	}
	base := d.Getenv("FORGEJO_BASE_URL")
	if base == "" {
		base = defaultForgejoBaseURL
	}
	// One trailing slash is stripped so the host fj keys the token under is the
	// host the clone's remote is derived from.
	base = strings.TrimSuffix(base, "/")
	name := d.Getenv("GIT_USER_NAME")
	if name == "" {
		name = defaultGitUserName
	}
	// The token rides stdin, never argv. `auth add-key` (NAME positional) is the
	// forgejo-cli 0.5.0 spelling baked into the image; a nixpkgs bump that
	// renames it must update this call in lockstep, or fj would store
	// GIT_USER_NAME as the token.
	cmd := exec.Command("fj", "-H", base, "auth", "add-key", name)
	cmd.Stdin = strings.NewReader(token)
	cmd.Stderr = d.Stderr
	return d.RunCmd(cmd)
}
