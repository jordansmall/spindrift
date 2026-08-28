package github

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/seambundle"
)

// newRelayHarness sets up a real bare "remote" repo plus the same fake gh
// script codeforge_contract_test.go uses (its `repo clone` case clones
// $REMOTE for any repo slug), which is all RelayBundle needs to reach.
func newRelayHarness(t *testing.T) *forgetest.GitRepoFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHCodeForge), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE", repo.Bare)
	t.Setenv("STATE_DIR", t.TempDir())

	// forgetest.NewGitRepoFixture's first push (of "main") never updates the
	// bare repo's own HEAD symref away from git-init's default (typically
	// "master", which doesn't exist here), so a fresh clone otherwise has no
	// local "main" branch to check out -- only refs/remotes/origin/main.
	// CommitSubjects's base argument needs "main" itself to resolve for
	// `git log base..ref` to work, the same way it would against a real
	// forge clone.
	if out, err := exec.Command("git", "-C", repo.Bare, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set bare repo HEAD to refs/heads/main: %v: %s", err, out)
	}
	return repo
}

// TestExecClient_DoesNotImplementBundleRelay guards read-write's own
// contract: NewExecClient (BOX_FORGE_AND_ISSUE_ACCESS=read-write, the Box
// pushes in-box) must never satisfy forge.BundleRelay, or settle's generic
// relay-before-merge (ready.go) would try to relay a bundle a read-write Box
// never wrote and block every read-write github land.
func TestExecClient_DoesNotImplementBundleRelay(t *testing.T) {
	var cf forge.CodeForge = NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, ok := cf.(forge.BundleRelay); ok {
		t.Error("NewExecClient satisfies forge.BundleRelay, want it hidden for read-write")
	}
}

// TestReadOnlyCodeForge_ImplementsPRForge asserts the read-only adapter
// keeps the full PRForge surface NewExecClient has (via embedding) — it
// still opens PRs and watches CI exactly as read-write does; only the
// finished branch's hand-off differs (issue #1918).
func TestReadOnlyCodeForge_ImplementsPRForge(t *testing.T) {
	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("NewReadOnlyCodeForge does not satisfy forge.PRForge")
	}
}

// TestReadOnlyCodeForge_RelayBundle_PushesRefToOrigin asserts RelayBundle
// imports a Box's code-out bundle and pushes it to the real remote (unlike
// local's RelayBundle, which only ever imports into its own bare backing
// repo) so the host-side draft-PR-create and the existing ready-flip/
// rebase-merge operate on a real remote branch.
func TestReadOnlyCodeForge_RelayBundle_PushesRefToOrigin(t *testing.T) {
	repo := newRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1918"
	wantSHA := forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	br, ok := cf.(forge.BundleRelay)
	if !ok {
		t.Fatal("github read-only CodeForge does not implement forge.BundleRelay")
	}

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// TestReadOnlyCodeForge_RelayBundle_CloneFailureSurfacesStderr asserts a
// `gh repo clone` failure's stderr text reaches the returned error (via
// ghCommandErr), not just err's own Go-side message.
func TestReadOnlyCodeForge_RelayBundle_CloneFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$1-$2" in
repo-clone)
	printf 'gh: repository not found\n' >&2
	exit 1
	;;
esac
`)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1918")
	if err == nil {
		t.Fatal("RelayBundle with a failing gh repo clone: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("RelayBundle error = %q, want it to contain gh's stderr %q", err.Error(), "repository not found")
	}
}

// TestReadOnlyCodeForge_CommitSubjects_CloneFailureSurfacesStderr mirrors
// TestReadOnlyCodeForge_RelayBundle_CloneFailureSurfacesStderr for
// CommitSubjects's own `gh repo clone` closure.
func TestReadOnlyCodeForge_CommitSubjects_CloneFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$1-$2" in
repo-clone)
	printf 'gh: repository not found\n' >&2
	exit 1
	;;
esac
`)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	cs := cf.(forge.BundleCommitSubjects)

	_, err := cs.CommitSubjects(outbox, "main", "agent/issue-1918")
	if err == nil {
		t.Fatal("CommitSubjects with a failing gh repo clone: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("CommitSubjects error = %q, want it to contain gh's stderr %q", err.Error(), "repository not found")
	}
}

// TestReadOnlyCodeForge_RelayBundle_MissingBundleErrors asserts an empty
// outbox (the Box never wrote a bundle) blocks the seam via an error rather
// than a nil-error no-op, mirroring local's RelayBundle (ADR 0033).
func TestReadOnlyCodeForge_RelayBundle_MissingBundleErrors(t *testing.T) {
	newRelayHarness(t)
	outbox := t.TempDir()

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1918")
	if err == nil {
		t.Fatal("RelayBundle with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// TestReadOnlyCodeForge_RelayBundle_MalformedBundleErrors asserts a corrupt
// bundle file is rejected by `git bundle verify` rather than fed to fetch,
// mirroring local's RelayBundle.
func TestReadOnlyCodeForge_RelayBundle_MalformedBundleErrors(t *testing.T) {
	newRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1918")
	if err == nil {
		t.Fatal("RelayBundle with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// TestReadOnlyCodeForge_CommitSubjects_ReturnsSubjectsReadOnly asserts
// CommitSubjects returns the seeded bundle's commit subjects, oldest first,
// and — unlike RelayBundle — never mutates the real remote: no ref for
// branch appears on repo.Bare afterward.
func TestReadOnlyCodeForge_CommitSubjects_ReturnsSubjectsReadOnly(t *testing.T) {
	repo := newRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1918"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	cs, ok := cf.(forge.BundleCommitSubjects)
	if !ok {
		t.Fatal("github read-only CodeForge does not implement forge.BundleCommitSubjects")
	}

	subjects, err := cs.CommitSubjects(outbox, "main", branch)
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	want := []string{"feature"}
	if len(subjects) != len(want) || subjects[0] != want[0] {
		t.Errorf("CommitSubjects = %v, want %v", subjects, want)
	}

	cmd := exec.Command("git", "-C", repo.Bare, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err := cmd.Run(); err == nil {
		t.Errorf("refs/heads/%s exists on the real remote after CommitSubjects, want no push side effect", branch)
	}
}

// TestReadOnlyCodeForge_CommitSubjects_MissingBundleErrors mirrors
// TestReadOnlyCodeForge_RelayBundle_MissingBundleErrors: an empty outbox (the
// Box never wrote a bundle) surfaces forge.ErrBundleNotFound.
func TestReadOnlyCodeForge_CommitSubjects_MissingBundleErrors(t *testing.T) {
	newRelayHarness(t)
	outbox := t.TempDir()

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	cs := cf.(forge.BundleCommitSubjects)

	_, err := cs.CommitSubjects(outbox, "main", "agent/issue-1918")
	if err == nil {
		t.Fatal("CommitSubjects with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// TestReadOnlyCodeForge_CommitSubjects_MalformedBundleErrors mirrors
// TestReadOnlyCodeForge_RelayBundle_MalformedBundleErrors: a corrupt bundle
// file is rejected by `git bundle verify` and surfaces a generic error, not
// forge.ErrBundleNotFound.
func TestReadOnlyCodeForge_CommitSubjects_MalformedBundleErrors(t *testing.T) {
	newRelayHarness(t)
	outbox := t.TempDir()
	forgetest.WriteFile(t, filepath.Join(outbox, seambundle.FileName), "not a bundle")

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	cs := cf.(forge.BundleCommitSubjects)

	_, err := cs.CommitSubjects(outbox, "main", "agent/issue-1918")
	if err == nil {
		t.Fatal("CommitSubjects with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// TestReadOnlyCodeForge_CreateDraftPR_ReturnsURL asserts CreateDraftPR opens
// a draft PR via `gh pr create`, returns its URL, and reports created=true
// — the host-side counterpart to the Box's own in-box `gh pr create` under
// read-write (issue #1919). created=true distinguishes this fresh-create
// success from the adoption path below (issue #2447).
func TestReadOnlyCodeForge_CreateDraftPR_ReturnsURL(t *testing.T) {
	newRelayHarness(t)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		t.Fatal("github read-only CodeForge does not implement forge.DraftPRCreator")
	}

	url, created, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1919")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://github.com/owner/repo/pull/1919"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if !created {
		t.Error("CreateDraftPR created = false, want true for a fresh create")
	}
}

// TestReadOnlyCodeForge_CreateDraftPR_Errors asserts a `gh pr create`
// failure surfaces as an error rather than a blank URL.
func TestReadOnlyCodeForge_CreateDraftPR_Errors(t *testing.T) {
	newRelayHarness(t)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc := cf.(forge.DraftPRCreator)

	if _, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "fail-head"); err == nil {
		t.Fatal("CreateDraftPR with a failing gh pr create: got nil error, want one")
	}
}

// TestReadOnlyCodeForge_CreateDraftPR_AdoptsExistingOnAlreadyExists asserts
// that when `gh pr create` fails because a PR for this head already exists
// (e.g. a retried fix pass after an earlier host-mediated create already
// succeeded), CreateDraftPR adopts the existing open PR via
// OpenPRForBranch and returns its URL with no error and created=false,
// rather than surfacing the create failure as blocked (issue #2407 slice
// 1). created=false lets a caller like settle's reconstructed-PR path
// (issue #2447) tell this call did not itself open the PR.
func TestReadOnlyCodeForge_CreateDraftPR_AdoptsExistingOnAlreadyExists(t *testing.T) {
	prependFakeGH(t, `case "$1-$2" in
pr-create)
	printf 'a pull request for branch "agent/issue-2407" into branch "main" already exists: https://github.com/owner/repo/pull/2407\n' >&2
	exit 1
	;;
pr-list)
	printf 'https://github.com/owner/repo/pull/2407\n'
	;;
pr-view)
	printf 'true\n'
	;;
esac
`)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc := cf.(forge.DraftPRCreator)

	url, created, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "agent/issue-2407")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://github.com/owner/repo/pull/2407"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
	if created {
		t.Error("CreateDraftPR created = true, want false for an adopted pre-existing PR")
	}
}

// TestReadOnlyCodeForge_CreateDraftPR_AlreadyExistsButNoOpenPRReturnsOriginalError
// asserts that when `gh pr create` fails with an already-exists signal but
// OpenPRForBranch cannot resolve an open PR for that head (e.g. only a
// closed/merged PR exists, or the lookup itself errors), CreateDraftPR
// surfaces the original create error rather than masking it.
func TestReadOnlyCodeForge_CreateDraftPR_AlreadyExistsButNoOpenPRReturnsOriginalError(t *testing.T) {
	prependFakeGH(t, `case "$1-$2" in
pr-create)
	printf 'a pull request for branch "agent/issue-2407" into branch "main" already exists: https://github.com/owner/repo/pull/2407\n' >&2
	exit 1
	;;
pr-list)
	printf '\n'
	;;
esac
`)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc := cf.(forge.DraftPRCreator)

	_, _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "agent/issue-2407")
	if err == nil {
		t.Fatal("CreateDraftPR with already-exists failure but no open PR found: got nil error, want the original create error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("CreateDraftPR error must be the original create error (mentioning \"already exists\"), got: %v", err)
	}
}

// TestExecClient_DoesNotImplementDraftPRCreator guards read-write's own
// contract: NewExecClient must never satisfy forge.DraftPRCreator, mirroring
// TestExecClient_DoesNotImplementBundleRelay — a read-write Box already
// opens its own PR in-box, so settle must never call a host-side create for
// it.
func TestExecClient_DoesNotImplementDraftPRCreator(t *testing.T) {
	var cf forge.CodeForge = NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, ok := cf.(forge.DraftPRCreator); ok {
		t.Error("NewExecClient satisfies forge.DraftPRCreator, want it hidden for read-write")
	}
}

// TestReadOnlyCodeForge_RelayBundle_ReRelayForceUpdatesRef asserts a fix-pass
// retry -- a rebuilt bundle whose branch tip diverged from what an earlier
// pass already relayed -- overwrites the remote ref rather than being
// rejected as non-fast-forward, so the warm fix-pass re-push works (issue
// #1918's acceptance criterion).
func TestReadOnlyCodeForge_RelayBundle_ReRelayForceUpdatesRef(t *testing.T) {
	repo := newRelayHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1918"
	forgetest.SeedRelayBundle(t, repo.Bare, "main", outbox, branch)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (first attempt): %v", err)
	}

	// Rebuild branch from a diverged history (a different marker file, same
	// name) -- a fresh clone of bare's base, not of the already-relayed ref,
	// so the new commit shares no ancestry with the one already relayed in.
	work := t.TempDir()
	forgetest.Run(t, "", "clone", repo.Bare, work)
	forgetest.Run(t, work, "checkout", "main")
	forgetest.Run(t, work, "checkout", "-b", branch)
	forgetest.WriteFile(t, filepath.Join(work, "feature.txt"), "retried\n")
	forgetest.Run(t, work, "add", "feature.txt")
	forgetest.Run(t, work, "commit", "-m", "retried feature")
	wantSHA := forgetest.RevParse(t, work, branch)
	forgetest.Run(t, work, "bundle", "create", filepath.Join(outbox, seambundle.FileName), "main.."+branch)

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (retry, diverged history): %v", err)
	}

	if got := forgetest.RevParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s (the retried bundle's tip)", branch, got, wantSHA)
	}
}
