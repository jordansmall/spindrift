// Package boxclone is the Box's repository clone: it picks the clone URL for
// the configured code forge, wires git credentials, clones into Config.WorkDir
// and leaves the clone with a repo-local identity and fresh remote refs.
package boxclone

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Config is what the clone reads from the Box's environment.
type Config struct {
	// CodeForge is CODE_FORGE, defaulted to github by the caller; "" is
	// treated as github too.
	CodeForge string
	// GHCredentialHelper runs `gh auth setup-git` before cloning. See
	// backend.Descriptor.InBoxGHCredentialHelper for which forges want it.
	GHCredentialHelper bool
	// HostMediatedRemote marks the remote as a host-owned filesystem mount,
	// so the clone trusts the mount and work dir globally (#1720).
	HostMediatedRemote bool
	RepoSlug           string
	RemoteURL          string // CODE_FORGE_REMOTE_URL
	ForgejoBaseURL     string // FORGEJO_BASE_URL; default https://codeberg.org
	ForgejoToken       string
	RepoMountDir       string
	WorkDir            string
	GitUserName        string
	GitUserEmail       string
}

// CloneURL resolves the URL to clone from. The choice is gated on the exact
// CODE_FORGE value so a stray CODE_FORGE_REMOTE_URL cannot redirect a default
// github deployment.
func CloneURL(cfg Config) (string, error) {
	switch cfg.CodeForge {
	case "git":
		if cfg.RemoteURL == "" {
			return "", errors.New("CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git")
		}
		return cfg.RemoteURL, nil
	case "forgejo":
		if cfg.ForgejoToken == "" {
			return "", errors.New("FORGEJO_TOKEN is required when CODE_FORGE=forgejo")
		}
		base := cfg.ForgejoBaseURL
		if base == "" {
			base = "https://codeberg.org"
		}
		base = strings.TrimSuffix(base, "/")
		scheme, rest, ok := strings.Cut(base, "://")
		if !ok {
			// Mirrors the shell's ${base%%://*} / ${base#*://}, which yield
			// the whole string for both when there is no scheme.
			scheme, rest = base, base
		}
		return fmt.Sprintf("%s://%s@%s/%s.git", scheme, cfg.ForgejoToken, rest, cfg.RepoSlug), nil
	case "local":
		return cfg.RepoMountDir, nil
	default:
		return "https://github.com/" + cfg.RepoSlug + ".git", nil
	}
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
