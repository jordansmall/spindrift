package github

import (
	"errors"
	"os"
	"path/filepath"
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

// TestReadOnlyCodeForge_CreateDraftPR_ReturnsURL asserts CreateDraftPR opens
// a draft PR via `gh pr create` and returns its URL — the host-side
// counterpart to the Box's own in-box `gh pr create` under read-write (issue
// #1919).
func TestReadOnlyCodeForge_CreateDraftPR_ReturnsURL(t *testing.T) {
	newRelayHarness(t)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		t.Fatal("github read-only CodeForge does not implement forge.DraftPRCreator")
	}

	url, err := dpc.CreateDraftPR("feat: add widget", "Adds a widget.", "main", "agent/issue-1919")
	if err != nil {
		t.Fatalf("CreateDraftPR: %v", err)
	}
	want := "https://github.com/owner/repo/pull/1919"
	if url != want {
		t.Errorf("CreateDraftPR url = %q, want %q", url, want)
	}
}

// TestReadOnlyCodeForge_CreateDraftPR_Errors asserts a `gh pr create`
// failure surfaces as an error rather than a blank URL.
func TestReadOnlyCodeForge_CreateDraftPR_Errors(t *testing.T) {
	newRelayHarness(t)

	cf := NewReadOnlyCodeForge("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	dpc := cf.(forge.DraftPRCreator)

	if _, err := dpc.CreateDraftPR("feat: add widget", "body", "main", "fail-head"); err == nil {
		t.Fatal("CreateDraftPR with a failing gh pr create: got nil error, want one")
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
