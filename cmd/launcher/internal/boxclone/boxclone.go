// Package boxclone is the Box's repository clone: it picks the clone URL for
// the configured code forge, wires git credentials, clones into Config.WorkDir
// and leaves the clone with a repo-local identity and fresh remote refs.
package boxclone

import (
	"errors"
	"fmt"
	"io"
	"os/exec"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Config is what the clone reads from the Box's environment.
type Config struct {
	// CodeForge is CODE_FORGE, already defaulted to github by the caller; an
	// empty or unregistered value fails the clone closed (see CloneURL).
	CodeForge string
	// GHCredentialHelper runs `gh auth setup-git` before cloning. See
	// backend.Descriptor.InBoxGHCredentialHelper for which forges want it.
	GHCredentialHelper bool
	// HostMediatedRemote marks the remote as a host-owned filesystem mount,
	// so the clone trusts the mount and work dir globally (#1720).
	HostMediatedRemote bool
	RepoSlug           string
	// Lookup serves the knobs the CODE_FORGE backend's Strategy reads.
	Lookup       backendkit.Lookup
	RepoMountDir string
	WorkDir      string
	GitUserName  string
	GitUserEmail string
}

// CloneURL resolves the URL to clone from via the CODE_FORGE backend's
// Strategy. The choice is gated on the exact CODE_FORGE value, so a stray
// CODE_FORGE_REMOTE_URL cannot redirect a github deployment (github's
// Strategy ignores the lookup). An unregistered backend, or one with no clone
// URL, is an error rather than a silent github fallback.
func CloneURL(cfg Config) (string, error) {
	s, ok := backendkit.ByName(cfg.CodeForge)
	if !ok || s.CloneURL == nil {
		return "", fmt.Errorf("CODE_FORGE=%q is not a code forge the Box can clone from", cfg.CodeForge)
	}
	return s.CloneURL(backendkit.Shared{RepoSlug: cfg.RepoSlug}, cfg.Lookup, cfg.RepoMountDir)
}

// Clone clones the repo into cfg.WorkDir, sets the repo-local git identity and
// fetches origin so the pre-work rebase sees current origin/BASE_BRANCH, not
// the state captured at clone time. Narration goes to stdout; git and gh keep
// their own stdout and stderr streams.
func Clone(cfg Config, stdout, stderr io.Writer) error {
	if cfg.GHCredentialHelper {
		if err := run(stdout, stderr, "", "gh", "auth", "setup-git"); err != nil {
			return err
		}
	}
	url, err := CloneURL(cfg)
	if err != nil {
		return err
	}
	if cfg.HostMediatedRemote {
		// Under rootless podman the Box's mapped uid never matches the
		// host-owned bind mount's uid, so git's dubious-ownership guard
		// rejects RepoMountDir before the clone copies a single object
		// (#1720). Both paths outlive the clone step, so these are standing
		// global entries: the one host-mediated exception to the
		// repo-local-only config rule below.
		for _, dir := range []string{cfg.RepoMountDir, cfg.WorkDir} {
			if err := run(stdout, stderr, "", "git", "config", "--global", "--add", "safe.directory", dir); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(stdout, "==> cloning %s\n", forge.RedactURLCredentials(url))
	if err := run(stdout, stderr, "", "git", "clone", url, cfg.WorkDir); err != nil {
		return err
	}
	// Identity is repo-local, not global (#404): CI's hermetic check
	// environment has no global git config, so a global identity here would
	// let git-shelling tests observe config the Box has but CI doesn't.
	for _, args := range [][]string{
		{"config", "user.name", cfg.GitUserName},
		{"config", "user.email", cfg.GitUserEmail},
		{"fetch", "origin"},
	} {
		if err := run(stdout, stderr, cfg.WorkDir, "git", args...); err != nil {
			return err
		}
	}
	return nil
}

func run(stdout, stderr io.Writer, dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		// Args can carry the token-bearing clone URL, so redact them before
		// the error leaves the package. git's own output goes to the writers as is.
		return errors.New(forge.RedactURLCredentials(fmt.Sprintf("%s %v: %v", name, args, err)))
	}
	return nil
}
