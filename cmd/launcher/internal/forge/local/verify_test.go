package local

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// TestLocalCodeForge_VerifyLanding_ReportsMergedAfterCleanLand asserts
// VerifyLanding reports merged=true for the exact landing: ref LandingRef
// resolved right after a clean Merge — the no-network "is this seam actually
// merged into the Integration branch" check reconcile relies on (ADR 0029,
// ADR 0033).
func TestLocalCodeForge_VerifyLanding_ReportsMergedAfterCleanLand(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	outbox := t.TempDir()
	branch := "agent/issue-1698"
	seedBundleBranch(t, repo.Bare, IntegrationBranch(parent), outbox, branch, "1698")

	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	br := cf.(forge.BundleRelay)
	lr := cf.(forge.LandingRef)
	verifier, ok := cf.(forge.LandingVerifier)
	if !ok {
		t.Fatal("local CodeForge does not implement forge.LandingVerifier")
	}

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

	merged, err := verifier.VerifyLanding(landing)
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if !merged {
		t.Errorf("VerifyLanding(%q) = false, want true", landing)
	}
}

// TestLocalCodeForge_VerifyLanding_ReportsUnmergedForMalformedRef asserts
// VerifyLanding reports merged=false, no error, for a landing that doesn't
// parse as "<branch>@<sha>" — the raw agent-branch name settle records
// before attempting a merge (gate.go's early recordLanding call), which
// never gets overwritten when that merge goes on to conflict (ADR 0033: "a
// conflicting merge leaves the seam unlanded and blocked").
func TestLocalCodeForge_VerifyLanding_ReportsUnmergedForMalformedRef(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	verifier := cf.(forge.LandingVerifier)

	merged, err := verifier.VerifyLanding("agent/issue-1698")
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if merged {
		t.Error("VerifyLanding(malformed ref) = true, want false")
	}
}

// TestLocalCodeForge_VerifyLanding_ReportsUnmergedForUnknownSHA asserts
// VerifyLanding reports merged=false, no error, for a well-formed ref whose
// sha the repo has never seen — never a genuine Go error, since a stale or
// forged ref must leave the seam-issue open exactly like an unmerged one.
func TestLocalCodeForge_VerifyLanding_ReportsUnmergedForUnknownSHA(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	verifier := cf.(forge.LandingVerifier)

	landing := IntegrationBranch(parent) + "@0000000000000000000000000000000000000000"
	merged, err := verifier.VerifyLanding(landing)
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if merged {
		t.Error("VerifyLanding(unknown sha) = true, want false")
	}
}

// TestLocalCodeForge_VerifyLanding_ReportsUnmergedForNonexistentBranch
// asserts VerifyLanding reports merged=false for a landing naming a branch
// that doesn't exist in the repo at all — a stale or forged ref, same
// "not merged" posture as any other not-yet-landed seam.
func TestLocalCodeForge_VerifyLanding_ReportsUnmergedForNonexistentBranch(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	verifier := cf.(forge.LandingVerifier)

	sha := revParse(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent))
	merged, err := verifier.VerifyLanding("integration/9999@" + sha)
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if merged {
		t.Error("VerifyLanding(nonexistent branch) = true, want false")
	}
}

// TestLocalCodeForge_VerifyLanding_IsInstanceAgnostic asserts VerifyLanding
// checks the landing's own named branch and sha, not whichever parent this
// particular CodeForge instance was constructed with (issue #1734: a single
// shared instance now verifies landings across every parent in a mixed
// batch, not just the one it happened to be built for). A landing for
// parent 2200, verified through a CodeForge instance built for parent 1694,
// still reports merged=true.
func TestLocalCodeForge_VerifyLanding_IsInstanceAgnostic(t *testing.T) {
	setGitIdentityEnv(t)

	const parent1, parent2 = "1694", "2200"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent1))
	cf1 := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent1), parent1, "Test Bot", "bot@example.com", "agent/issue-")

	// parent2's Integration branch doesn't exist yet -- cf2's RelayBundle
	// creates it from cf1's own Integration branch tip on demand
	// (ensureIntegrationBranch), exactly like a second broad ticket's first
	// seam landing in the same run.
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
	landing2, err := cf2.(forge.LandingRef).LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}

	merged, err := cf1.(forge.LandingVerifier).VerifyLanding(landing2)
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if !merged {
		t.Errorf("VerifyLanding(%q) via a differently-parented instance = false, want true", landing2)
	}
}

// TestLocalCodeForge_VerifyLanding_ReportsUnmergedForDashPrefixedSHA asserts
// VerifyLanding rejects a sha starting with "-" as malformed outright,
// rather than relying solely on the "--" end-of-options guard passed to git
// merge-base to keep it from being misread as an option.
func TestLocalCodeForge_VerifyLanding_ReportsUnmergedForDashPrefixedSHA(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	verifier := cf.(forge.LandingVerifier)

	merged, err := verifier.VerifyLanding(IntegrationBranch(parent) + "@-not-a-sha")
	if err != nil {
		t.Fatalf("VerifyLanding: %v", err)
	}
	if merged {
		t.Error("VerifyLanding(dash-prefixed sha) = true, want false")
	}
}

// TestLocalCodeForge_VerifyLanding_ErrorsOnGenuineGitFailure asserts
// VerifyLanding returns a real error — not merged=false — when git itself
// cannot even run, distinct from every "not merged" outcome above, which all
// come from git running fine and reporting non-ancestry.
func TestLocalCodeForge_VerifyLanding_ErrorsOnGenuineGitFailure(t *testing.T) {
	setGitIdentityEnv(t)

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	cf := NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	verifier := cf.(forge.LandingVerifier)
	sha := revParse(t, repo.Bare, "refs/heads/"+IntegrationBranch(parent))

	t.Setenv("PATH", "")
	if _, err := verifier.VerifyLanding(IntegrationBranch(parent) + "@" + sha); err == nil {
		t.Fatal("VerifyLanding with no git on PATH: got nil error, want one")
	}
}
