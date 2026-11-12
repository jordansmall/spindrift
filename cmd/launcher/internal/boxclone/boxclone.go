// Package boxclone is the Box's repository clone: it picks the clone source
// (the host-seeded mirror mount, else the configured code forge's URL), wires
// git credentials for a forge URL, clones into Config.WorkDir and leaves the
// clone with a repo-local identity and fresh remote refs.
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
	// HostSeededMirror is BOX_HOST_SEEDED_MIRROR: the launcher mounted a
	// host-seeded mirror at RepoMountDir, so the Box clones it with no forge
	// credential (no gh helper, no CloneURL) and trusts the mount and work dir
	// globally (#1720).
	HostSeededMirror bool
	RepoSlug         string
	// Lookup serves the knobs the CODE_FORGE backend's Strategy reads.
	Lookup       backendkit.Lookup
	RepoMountDir string
	WorkDir      string
	GitUserName  string
	GitUserEmail string
}

// CloneURL resolves the remote URL to clone from via the CODE_FORGE backend's
// Strategy. The choice is gated on the exact CODE_FORGE value, so a stray
// CODE_FORGE_REMOTE_URL cannot redirect a github deployment (github's
// Strategy ignores the lookup). An unregistered backend, or one with no clone
// URL, is an error rather than a silent github fallback.
func CloneURL(cfg Config) (string, error) {
	s, ok := backendkit.ByName(cfg.CodeForge)
	if !ok || s.CloneURL == nil {
		return "", fmt.Errorf("CODE_FORGE=%q is not a code forge the Box can clone from", cfg.CodeForge)
	}
	return s.CloneURL(backendkit.Shared{RepoSlug: cfg.RepoSlug}, cfg.Lookup)
}

// Clone clones the repo into cfg.WorkDir, sets the repo-local git identity and
// fetches origin so the pre-work rebase sees current origin/BASE_BRANCH, not
// the state captured at clone time. Narration goes to stdout; git and gh keep
// their own stdout and stderr streams.
func Clone(cfg Config, stdout, stderr io.Writer) error {
	var url string
	if cfg.HostSeededMirror {
		url = cfg.RepoMountDir
		// Under rootless podman the Box's mapped uid never matches the
		// host-owned bind mount's uid, so git's dubious-ownership guard
		// rejects RepoMountDir before the clone copies a single object
		// (#1720). Both paths outlive the clone step, so these are standing
		// global entries: the one host-seeded-mirror exception to the
		// repo-local-only config rule below.
		for _, dir := range []string{cfg.RepoMountDir, cfg.WorkDir} {
			if err := run(stdout, stderr, "", "git", "config", "--global", "--add", "safe.directory", dir); err != nil {
				return err
			}
		}
	} else {
		if cfg.GHCredentialHelper {
			if err := run(stdout, stderr, "", "gh", "auth", "setup-git"); err != nil {
				return err
			}
		}
		var err error
		if url, err = CloneURL(cfg); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "==> cloning %s\n", forge.RedactURLCredentials(url))
	if err := run(stdout, stderr, "", "git", "clone", url, cfg.WorkDir); err != nil {
		return err
	}
	if cfg.HostSeededMirror {
		if err := addForgeRemote(cfg, stdout, stderr); err != nil {
			return err
		}
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

// addForgeRemote gives a mirror clone a credential-free "forge" remote: gh and
// fj resolve the repo from the clone's remotes, and origin is the mount path.
// skipFetchAll keeps `git fetch --all` off the forge, which the Box holds no
// credential for. remote.pushDefault is repo-wide: every branch without its own
// pushRemote pushes to forge, which gh uses to find a PR and branch recovery
// would otherwise leave on origin. A push naming no remote then goes to forge;
// when the Box is host-mediated or outbox-relay capable, the read-only guard
// points forge's pushurl at the decoy. A forge with no web URL (local, git) gets no
// remote.
func addForgeRemote(cfg Config, stdout, stderr io.Writer) error {
	s, ok := backendkit.ByName(cfg.CodeForge)
	if !ok || s.WebURL == nil {
		return nil
	}
	url := s.WebURL(backendkit.Shared{RepoSlug: cfg.RepoSlug}, cfg.Lookup)
	if url == "" {
		return nil
	}
	for _, args := range [][]string{
		{"remote", "add", "forge", url},
		{"config", "remote.forge.skipFetchAll", "true"},
		{"config", "remote.pushDefault", "forge"},
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
