package butler

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
)

// Tree is the checkout a Butler scans -- a seam over a local repo's
// Head/TrackedFiles so Sweep is testable without a real repo.
type Tree interface {
	Head(branch string) (string, error)
	TrackedFiles(commit string) ([]string, error)
}

// GitTree is Tree's production implementation over an existing local repo
// (bare or not): Repo is the filesystem path git shells out against. The
// branch comes from Sweep's own r.policy.Branch, not a field here, so it's
// never passed twice (issue #3990).
type GitTree struct {
	Repo string
}

// Head resolves branch's tip in g.Repo to a full commit sha.
func (g GitTree) Head(branch string) (string, error) {
	out, err := exec.Command("git", "-C", g.Repo, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return "", gitexec.OutputErr("butler", err, "resolve refs/heads/%s", branch)
	}
	return strings.TrimSpace(string(out)), nil
}

// TrackedFiles returns every path git tracks in commit's tree, in whatever
// order `git ls-tree` reports them (NextScope sorts its own copy, so callers
// needn't).
func (g GitTree) TrackedFiles(commit string) ([]string, error) {
	out, err := exec.Command("git", "-C", g.Repo, "ls-tree", "-r", "--name-only", "-z", commit).Output()
	if err != nil {
		return nil, gitexec.OutputErr("butler", err, "ls-tree %s", commit)
	}
	trimmed := bytes.Trim(out, "\x00")
	if len(trimmed) == 0 {
		return nil, nil
	}
	parts := bytes.Split(trimmed, []byte{0})
	files := make([]string, len(parts))
	for i, p := range parts {
		files[i] = string(p)
	}
	return files, nil
}

// FetchTree shallow-fetches branch's current tip from url into the
// already-initialized bare repo at scratch (e.g. via ledger.NewRemote), once
// at construction, and returns a Tree over it -- the scan checkout a hosted
// Ledger's backend row shares with its Ledger backend (issue #3995). gitArgs
// are the same leading git options a Ledger backend takes (e.g. credential
// helpers); GIT_TERMINAL_PROMPT=0 keeps a missing credential from hanging,
// and url's credentials never reach an error string
// (forge.RedactURLCredentials).
func FetchTree(scratch, url, branch string, gitArgs ...string) (GitTree, error) {
	ref := "refs/heads/" + branch
	cmd := gitexec.Cmd(gitArgs, "-C", scratch, "fetch", "--depth=1", "-q", url, "+"+ref+":"+ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return GitTree{}, fmt.Errorf("butler: fetch branch %s from %s: %w: %s", branch, forge.RedactURLCredentials(url), err, forge.RedactURLCredentials(string(out)))
	}
	return GitTree{Repo: scratch}, nil
}
