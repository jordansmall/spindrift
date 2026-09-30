package butler

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/signalwire"
)

// newTreeBareRepo builds a bare repo with one commit on "main" holding the
// given paths, via the shared ledgertest.NewRepo fixture, and also returns
// the commit sha the tree tests assert against.
func newTreeBareRepo(t *testing.T, paths ...string) (repo, commit string) {
	t.Helper()
	bare := ledgertest.NewRepo(t, paths...)
	out, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return bare, strings.TrimSpace(string(out))
}

func TestGitTreeHead(t *testing.T) {
	bare, commit := newTreeBareRepo(t, "a.go", "b.go")
	tree := GitTree{Repo: bare}

	got, err := tree.Head("main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != commit {
		t.Fatalf("Head(main) = %s, want %s", got, commit)
	}

	if _, err := tree.Head("no-such-branch"); err == nil {
		t.Fatal("Head(no-such-branch): got nil error, want one")
	}
}

func TestGitTreeTrackedFiles(t *testing.T) {
	// mktree builds one flat tree (no --batch nesting), so this fixture
	// sticks to top-level paths; ls-tree -r's recursion into subtrees is
	// exercised by git itself, not this thin wrapper.
	bare, commit := newTreeBareRepo(t, "c.go", "a.go", "b.go")
	tree := GitTree{Repo: bare}

	got, err := tree.TrackedFiles(commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"a.go", "b.go", "c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TrackedFiles = %v, want %v", got, want)
	}
}

func TestGitTreeTrackedFilesEmptyTree(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	bare := ledgertest.NewRepo(t)

	mktreeCmd := exec.Command("git", "-C", bare, "mktree")
	mktreeCmd.Stdin = strings.NewReader("")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree (empty): %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))
	commitOut, err := exec.Command("git", "-C", bare, "commit-tree", tree, "-m", "empty").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit := strings.TrimSpace(string(commitOut))

	got, err := GitTree{Repo: bare}.TrackedFiles(commit)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TrackedFiles on an empty tree = %v, want empty", got)
	}
}

// TestFetchTree_FetchesBranchAndReturnsTreeOverScratch drives FetchTree
// against a bare repo standing in for a hosted forge's remote: an
// already-initialized bare scratch repo (mirroring the state remoteLedger
// hands FetchTree after ledger.NewRemote's own git init --bare), then
// FetchTree fetches "main" into it exactly once.
func TestFetchTree_FetchesBranchAndReturnsTreeOverScratch(t *testing.T) {
	remote, head := newTreeBareRepo(t, "a.go", "b.go")
	scratch := ledgertest.NewRepo(t)

	tree, err := FetchTree(scratch, remote, "main")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}

	got, err := tree.Head("main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if got != head {
		t.Fatalf("Head(main) = %s, want remote head %s", got, head)
	}

	files, err := tree.TrackedFiles(got)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	sort.Strings(files)
	want := []string{"a.go", "b.go"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("TrackedFiles = %v, want %v", files, want)
	}
}

// makeDiff checks out commit into a throwaway linked worktree of bare
// (bare repos support worktree add, no primary checkout to conflict with),
// overwrites path with newContent, and returns `git diff`'s unified-diff
// text -- a real diff whose hunk headers/context are guaranteed to match
// commit, rather than a hand-authored one that would silently drift from
// whatever ledgertest.NewRepo's fixture format actually produces.
func makeDiff(t *testing.T, bare, commit, path, newContent string) string {
	t.Helper()
	wt := t.TempDir()
	runGitTree(t, "-C", bare, "worktree", "add", "--detach", wt, commit)
	defer runGitTree(t, "-C", bare, "worktree", "remove", "--force", wt)

	if err := os.WriteFile(filepath.Join(wt, path), []byte(newContent), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	out, err := exec.Command("git", "-C", wt, "diff", path).Output()
	if err != nil {
		t.Fatalf("git diff %s: %v", path, err)
	}
	return string(out)
}

// advanceBranch commits content for path on top of branch's current tip in
// bare, via the same linked-worktree trick as makeDiff, and returns the new
// tip's sha. The commit's author/committer identity is passed with -c so
// the commit succeeds with no ambient git config, independent of whatever
// identity env vars a given test did or didn't set for other purposes.
func advanceBranch(t *testing.T, bare, branch, path, content string) string {
	t.Helper()
	wt := t.TempDir()
	runGitTree(t, "-C", bare, "worktree", "add", "--detach", wt, branch)
	defer runGitTree(t, "-C", bare, "worktree", "remove", "--force", wt)

	if err := os.WriteFile(filepath.Join(wt, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	runGitTree(t, "-c", "user.name=Advancer", "-c", "user.email=advancer@example.com", "-C", wt, "commit", "-am", "advance")
	out, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	newHead := strings.TrimSpace(string(out))
	runGitTree(t, "-C", bare, "update-ref", "refs/heads/"+branch, newHead)
	return newHead
}

// runGitTree runs `git args...` and fatals the test on a non-zero exit,
// including stderr in the failure message.
func runGitTree(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// resolveRef runs `git -C repo rev-parse ref` and returns the trimmed sha.
func resolveRef(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// TestGitTreeCommitPatch_CommitsCleanDiffOnTip drives the whole apply/commit
// path over a plain local repo (no URL, so no re-fetch): a diff that
// applies cleanly to the tip lands as a new commit under the configured
// identity, parented on the tip, at patchRef.
func TestGitTreeCommitPatch_CommitsCleanDiffOnTip(t *testing.T) {
	bare, tip := newTreeBareRepo(t)
	diff := makeDiff(t, bare, tip, "a.go", "package a\n\n// patched\n")

	tree := GitTree{Repo: bare, Name: "Butler Bot", Email: "butler@example.com"}
	pc, err := tree.CommitPatch("main", ScannedCommit(tip), diff, "apply butler patch")
	if err != nil {
		t.Fatalf("CommitPatch: %v", err)
	}
	if pc.Dir != bare {
		t.Fatalf("PatchCommit.Dir = %s, want %s", pc.Dir, bare)
	}
	if pc.Ref != patchRef {
		t.Fatalf("PatchCommit.Ref = %s, want %s", pc.Ref, patchRef)
	}

	commit := resolveRef(t, bare, pc.Ref)
	if parent := resolveRef(t, bare, commit+"^"); parent != tip {
		t.Fatalf("commit parent = %s, want tip %s", parent, tip)
	}

	authorOut, err := exec.Command("git", "-C", bare, "log", "-1", "--format=%an <%ae>", commit).Output()
	if err != nil {
		t.Fatalf("log --format author: %v", err)
	}
	if got, want := strings.TrimSpace(string(authorOut)), "Butler Bot <butler@example.com>"; got != want {
		t.Fatalf("commit author = %q, want %q", got, want)
	}

	msgOut, err := exec.Command("git", "-C", bare, "log", "-1", "--format=%s", commit).Output()
	if err != nil {
		t.Fatalf("log --format subject: %v", err)
	}
	if got, want := strings.TrimSpace(string(msgOut)), "apply butler patch"; got != want {
		t.Fatalf("commit message = %q, want %q", got, want)
	}
}

// TestGitTreeCommitPatch_StaleDiffErrorsAndWritesNoRef advances branch past
// the tip the diff was built against, so the diff's context still matches
// scanned (the diff's own origin) but no longer matches the base head gate:
// CommitPatch must fail with an error naming "base head" and must leave
// patchRef untouched (never partially write it).
func TestGitTreeCommitPatch_StaleDiffErrorsAndWritesNoRef(t *testing.T) {
	bare, tip := newTreeBareRepo(t)
	diff := makeDiff(t, bare, tip, "a.go", "package a\n\n// patched\n")
	advanceBranch(t, bare, "main", "a.go", "package a\n\n// unrelated upstream change\n")

	tree := GitTree{Repo: bare, Name: "Butler Bot", Email: "butler@example.com"}
	_, err := tree.CommitPatch("main", ScannedCommit(tip), diff, "apply butler patch")
	if err == nil {
		t.Fatal("CommitPatch on a stale diff: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "base head") {
		t.Fatalf("CommitPatch error = %v, want it to mention %q", err, "base head")
	}

	if _, err := exec.Command("git", "-C", bare, "rev-parse", patchRef).Output(); err == nil {
		t.Fatal("patchRef resolved after a failed CommitPatch, want it unwritten")
	}
}

// TestGitTreeCommitPatch_FailsWhenDiffDoesNotApplyToScannedCommit pins the
// other half of ADR 0057's ordered gate: a diff that applies cleanly to the
// current base head but not to the scanned commit it was actually built
// against (an older commit here) must still fail -- named "scanned commit"
// -- and must leave patchRef untouched. Guards against a diff that only
// happens to still apply to wherever the base head drifted, which was never
// what the Box actually reviewed.
func TestGitTreeCommitPatch_FailsWhenDiffDoesNotApplyToScannedCommit(t *testing.T) {
	bare, scanned := newTreeBareRepo(t)
	tip := advanceBranch(t, bare, "main", "a.go", "package a\n\n// upstream unrelated\n")
	diff := makeDiff(t, bare, tip, "a.go", "package a\n\n// upstream unrelated\n\n// patched\n")

	tree := GitTree{Repo: bare, Name: "Butler Bot", Email: "butler@example.com"}
	_, err := tree.CommitPatch("main", ScannedCommit(scanned), diff, "apply butler patch")
	if err == nil {
		t.Fatal("CommitPatch with a diff that only applies to the base head: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "scanned commit") {
		t.Fatalf("CommitPatch error = %v, want it to mention %q", err, "scanned commit")
	}

	if _, err := exec.Command("git", "-C", bare, "rev-parse", patchRef).Output(); err == nil {
		t.Fatal("patchRef resolved after a failed CommitPatch, want it unwritten")
	}
}

// TestGitTreeCommitPatch_RefetchesBeforeApplying pins that CommitPatch, when
// URL is set (as FetchTree sets it), re-fetches branch's tip rather than
// trusting scratch's ref from an earlier FetchTree call: an upstream commit
// landing between FetchTree and CommitPatch is the parent CommitPatch
// commits on top of. The advance touches only b.go, leaving a.go -- the
// diff's own file -- matching between scanned (origTip) and the refetched
// tip, so the scanned gate and the refetch are each pinned independently.
func TestGitTreeCommitPatch_RefetchesBeforeApplying(t *testing.T) {
	upstream, origTip := newTreeBareRepo(t, "a.go", "b.go")
	scratch := ledgertest.NewRepo(t)

	tree, err := FetchTree(scratch, upstream, "main")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}
	tree.Name, tree.Email = "Butler Bot", "butler@example.com"

	diff := makeDiff(t, upstream, origTip, "a.go", "a.go\n\n// patched\n")
	newTip := advanceBranch(t, upstream, "main", "b.go", "b.go\n\n// upstream advanced\n")

	pc, err := tree.CommitPatch("main", ScannedCommit(origTip), diff, "apply butler patch")
	if err != nil {
		t.Fatalf("CommitPatch: %v", err)
	}
	commit := resolveRef(t, scratch, pc.Ref)
	if parent := resolveRef(t, scratch, commit+"^"); parent != newTip {
		t.Fatalf("commit parent = %s, want re-fetched tip %s", parent, newTip)
	}
}

// TestGitTreeCommitPatch_MissingIdentityErrors pins that CommitPatch refuses
// to commit under git's ambient identity when Name/Email are unset -- a
// host commit must always carry the configured launcher identity.
func TestGitTreeCommitPatch_MissingIdentityErrors(t *testing.T) {
	bare, tip := newTreeBareRepo(t)
	diff := makeDiff(t, bare, tip, "a.go", "package a\n\n// patched\n")

	tree := GitTree{Repo: bare}
	if _, err := tree.CommitPatch("main", ScannedCommit(tip), diff, "apply butler patch"); err == nil {
		t.Fatal("CommitPatch with no identity: got nil error, want one")
	}
	if _, err := exec.Command("git", "-C", bare, "rev-parse", patchRef).Output(); err == nil {
		t.Fatal("patchRef resolved despite missing identity, want it unwritten")
	}
}

// modHunk is an ordinary docs/x.md modification hunk, unrelated to whatever
// attack section precedes it in the two reviewer-attack diffs below.
const modHunk = `diff --git a/docs/x.md b/docs/x.md
index 1111111..2222222 100644
--- a/docs/x.md
+++ b/docs/x.md
@@ -1,2 +1,2 @@
 line1
-line2
+line2changed
`

// TestVerifyDiffMatchesGit_RefusesReviewerAttacks pins the defense-in-depth
// gate itself, independent of whatever the parser currently does with these
// bytes: each diff smuggles a write to CLAUDE.md past a section signalwire's
// old (pre-fix) parser silently dropped -- attack A via a binary-looking
// "Files ... differ" line, attack B via the legacy "rename old"/"rename new"
// header pair -- so entries here is what that old parser returned: just the
// unrelated docs/x.md entry, with no sign CLAUDE.md was ever touched. git's
// own numstat/summary reading of the bytes disagrees (it reports the
// CLAUDE.md section too), and verifyDiffMatchesGit must refuse on that
// disagreement alone, whatever entries a parser handed it (issue #4075).
func TestVerifyDiffMatchesGit_RefusesReviewerAttacks(t *testing.T) {
	bare, _ := newTreeBareRepo(t)
	entries := []signalwire.DiffFile{{Path: "docs/x.md", Added: 1, Removed: 1}}

	tests := map[string]string{
		"binary-looking Files differ line": "diff --git a/CLAUDE.md b/CLAUDE.md\n" +
			"index 1111111..2222222 100644\n" +
			"Files a/CLAUDE.md and b/CLAUDE.md differ\n" +
			modHunk,
		"legacy rename old/new pair": "diff --git a/CLAUDE.md b/docs/evil.md\n" +
			"rename old CLAUDE.md\n" +
			"rename new docs/evil.md\n" +
			modHunk,
	}
	for name, diff := range tests {
		t.Run(name, func(t *testing.T) {
			if err := verifyDiffMatchesGit(nil, bare, diff, entries); err == nil {
				t.Fatal("verifyDiffMatchesGit: got nil error, want one")
			}
		})
	}
}

// TestVerifyDiffMatchesGit_AcceptsMatchingModification pins the non-attack
// case: a plain modification whose parsed entries actually match git's own
// numstat reading must not be refused.
func TestVerifyDiffMatchesGit_AcceptsMatchingModification(t *testing.T) {
	bare, tip := newTreeBareRepo(t)
	diff := makeDiff(t, bare, tip, "a.go", "package a\n\n// patched\n")

	entries, err := signalwire.ParseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff: %v", err)
	}
	if err := verifyDiffMatchesGit(nil, bare, diff, entries); err != nil {
		t.Fatalf("verifyDiffMatchesGit: %v, want nil", err)
	}
}

// TestVerifyDiffMatchesGit_AgreesWithParserOnHunkShape pins parser/git
// agreement on the plain-section create/delete inference from hunk shape
// alone (issue #4116): git reports a summary line exactly where the parser
// reports a create/delete, which the modification-only gate catches first.
func TestVerifyDiffMatchesGit_AgreesWithParserOnHunkShape(t *testing.T) {
	bare, _ := newTreeBareRepo(t)

	tests := []struct {
		name       string
		diff       string
		wantChange string
		wantErr    bool
	}{
		{
			name: "plain multi-hunk all-removed stays a modification",
			diff: "--- a/z.txt\n+++ b/z.txt\n@@ -1 +0,0 @@\n-line1\n@@ -5 +0,0 @@\n-line2\n",
		},
		{
			name: "diff --git section with no extended header stays a modification",
			diff: "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -1 +0,0 @@\n-line\n",
		},
		{
			name:       "plain single-hunk delete",
			diff:       "--- a/x.txt\n+++ b/x.txt\n@@ -1 +0,0 @@\n-line\n",
			wantChange: "deleted",
			wantErr:    true,
		},
		{
			name:       "plain single-hunk create",
			diff:       "--- a/y.txt\n+++ b/y.txt\n@@ -0,0 +1 @@\n+line\n",
			wantChange: "added",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := signalwire.ParseUnifiedDiff(tc.diff)
			if err != nil {
				t.Fatalf("ParseUnifiedDiff: %v", err)
			}
			if len(entries) != 1 || entries[0].Change != tc.wantChange {
				t.Fatalf("entries = %+v, want single entry with Change %q", entries, tc.wantChange)
			}

			err = verifyDiffMatchesGit(nil, bare, tc.diff, entries)
			if tc.wantErr {
				if err == nil {
					t.Fatal("verifyDiffMatchesGit: got nil error, want one")
				}
				if !strings.Contains(err.Error(), "summary line") {
					t.Fatalf("verifyDiffMatchesGit error = %q, want it to mention a summary line", err.Error())
				}
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("verifyDiffMatchesGit: %v, want nil", err)
			}
		})
	}
}

// TestFetchTree_RedactsCredentialsOnError pins that a failed fetch's error
// never leaks a URL's embedded token, the same guarantee ledger.Remote's own
// git wrappers give (forge.RedactURLCredentials) -- the port at 1 refuses
// the connection immediately rather than hanging on GIT_TERMINAL_PROMPT=0.
func TestFetchTree_RedactsCredentialsOnError(t *testing.T) {
	scratch := ledgertest.NewRepo(t)
	const secret = "secret-token"
	url := "https://user:" + secret + "@127.0.0.1:1/x.git"

	_, err := FetchTree(scratch, url, "main")
	if err == nil {
		t.Fatal("FetchTree: got nil error, want one (unreachable remote)")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("FetchTree error leaked the credential: %v", err)
	}
}
