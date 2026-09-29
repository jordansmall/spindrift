package butler

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
)

// Tree is the checkout a Butler scans -- a seam over a local repo's
// Head/TrackedFiles so Sweep is testable without a real repo.
type Tree interface {
	Head(branch string) (string, error)
	TrackedFiles(commit string) ([]string, error)
	// CommitPatch fetches branch's current tip, checks that diff applies
	// cleanly to it, and commits it on top as message under the launcher
	// identity, returning a PatchCommit naming the repo dir and the local
	// ref holding that commit -- exactly what
	// forge.BranchPusher.PushBranch(srcDir, localRef, branch) takes (issue
	// #4074).
	CommitPatch(branch, diff, message string) (PatchCommit, error)
}

// PatchCommit is CommitPatch's result: srcDir and localRef, ready to hand
// straight to forge.BranchPusher.PushBranch(srcDir, localRef, branch).
type PatchCommit struct {
	Dir string
	Ref string
}

// patchRef is the single namespaced ref CommitPatch commits under. One ref,
// overwritten per call: a Tree stages at most one proposed patch at a time,
// so there is nothing to disambiguate between (issue #4074).
const patchRef = "refs/butler/patch"

// GitTree is Tree's production implementation over an existing local repo
// (bare or not): Repo is the filesystem path git shells out against. The
// branch comes from Sweep's own r.policy.Branch, not a field here, so it's
// never passed twice (issue #3990). URL and GitArgs, when set (FetchTree
// sets both on construction), let CommitPatch re-fetch branch's tip fresh
// rather than trust however stale Repo's local ref is; a GitTree built
// directly over a plain local repo (as in tests) leaves URL empty and
// CommitPatch reads the local ref as-is. Name/Email are the launcher
// identity CommitPatch commits under -- production wiring sets them, not
// FetchTree, since a scan-only Tree never needs one.
type GitTree struct {
	Repo    string
	URL     string
	GitArgs []string
	Name    string
	Email   string
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
// (forge.RedactURLCredentials). The returned GitTree carries url and
// gitArgs, so a later CommitPatch on it re-fetches rather than trusting this
// construction-time fetch to still be fresh.
func FetchTree(scratch, url, branch string, gitArgs ...string) (GitTree, error) {
	if err := fetchBranchTip(gitArgs, scratch, url, branch); err != nil {
		return GitTree{}, err
	}
	return GitTree{Repo: scratch, URL: url, GitArgs: gitArgs}, nil
}

// fetchBranchTip shallow-fetches branch's current tip from url into repo,
// force-updating repo's local refs/heads/<branch> to match -- the one fetch
// invocation FetchTree and CommitPatch's re-fetch share, so "the current
// base head" always means "just fetched", never "however stale scratch was
// at construction" (issue #4074).
func fetchBranchTip(gitArgs []string, repo, url, branch string) error {
	ref := "refs/heads/" + branch
	cmd := gitexec.Cmd(gitArgs, "-C", repo, "fetch", "--depth=1", "-q", url, "+"+ref+":"+ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("butler: fetch branch %s from %s: %w: %s", branch, forge.RedactURLCredentials(url), err, forge.RedactURLCredentials(string(out)))
	}
	return nil
}

// CommitPatch implements Tree. See the Tree interface doc for the contract;
// the mechanics: re-fetch branch's tip when g.URL is set (see fetchBranchTip
// above), stage that tip into a throwaway index file (GIT_INDEX_FILE --
// Repo is bare, so there is no working tree to stage into), apply diff
// against it -- a stale/rebased diff fails here with gitexec.OutputErr's own
// wrap around git's own error, not a separate check pass -- write the
// resulting tree, commit it under the configured identity, and point
// patchRef at the new commit.
func (g GitTree) CommitPatch(branch, diff, message string) (PatchCommit, error) {
	if g.Name == "" || g.Email == "" {
		return PatchCommit{}, fmt.Errorf("butler: CommitPatch: no launcher identity configured for %s", g.Repo)
	}

	if g.URL != "" {
		if err := fetchBranchTip(g.GitArgs, g.Repo, g.URL, branch); err != nil {
			return PatchCommit{}, err
		}
	}
	tip, err := g.Head(branch)
	if err != nil {
		return PatchCommit{}, err
	}

	// A private index file, not Repo's real index (Repo is bare and has
	// none): read-tree/apply/write-tree stage entirely off to the side, so
	// nothing here can collide with another Tree operation on the same repo.
	indexDir, err := os.MkdirTemp("", "butler-patch-index")
	if err != nil {
		return PatchCommit{}, fmt.Errorf("butler: CommitPatch: %w", err)
	}
	defer os.RemoveAll(indexDir)
	indexEnv := "GIT_INDEX_FILE=" + filepath.Join(indexDir, "index")

	run := func(extraEnv []string, stdin string, args ...string) (string, error) {
		full := append([]string{"-C", g.Repo}, args...)
		cmd := gitexec.Cmd(g.GitArgs, full...)
		cmd.Env = append(cmd.Env, extraEnv...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}

	if _, err := run([]string{indexEnv}, "", "read-tree", tip); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "read-tree %s", tip)
	}
	if _, err := run([]string{indexEnv}, diff, "apply", "--cached", "-"); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "diff does not apply to %s", tip)
	}
	newTree, err := run([]string{indexEnv}, "", "write-tree")
	if err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "write-tree")
	}

	identEnv := []string{
		"GIT_AUTHOR_NAME=" + g.Name, "GIT_AUTHOR_EMAIL=" + g.Email,
		"GIT_COMMITTER_NAME=" + g.Name, "GIT_COMMITTER_EMAIL=" + g.Email,
	}
	commit, err := run(identEnv, "", "commit-tree", newTree, "-p", tip, "-m", message)
	if err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "commit-tree")
	}

	if _, err := run(nil, "", "update-ref", patchRef, commit); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "update-ref %s", patchRef)
	}

	return PatchCommit{Dir: g.Repo, Ref: patchRef}, nil
}
