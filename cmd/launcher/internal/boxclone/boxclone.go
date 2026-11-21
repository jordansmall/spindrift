// Package boxclone is the Box's repository clone: it clones the host-seeded
// mirror mount into Config.WorkDir and leaves the clone with a repo-local
// identity and fresh remote refs. The Box holds no forge credential, so the
// mirror is its only clone source.
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
	// CodeForge is CODE_FORGE, already defaulted to github by the caller; it
	// picks the credential-free "forge" remote the mirror clone gains.
	CodeForge string
	RepoSlug  string
	// Lookup serves the knobs the CODE_FORGE backend's Strategy reads.
	Lookup       backendkit.Lookup
	RepoMountDir string
	WorkDir      string
	GitUserName  string
	GitUserEmail string
}

// Clone clones the host-seeded mirror at cfg.RepoMountDir into cfg.WorkDir,
// sets the repo-local git identity and fetches origin so the pre-work rebase
// sees current origin/BASE_BRANCH, not the state captured at clone time.
// Narration goes to stdout; git keeps its own stdout and stderr streams.
func Clone(cfg Config, stdout, stderr io.Writer) error {
	// Under rootless podman the Box's mapped uid never matches the bind
	// mount's, so git's dubious-ownership guard rejects RepoMountDir (#1720).
	// Both paths outlive the clone, so these are standing global entries: the
	// one exception to the repo-local identity rule below.
	for _, dir := range []string{cfg.RepoMountDir, cfg.WorkDir} {
		if err := run(stdout, stderr, "", "git", "config", "--global", "--add", "safe.directory", dir); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "==> cloning %s\n", cfg.RepoMountDir)
	if err := run(stdout, stderr, "", "git", "clone", cfg.RepoMountDir, cfg.WorkDir); err != nil {
		return err
	}
	if err := addForgeRemote(cfg, stdout, stderr); err != nil {
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

// addForgeRemote gives the mirror clone a credential-free "forge" remote naming
// the repo's web URL, since origin is the mount path. skipFetchAll keeps `git
// fetch --all` off the uncredentialed forge. remote.pushDefault is repo-wide
// and outranks the origin upstream that branch recovery sets, so any push
// naming no remote goes to forge, whose pushurl the read-only guard points at
// the decoy. A forge with no web URL (local, git) gets no remote.
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
		// Args can carry a URL with userinfo, so redact before the error leaves.
		return errors.New(forge.RedactURLCredentials(fmt.Sprintf("%s %v: %v", name, args, err)))
	}
	return nil
}
