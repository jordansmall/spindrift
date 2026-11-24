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
	"spindrift.dev/launcher/internal/signalwire"
)

// ScannedCommit is the commit a finding was scanned against -- its own type
// so CommitPatch's branch and scanned arguments cannot be swapped silently.
type ScannedCommit string

// Tree is the checkout a Butler scans -- a seam over a local repo's
// Head/TrackedFiles so Sweep is testable without a real repo.
type Tree interface {
	Head(branch string) (string, error)
	TrackedFiles(commit string) ([]string, error)
	// CommitPatch first refuses diff unless git's own reading of it matches
	// signalwire.ParseUnifiedDiff(diff), so the parser the gates decided on
	// is never the only thing between an untrusted diff and a commit. It then
	// checks that diff applies cleanly to scanned and to branch's current
	// tip, in that order, and commits it on top of the tip under the launcher
	// identity (issue #4074).
	CommitPatch(branch string, scanned ScannedCommit, diff, message string) (PatchCommit, error)
}

// PatchCommit is CommitPatch's result, ready to hand to
// forge.BranchPusher.PushBranch(Dir, Ref, branch, base).
type PatchCommit struct {
	Dir string
	Ref string
}

// patchRef is overwritten per call: a Tree stages at most one proposed patch
// at a time (issue #4074).
const patchRef = "refs/butler/patch"

// GitTree is Tree's production implementation over an existing local repo
// (bare or not). It has no branch field: the branch comes from Sweep's
// r.policy.Branch, so it is never passed twice (issue #3990). URL, when set
// (FetchTree sets it), lets CommitPatch re-fetch branch's tip fresh rather
// than trust however stale Repo's local ref is; an empty URL reads the local
// ref as-is. Name/Email are the launcher identity
// CommitPatch commits under; production wiring sets them, not FetchTree,
// since a scan-only Tree never needs one.
type GitTree struct {
	Repo string
	URL  string
	// Credential authenticates the re-fetch from URL; URL itself carries none.
	Credential gitexec.Credential
	GitArgs    []string
	Name       string
	Email      string
}

// Head resolves branch's tip in g.Repo to a full commit sha.
func (g GitTree) Head(branch string) (string, error) {
	out, err := exec.Command("git", "-C", g.Repo, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return "", gitexec.OutputErr("butler", err, "resolve refs/heads/%s", branch)
	}
	return strings.TrimSpace(string(out)), nil
}

// TrackedFiles returns every path git tracks in commit's tree, unsorted.
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
// already-initialized bare repo at scratch, once at construction, and returns
// a Tree over it -- the scan checkout a hosted Ledger shares with its Ledger
// backend (issue #3995). cred (zero for none) authenticates every fetch; url's
// credentials never reach an error string. The returned GitTree carries url,
// cred and gitArgs, so a later CommitPatch re-fetches.
func FetchTree(scratch, url, branch string, cred gitexec.Credential, gitArgs ...string) (GitTree, error) {
	if err := fetchBranchTip(cred, gitArgs, scratch, url, branch); err != nil {
		return GitTree{}, err
	}
	return GitTree{Repo: scratch, URL: url, Credential: cred, GitArgs: gitArgs}, nil
}

// fetchBranchTip shallow-fetches branch's current tip from url into repo,
// force-updating repo's local refs/heads/<branch>, so "the current base head"
// always means "just fetched" (issue #4074).
func fetchBranchTip(cred gitexec.Credential, gitArgs []string, repo, url, branch string) error {
	ref := "refs/heads/" + branch
	cmd := cred.Apply(gitexec.Cmd(gitArgs, "-C", repo, "fetch", "--depth=1", "-q", url, "+"+ref+":"+ref), url)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("butler: fetch branch %s from %s: %w: %s", branch, forge.RedactURLCredentials(url), err, forge.RedactURLCredentials(string(out)))
	}
	return nil
}

// CommitPatch implements Tree. The dry-run apply against scanned (ADR 0057)
// catches a diff that never matched what the Box reviewed, even when it
// happens to apply to the tip; the real apply against the re-fetched tip
// fails a stale/rebased diff.
func (g GitTree) CommitPatch(branch string, scanned ScannedCommit, diff, message string) (PatchCommit, error) {
	if g.Name == "" || g.Email == "" {
		return PatchCommit{}, fmt.Errorf("butler: CommitPatch: no launcher identity configured for %s", g.Repo)
	}

	entries, err := signalwire.ParseUnifiedDiff(diff)
	if err != nil {
		return PatchCommit{}, fmt.Errorf("butler: CommitPatch: diff does not parse: %w", err)
	}
	if err := verifyDiffMatchesGit(g.GitArgs, g.Repo, diff, entries); err != nil {
		return PatchCommit{}, err
	}

	// A private index file (Repo is bare and has none), so nothing here can
	// collide with another Tree operation on the same repo.
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

	if _, err := run([]string{indexEnv}, "", "read-tree", string(scanned)); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "read-tree scanned commit %s", scanned)
	}
	if _, err := run([]string{indexEnv}, diff, "apply", "--cached", "--check", "-"); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "diff does not apply to scanned commit %s", scanned)
	}

	if g.URL != "" {
		if err := fetchBranchTip(g.Credential, g.GitArgs, g.Repo, g.URL, branch); err != nil {
			return PatchCommit{}, err
		}
	}
	tip, err := g.Head(branch)
	if err != nil {
		return PatchCommit{}, err
	}

	if _, err := run([]string{indexEnv}, "", "read-tree", tip); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "read-tree %s", tip)
	}
	if _, err := run([]string{indexEnv}, diff, "apply", "--cached", "-"); err != nil {
		return PatchCommit{}, gitexec.OutputErr("butler", err, "diff does not apply to base head %s", tip)
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

// verifyDiffMatchesGit refuses diff unless git's own reading of it (`git
// apply --numstat --summary`) matches entries exactly: same paths, order and
// +/- counts, and no create/delete/rename/copy/mode summary line. The gates
// decide on our parser's entries; this keeps a parser gap from letting git
// apply something they never saw.
func verifyDiffMatchesGit(gitArgs []string, repo, diff string, entries []signalwire.DiffFile) error {
	want := make([]string, len(entries))
	for i, f := range entries {
		want[i] = fmt.Sprintf("%d\t%d\t%s", f.Added, f.Removed, f.Path)
	}

	cmd := gitexec.Cmd(gitArgs, "-C", repo, "apply", "--numstat", "--summary", "-")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.Output()
	if err != nil {
		return gitexec.OutputErr("butler", err, "git's own reading of the diff")
	}

	var numstat []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, " "):
			return fmt.Errorf("butler: CommitPatch: git's reading of the diff disagrees with the parsed one: git reports a summary line %q", line)
		default:
			numstat = append(numstat, line)
		}
	}

	if len(numstat) != len(want) {
		return fmt.Errorf("butler: CommitPatch: git's reading of the diff disagrees with the parsed one: git reports %d file(s), parsed %d", len(numstat), len(want))
	}
	for i := range want {
		if numstat[i] != want[i] {
			return fmt.Errorf("butler: CommitPatch: git's reading of the diff disagrees with the parsed one: git reports %q, parsed %q", numstat[i], want[i])
		}
	}
	return nil
}
