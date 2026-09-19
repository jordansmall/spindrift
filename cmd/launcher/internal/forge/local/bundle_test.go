package local

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/seambundle"
)

// setGitIdentityEnv gives ambient git commands a commit identity. The tests
// here otherwise run with none, since production code always sets identity
// explicitly (setCommitIdentity in the git adapter) on the clones it drives.
func setGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}

// seedBundleBranch clones bare, creates branch one commit ahead of base with a
// marker file unique to num, and writes a bundle of base..branch to
// outboxDir/seambundle.FileName. It stands in for the Box's code-out (ADR
// 0033) and never pushes branch to bare directly. Returns branch's HEAD sha.
func seedBundleBranch(t *testing.T, bare, base, outboxDir, branch, num string) string {
	t.Helper()
	work := t.TempDir()
	run(t, "", "clone", bare, work)
	run(t, work, "checkout", base)
	run(t, work, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(work, "feature-"+num+".txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "feature-"+num+".txt")
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	run(t, work, "commit", "-m", "feature "+num)
	run(t, work, "bundle", "create", filepath.Join(outboxDir, seambundle.FileName), base+".."+branch)
	return revParse(t, work, branch)
}

// RelayBundle imports a Box's code-out bundle into the Accumulation repo as
// branch's own ref, so a later Merge(branch), which fetches "origin" branch
// from the same repo, finds it (ADR 0033: bundle in, no direct push).
func TestLocalCodeForge_RelayBundle_ImportsBranchIntoRepo(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	wantSHA := seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br, ok := cf.(forge.BundleRelay)
	if !ok {
		t.Fatal("local CodeForge does not implement forge.BundleRelay")
	}

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}
}

// An empty outbox (the Box crashed, or its code-out never ran) leaves the seam
// unlanded via an error rather than a nil-error no-op (ADR 0033, "missing
// bundle... leaves the seam unlanded and flagged blocked").
func TestLocalCodeForge_RelayBundle_MissingBundleErrors(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1698")
	if err == nil {
		t.Fatal("RelayBundle with no bundle file present: got nil error, want one")
	}
	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with no bundle file present: err = %v, want errors.Is(err, forge.ErrBundleNotFound)", err)
	}
}

// A corrupt bundle file (truncated transfer, disk corruption) is rejected by
// `git bundle verify` rather than fed to fetch, which could behave
// unpredictably on garbage input.
func TestLocalCodeForge_RelayBundle_MalformedBundleErrors(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	if err := os.WriteFile(filepath.Join(outbox, seambundle.FileName), []byte("not a bundle"), 0o644); err != nil {
		t.Fatal(err)
	}

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1698")
	if err == nil {
		t.Fatal("RelayBundle with a malformed bundle file: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with a malformed bundle file: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// RelayBundle reports a stat failure that is not os.IsNotExist (here ENOTDIR,
// standing in for permission-denied or any other error an unreadable outbox
// produces) as a generic error, never as the ErrBundleNotFound reserved for a
// genuinely absent bundle or outbox.
func TestLocalCodeForge_RelayBundle_UnreadableBundleErrors(t *testing.T) {
	setGitIdentityEnv(t)
	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))

	// A regular file where a directory belongs: any path that walks through it
	// makes os.Stat fail with ENOTDIR rather than ENOENT.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(blocker, "outbox")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)

	err := br.RelayBundle(outbox, "agent/issue-1698")
	if err == nil {
		t.Fatal("RelayBundle with an unreadable (ENOTDIR) bundle path: got nil error, want one")
	}
	if errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle with an unreadable (ENOTDIR) bundle path: err = %v, want a generic error, not forge.ErrBundleNotFound", err)
	}
}

// A retry (the Box crashed, was re-dispatched, and rebuilt its bundle from a
// rebased branch) can relay again even though the new tip diverged from the
// failed attempt's ref already in the Accumulation repo. A non-force fetch
// would reject that as non-fast-forward, but a retried seam must win over its
// own abandoned prior attempt.
func TestLocalCodeForge_RelayBundle_ReRelayOverwritesDivergedRef(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (first attempt): %v", err)
	}

	// Rebuild branch from a diverged history: a fresh clone of bare's base, not
	// of the already-relayed ref, so the new commit shares no ancestry with the
	// one already relayed in.
	work := t.TempDir()
	run(t, "", "clone", repo.Bare, work)
	run(t, work, "checkout", IntegrationBranch(parent))
	run(t, work, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(work, "feature-1698.txt"), []byte("retried\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "feature-1698.txt")
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	run(t, work, "commit", "-m", "retried feature 1698")
	wantSHA := revParse(t, work, branch)
	run(t, work, "bundle", "create", filepath.Join(outbox, seambundle.FileName), IntegrationBranch(parent)+".."+branch)

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle (retry, diverged history): %v", err)
	}

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s (the retried bundle's tip)", branch, got, wantSHA)
	}
}

// The first seam of a broad ticket lands even though integration/<parent> does
// not exist yet, only baseBranch, exactly what SeedAccumulationRepo produces
// (ADR 0033: parent tickets seed no Integration branch). Landing must create
// integration/<parent> from baseBranch on demand rather than assume it exists,
// the way a second seam's land legitimately can.
func TestLocalCodeForge_FirstSeam_CreatesIntegrationBranchFromBase(t *testing.T) {
	setGitIdentityEnv(t)

	checkout := newCheckoutFixture(t, "main")
	repoPath := filepath.Join(t.TempDir(), "repo.git")
	if err := SeedAccumulationRepo(repoPath, checkout.dir, "main"); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	parent := ResolveParent("1694", "")
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	// The bundle is built off baseBranch ("main"), not integration/1694: no
	// seam has landed for this parent yet, so that ref cannot exist.
	seedBundleBranch(t, repoPath, "main", outbox, branch, "1698")

	cf := NewLocalCodeForge(repoPath, "main", parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if got := revParse(t, repoPath, "refs/heads/"+IntegrationBranch(parent)); got == "" {
		t.Error("integration branch: want a resolved sha, got empty")
	}
}

// The full single-seam land: relay the bundle in, merge it onto the
// Integration branch, and resolve the landing: reference, which is the
// Integration branch name plus the commit sha the merge produced (ADR
// 0029/0033), so it changes on every land rather than pointing at a stale
// prior merge.
func TestLocalCodeForge_LandBundle_EndToEnd(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	lr := cf.(forge.LandingRef)

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	landing, err := lr.LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}
	wantSHA := revParse(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent))
	if want := IntegrationBranch(parent) + "@" + wantSHA; landing != want {
		t.Errorf("LandingRef = %q, want %q", landing, want)
	}
}
