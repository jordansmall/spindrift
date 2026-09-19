package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// Pins contained=true for the LandingIntegrationRef that LandingRef resolves
// right after a clean Merge. This is the no-network "is this seam merged into
// scope's Integration branch" check that reconcile and the wave gate both rely
// on (ADR 0029, ADR 0033, issue #2151).
func TestLocalCodeForge_LandingContained_IntegrationRef_TrueAfterCleanLand(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	lr := cf.(forge.LandingRef)
	query, ok := cf.(forge.LandingContainmentQuery)
	if !ok {
		t.Fatal("local CodeForge does not implement forge.LandingContainmentQuery")
	}

	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	landingStr, err := lr.LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}
	landing, err := forge.ParseLanding(landingStr)
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Errorf("LandingContained(%q) = false, want true", landingStr)
	}
}

// A LandingPRURL reaching the local-only path never touches git, and gets the
// same contained=false, no error posture as a genuine containment miss.
func TestLocalCodeForge_LandingContained_PRURL_ReturnsFalseNil(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingPRURL, URL: "https://github.com/o/r/pull/1"}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(PRURL landing) = true, want false")
	}
}

// A sha the repo has never seen is never a genuine Go error: a stale or forged
// ref must leave the seam issue open exactly like an uncontained one.
func TestLocalCodeForge_LandingContained_IntegrationRef_UnknownSHA(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingIntegrationRef, Branch: IntegrationBranch(parent), SHA: strings.Repeat("0", 40)}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(unknown sha) = true, want false")
	}
}

// The "--" end-of-options guard LandingContained passes to merge-base keeps a
// sha starting with "-" from being read as an option. This test builds the
// Landing directly because forge.ParseLanding would classify a dash-prefixed
// sha as a LandingBranchRef, skipping that guard entirely.
func TestLocalCodeForge_LandingContained_IntegrationRef_DashPrefixedSHA(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingIntegrationRef, Branch: IntegrationBranch(parent), SHA: "-not-a-sha"}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(dash-prefixed sha) = true, want false")
	}
}

// LandingContained checks scope's own named Integration branch, not the parent
// this CodeForge instance was built with: one shared instance checks
// containment for every parent in a mixed batch (issue #1734).
func TestLocalCodeForge_LandingContained_IntegrationRef_InstanceAgnostic(t *testing.T) {
	setGitIdentityEnv(t)

	parent1 := ResolveParent("1694", "")
	parent2 := ResolveParent("2200", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent1))
	cf1 := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent1), parent1, "Test Bot", "bot@example.com", "agent/issue-")

	// parent2's Integration branch doesn't exist yet, so cf2's RelayBundle
	// creates it from cf1's Integration branch tip (ensureIntegrationBranch),
	// like a second broad ticket's first seam landing in the same run.
	cf2 := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent1), parent2, "Test Bot", "bot@example.com", "agent/issue-")
	outbox := t.TempDir()
	branch := "agent/issue-2201"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent1), outbox, branch, "2201")
	if err := cf2.(forge.BundleRelay).RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf2.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	landing2Str, err := cf2.(forge.LandingRef).LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}
	landing2, err := forge.ParseLanding(landing2Str)
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}

	scope2 := forge.NewSeedScope(parent2.String(), IntegrationBranch(parent2))
	query1 := cf1.(forge.LandingContainmentQuery)
	contained, err := query1.LandingContained(landing2, scope2)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Errorf("LandingContained(%q) via a differently-parented instance = false, want true", landing2Str)
	}
}

// The wave gate's dependent-parent containment check (#2130) relies on this
// cross-seam case: scope names a parent whose Integration branch the landing's
// commit never reached.
func TestLocalCodeForge_LandingContained_IntegrationRef_FalseForOtherParent(t *testing.T) {
	setGitIdentityEnv(t)

	parentA := ResolveParent("1694", "")
	parentB := ResolveParent("2200", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parentA))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parentA), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parentA), parentA, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	landingStr, err := cf.(forge.LandingRef).LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}
	landing, err := forge.ParseLanding(landingStr)
	if err != nil {
		t.Fatalf("ParseLanding: %v", err)
	}

	// parentB's Integration branch was never created at all.
	scopeB := forge.NewSeedScope(parentB.String(), IntegrationBranch(parentB))
	query := cf.(forge.LandingContainmentQuery)
	contained, err := query.LandingContained(landing, scopeB)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained against a never-created integration branch = true, want false")
	}
}

// A git that cannot even run must produce a real error, not contained=false,
// unlike every "not contained" outcome above, where git runs fine and reports
// non-ancestry.
func TestLocalCodeForge_LandingContained_IntegrationRef_ErrorsOnGenuineGitFailure(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)
	sha := revParse(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent))

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingIntegrationRef, Branch: IntegrationBranch(parent), SHA: sha}
	t.Setenv("PATH", "")
	if _, err := query.LandingContained(landing, scope); err == nil {
		t.Fatal("LandingContained with no git on PATH: got nil error, want one")
	}
}

// A branch relayed into the Accumulation repo but never merged onto scope's
// Integration branch is the pre-merge state Reconcile's healing path must never
// mistake for a genuine repair opportunity.
func TestLocalCodeForge_LandingContained_BranchRef_FalseBeforeMerge(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	query, ok := cf.(forge.LandingContainmentQuery)
	if !ok {
		t.Fatal("local CodeForge does not implement forge.LandingContainmentQuery")
	}
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained before merge = true, want false")
	}
}

// Once a BranchRef's branch has actually landed, contained=true is how the
// healing path confirms a stuck landing really did merge, and tip resolution
// reconciliation's discovery path relies on the same answer (issue #2151).
func TestLocalCodeForge_LandingContained_BranchRef_TrueAfterMerge(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	query := cf.(forge.LandingContainmentQuery)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Error("LandingContained after merge = false, want true")
	}
}

// A BranchRef naming a branch the Accumulation repo has never seen, because it
// was never relayed or the attempt was abandoned, gets the same "stays open"
// posture as a genuinely uncontained one, not a hard error.
func TestLocalCodeForge_LandingContained_BranchRef_FalseForNonexistentBranch(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: "agent/issue-9999"}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained(nonexistent branch) = true, want false")
	}
}

// A seam that landed via rebase (issue #1889) keeps a branch ref pointing at
// its pre-rebase tip, so the landed commit's new sha is invisible to that stale
// ancestry. Reconcile's healing path re-derives containment from patch content
// instead (issue #1890).
func TestLocalCodeForge_LandingContained_BranchRef_TrueForRebasedLanding(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	query := cf.(forge.LandingContainmentQuery)
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	preLandSHA := revParse(t, repo.Bare, "refs/heads/"+branch)

	// Advance the integration branch with an unrelated commit so replaying
	// branch's own commit onto it is a genuine rebase (a new sha), not a no-op
	// fast-forward, mirroring land_test.go's two-seam setup.
	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, other, "add", "other.txt")
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	run(t, other, "commit", "-m", "other seam")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	// Land branch by rebasing it onto the advanced integration tip and
	// fast-forwarding integration to the result, bypassing cf.Merge, which
	// would resync refs/heads/branch to the rebased result. This test needs a
	// landing whose branch ref never got resynced in the Accumulation repo.
	rebaseWork := t.TempDir()
	run(t, "", "clone", repo.Bare, rebaseWork)
	run(t, rebaseWork, "checkout", branch)
	run(t, rebaseWork, "rebase", "origin/"+IntegrationBranch(parent))
	run(t, rebaseWork, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != preLandSHA {
		t.Fatalf("refs/heads/%s = %s, want unchanged %s", branch, got, preLandSHA)
	}
	if err := exec.Command("git", "-C", repo.Bare, "merge-base", "--is-ancestor", preLandSHA, "refs/heads/"+IntegrationBranch(parent)).Run(); err == nil {
		t.Fatal("branch's pre-rebase tip is an ancestor of integration branch, want not (test setup didn't force a rebase)")
	}

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Error("LandingContained for a rebased-and-landed seam = false, want true")
	}
}

// A rebase replays every commit of a multi-commit seam with a new sha, and all
// of them must be patch-equivalent to the integration branch, not just the
// oldest (issue #1890). A bundle relays a branch's whole base..branch range, so
// a real seam is routinely more than one commit.
func TestLocalCodeForge_LandingContained_BranchRef_TrueForMultiCommitRebasedLanding(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	branch := "agent/issue-1698"

	work := t.TempDir()
	run(t, "", "clone", repo.Bare, work)
	run(t, work, "checkout", IntegrationBranch(parent))
	run(t, work, "checkout", "-b", branch)
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	writeAndCommit(t, work, "feature-1698-a.txt", "a")
	writeAndCommit(t, work, "feature-1698-b.txt", "b")
	run(t, work, "push", "origin", branch)
	preLandSHA := revParse(t, repo.Bare, "refs/heads/"+branch)

	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	writeAndCommit(t, other, "other.txt", "other")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	rebaseWork := t.TempDir()
	run(t, "", "clone", repo.Bare, rebaseWork)
	run(t, rebaseWork, "checkout", branch)
	run(t, rebaseWork, "rebase", "origin/"+IntegrationBranch(parent))
	run(t, rebaseWork, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	if got := revParse(t, repo.Bare, "refs/heads/"+branch); got != preLandSHA {
		t.Fatalf("refs/heads/%s = %s, want unchanged %s", branch, got, preLandSHA)
	}

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if !contained {
		t.Error("LandingContained for a fully rebased multi-commit seam = false, want true")
	}
}

// Patch-equivalence must clear every commit `git cherry` reports on the branch,
// not just the first line, or a genuinely unlanded seam would self-heal to
// closed (issue #1890).
func TestLocalCodeForge_LandingContained_BranchRef_FalseWhenLaterCommitNeverLanded(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	branch := "agent/issue-1698"

	work := t.TempDir()
	run(t, "", "clone", repo.Bare, work)
	run(t, work, "checkout", IntegrationBranch(parent))
	run(t, work, "checkout", "-b", branch)
	run(t, work, "config", "user.email", "test@example.com")
	run(t, work, "config", "user.name", "Test")
	writeAndCommit(t, work, "feature-1698-a.txt", "a")
	oldestSHA := revParse(t, work, "HEAD")
	writeAndCommit(t, work, "feature-1698-b.txt", "b")
	run(t, work, "push", "origin", branch)

	other := t.TempDir()
	run(t, "", "clone", repo.Bare, other)
	run(t, other, "checkout", IntegrationBranch(parent))
	run(t, other, "config", "user.email", "test@example.com")
	run(t, other, "config", "user.name", "Test")
	writeAndCommit(t, other, "other.txt", "other")
	run(t, other, "push", "origin", IntegrationBranch(parent))

	// Land only the oldest commit's patch onto integration; the newest commit
	// genuinely never lands.
	partial := t.TempDir()
	run(t, "", "clone", repo.Bare, partial)
	run(t, partial, "checkout", IntegrationBranch(parent))
	run(t, partial, "config", "user.email", "test@example.com")
	run(t, partial, "config", "user.name", "Test")
	run(t, partial, "cherry-pick", oldestSHA)
	run(t, partial, "push", "origin", "HEAD:refs/heads/"+IntegrationBranch(parent))

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: branch}
	contained, err := query.LandingContained(landing, scope)
	if err != nil {
		t.Fatalf("LandingContained: %v", err)
	}
	if contained {
		t.Error("LandingContained with a genuinely-unlanded later commit = true, want false")
	}
}

// A git that cannot even run must produce a real error, not contained=false,
// unlike the "branch not found" outcome above, where git runs fine and reports
// no such ref.
func TestLocalCodeForge_LandingContained_BranchRef_ErrorsOnGenuineGitFailure(t *testing.T) {
	setGitIdentityEnv(t)

	parent := ResolveParent("1694", "")
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	query := cf.(forge.LandingContainmentQuery)

	scope := forge.NewSeedScope(parent.String(), IntegrationBranch(parent))
	landing := forge.Landing{Kind: forge.LandingBranchRef, Branch: "agent/issue-1698"}
	t.Setenv("PATH", "")
	if _, err := query.LandingContained(landing, scope); err == nil {
		t.Fatal("LandingContained with no git on PATH: got nil error, want one")
	}
}
