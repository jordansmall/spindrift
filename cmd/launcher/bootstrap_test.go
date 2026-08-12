package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/settle"
)

// TestBootstrap_PropagatesValidateError asserts bootstrap runs the shared
// config load+validate step and surfaces a validation error without
// constructing a runner, forge client, or dispatch factory.
func TestBootstrap_PropagatesValidateError(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	lc, err := bootstrap(true, dispatchKindWork, false)

	if lc != nil {
		t.Errorf("bootstrap() launch context = %+v, want nil on validate error", lc)
	}
	if err == nil || !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Fatalf("bootstrap() error = %v, want a REPO_SLUG validation error", err)
	}
}

// mustRunGit runs `git -C dir args...` via the package's own runGit helper,
// failing t on error.
func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := runGit(dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// mustSeedableCheckout creates a throwaway git checkout with a single commit
// on "main", suitable as the pwd argument to seedAccumulationRepoIfLocal.
func mustSeedableCheckout(t *testing.T) string {
	t.Helper()
	checkout := t.TempDir()
	mustRunGit(t, checkout, "init", "-b", "main")
	mustRunGit(t, checkout, "config", "user.email", "test@example.com")
	mustRunGit(t, checkout, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(checkout, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, checkout, "add", "base.txt")
	mustRunGit(t, checkout, "commit", "-m", "base")
	return checkout
}

// assertClonableAccumulationRepo verifies repoPath is not just present on
// disk but actually clonable: HEAD resolves to a real ref (rather than the
// dangling one git init --bare would leave behind, see SeedAccumulationRepo's
// symbolic-ref step) and that ref has a commit, which is what the "cloning
// and exploring" acceptance criterion turns on.
func assertClonableAccumulationRepo(t *testing.T, repoPath, baseBranch string) {
	t.Helper()
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("Accumulation repo not created: %v", err)
	}
	if err := runGit(repoPath, "rev-parse", "--verify", "refs/heads/"+baseBranch); err != nil {
		t.Errorf("Accumulation repo has no %s ref: %v", baseBranch, err)
	}
	head, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != "refs/heads/"+baseBranch {
		t.Errorf("Accumulation repo HEAD = %s, want refs/heads/%s", got, baseBranch)
	}
}

// TestSeedAccumulationRepoIfLocal_Local_SeedsFromPwd verifies
// seedAccumulationRepoIfLocal wires local.SeedAccumulationRepo (ADR 0033)
// against config's already-resolved codeForgeAccumulationRepoDir and
// baseBranch, seeding the bare Accumulation repo from pwd's checkout (issue
// #1726: seeding must happen before any Box runs, since a defaulted-but-
// nonexistent path makes the /repo mount silently skip).
func TestSeedAccumulationRepoIfLocal_Local_SeedsFromPwd(t *testing.T) {
	checkout := mustSeedableCheckout(t)

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err != nil {
		t.Fatalf("seedAccumulationRepoIfLocal: %v", err)
	}
	if lock == nil {
		t.Fatal("seedAccumulationRepoIfLocal lock = nil, want a held *local.AccumulationLock (issue #2441)")
	}
	t.Cleanup(func() { _ = lock.Release() })

	assertClonableAccumulationRepo(t, repoPath, "main")
}

// TestSeedAccumulationRepoIfLocal_NonLocal_NoOp verifies
// seedAccumulationRepoIfLocal does nothing for github/git (issue #1726
// acceptance criterion: "no seeding occurs" for those forges) — passing a
// nonexistent pwd here would fail SeedAccumulationRepo's git push if it
// were invoked, so a nil error proves the no-op.
func TestSeedAccumulationRepoIfLocal_NonLocal_NoOp(t *testing.T) {
	c := baseConfig()
	c.codeForge = "github"

	lock, err := seedAccumulationRepoIfLocal(c, "/nonexistent/pwd")
	if err != nil {
		t.Errorf("seedAccumulationRepoIfLocal(CODE_FORGE=github) = %v, want nil (no-op)", err)
	}
	if lock != nil {
		t.Errorf("seedAccumulationRepoIfLocal(CODE_FORGE=github) lock = %v, want nil (no-op)", lock)
	}
}

// TestSeedAccumulationRepoIfLocal_ResearchKind_SeedsFromPwd verifies
// seedAccumulationRepoIfLocal now seeds for the research dispatch kind under
// CODE_FORGE=local, as long as c.selfContained is false (issue #2439):
// non-self-contained research still clones and explores the repo in-box
// (agent/entrypoint.sh's clone_repo() under CODE_FORGE=local), so it needs
// /repo mounted just like work does. Only the no-repo selfContained
// sub-mode stays a no-op (see
// TestSeedAccumulationRepoIfLocal_ResearchSelfContained_NoOp).
func TestSeedAccumulationRepoIfLocal_ResearchKind_SeedsFromPwd(t *testing.T) {
	checkout := mustSeedableCheckout(t)

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.dispatchKind = dispatchKindResearch
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err != nil {
		t.Fatalf("seedAccumulationRepoIfLocal: %v", err)
	}
	if lock == nil {
		t.Fatal("seedAccumulationRepoIfLocal lock = nil, want a held *local.AccumulationLock (issue #2441)")
	}
	t.Cleanup(func() { _ = lock.Release() })

	assertClonableAccumulationRepo(t, repoPath, "main")
}

// TestSeedAccumulationRepoIfLocal_ResearchSelfContained_NoOp verifies
// seedAccumulationRepoIfLocal skips seeding for the research dispatch kind's
// self-contained sub-mode (c.selfContained = true) even under
// CODE_FORGE=local: self-contained research never mounts /repo or clones
// anything (it posts one verdict comment and stops), so seeding would be
// pure waste and a needless new failure surface (a missing baseBranch in
// pwd) for a run that never uses the repo it seeded. Passing a nonexistent
// pwd would fail SeedAccumulationRepo's git push if it were invoked, so a
// nil error proves the no-op.
func TestSeedAccumulationRepoIfLocal_ResearchSelfContained_NoOp(t *testing.T) {
	c := baseConfig()
	c.codeForge = "local"
	c.dispatchKind = dispatchKindResearch
	c.selfContained = true
	c.codeForgeAccumulationRepoDir = filepath.Join(t.TempDir(), "accum.git")
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfLocal(c, "/nonexistent/pwd")
	if err != nil {
		t.Errorf("seedAccumulationRepoIfLocal(research kind, selfContained) = %v, want nil (no-op)", err)
	}
	if lock != nil {
		t.Errorf("seedAccumulationRepoIfLocal(research kind, selfContained) lock = %v, want nil (no-op)", lock)
	}
}

// TestSeedAccumulationRepoIfLocal_ConcurrentCallSameRepo_FailsUntilReleased
// is the core regression test for issue #2441: a second, independent
// seedAccumulationRepoIfLocal call against the same repoPath — simulating a
// second `spindrift` process (e.g. a concurrent research and dispatch run)
// — must fail while the first call's returned lock is still held, rather
// than silently racing SeedAccumulationRepo's seed+mount window. Once the
// first lock is released, a third call against the same repoPath succeeds
// again, proving the lock is per-run rather than a permanent wedge.
func TestSeedAccumulationRepoIfLocal_ConcurrentCallSameRepo_FailsUntilReleased(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	firstLock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err != nil {
		t.Fatalf("first seedAccumulationRepoIfLocal: %v", err)
	}
	if firstLock == nil {
		t.Fatal("first seedAccumulationRepoIfLocal lock = nil, want a held *local.AccumulationLock")
	}

	secondLock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err == nil {
		t.Error("second seedAccumulationRepoIfLocal while first lock held = nil error, want contention error (issue #2441)")
	}
	if secondLock != nil {
		t.Errorf("second seedAccumulationRepoIfLocal while first lock held = %v, want nil lock on error", secondLock)
	}

	if err := firstLock.Release(); err != nil {
		t.Fatalf("firstLock.Release(): %v", err)
	}

	thirdLock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err != nil {
		t.Fatalf("third seedAccumulationRepoIfLocal after release: %v", err)
	}
	if thirdLock == nil {
		t.Fatal("third seedAccumulationRepoIfLocal after release lock = nil, want a held *local.AccumulationLock")
	}
	t.Cleanup(func() { _ = thirdLock.Release() })
}

// TestSeedAccumulationRepoIfLocal_SeedFailure_ReleasesLock is the regression
// test for the seed-failure path's `_ = lock.Release()` at
// seedAccumulationRepoIfLocal (bootstrap.go): a checkout with no commit on
// baseBranch makes SeedAccumulationRepo's push fail after the lock is
// already held, and that failure must release the lock rather than leak it
// — a one-call regression there would wedge the repo for the rest of the
// process with nothing catching it. Checking the returned lock is nil isn't
// enough to prove that (a caller could nil it out without releasing), so
// this proves the underlying flock is actually gone by reacquiring it
// directly against the same repoPath.
func TestSeedAccumulationRepoIfLocal_SeedFailure_ReleasesLock(t *testing.T) {
	checkout := t.TempDir()
	mustRunGit(t, checkout, "init", "-b", "main")
	// No commit: baseBranch has no ref yet, so SeedAccumulationRepo's push
	// fails after the lock is already acquired.

	repoPath := filepath.Join(t.TempDir(), "accum.git")
	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repoPath
	c.baseBranch = "main"

	lock, err := seedAccumulationRepoIfLocal(c, checkout)
	if err == nil {
		t.Fatal("seedAccumulationRepoIfLocal with an empty checkout = nil error, want a seed failure")
	}
	if lock != nil {
		t.Fatalf("seedAccumulationRepoIfLocal on seed failure lock = %v, want nil", lock)
	}

	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after seed failure: %v, want the failed attempt's lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// TestBootstrap_EarlyErrorAfterAccumLockAcquired_ReleasesLock is the
// regression test for bootstrap's own early-return window (issue #2441):
// once seedAccumulationRepoIfLocal hands back a held lock, every remaining
// step before launchContext is built can still fail (readiness, the
// read-only gates), and each of those bare `return nil, err` sites used to
// leak the lock rather than release it — only process exit dropped the
// flock. RUNTIME=bwrap keeps r.EnsureReady() a trivial no-op (bwrapAdapter
// never builds or shells out), so BOX_FORGE_AND_ISSUE_ACCESS=read-only
// against the default (github) issue tracker deterministically fails
// checkReadOnlyCapabilityGate instead — offline, past accumLock's
// acquisition, exactly the window in question. Proves the fix the same way
// as the seed-failure test above: by reacquiring the lock directly, rather
// than trusting a nil return alone.
func TestBootstrap_EarlyErrorAfterAccumLockAcquired_ReleasesLock(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() with BOX_FORGE_AND_ISSUE_ACCESS=read-only against the github tracker = nil error, want checkReadOnlyCapabilityGate to reject it")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on early error = %+v, want nil launch context", lc)
	}

	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after bootstrap's early error: %v, want the held lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// TestBootstrap_Success_HoldsAccumLockUntilCleanup is the regression test for
// the fix's actual load-bearing property (issue #2441): the accum lock must
// stay held across a *successful* bootstrap() return — released only when
// the caller later invokes lc.cleanup() — not just on the early-error paths
// TestBootstrap_EarlyErrorAfterAccumLockAcquired_ReleasesLock and
// TestSeedAccumulationRepoIfLocal_SeedFailure_ReleasesLock already cover. A
// mutation that deletes cleanup's `accumLock.Release()` call leaves every
// other test green (nothing else calls lc.cleanup() and then reacquires),
// silently reopening #2441; this test drives bootstrap all the way to a
// successful launchContext, proves the lock is still contended before
// cleanup runs, then proves it's free again after.
func TestBootstrap_Success_HoldsAccumLockUntilCleanup(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want a successful launch context", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}

	// Before cleanup: the lock must still be held, proving it survives past
	// a successful bootstrap() return rather than being released somewhere
	// on the success path before launchContext is even handed back.
	if _, err := local.AcquireAccumulationLock(repoPath); err == nil {
		t.Error("AcquireAccumulationLock before lc.cleanup() = nil error, want contention (accum lock should still be held after a successful bootstrap())")
	}

	lc.cleanup()

	// After cleanup: the lock must now be free.
	reacquired, err := local.AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock after lc.cleanup(): %v, want the lock to have been released", err)
	}
	t.Cleanup(func() { _ = reacquired.Release() })
}

// TestResearchLaunchStack_WiresResearchLabelsAndSettle verifies
// researchLaunchStack (cmdConsole's research-kind mirror of bootstrap's own
// work-kind wiring, issue #1708) returns a tracker carrying the fixed
// agent-research label family and a ResearchSettle, not the work Settle —
// built from the same newIssueTracker/newDispatchFactory/newSettle helpers
// bootstrap itself uses, just with dispatchKindResearch applied. Uses the
// local tracker (like TestNewIssueTracker_ResearchKind_WiresVerdictLabels)
// so the label write is observable from disk with no network dependency.
func TestResearchLaunchStack_WiresResearchLabelsAndSettle(t *testing.T) {
	issuesDir := t.TempDir()
	issueFile := `---
title: Some issue
state: untriaged
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := baseConfig()
	c.issueTracker = "local"
	c.localIssuesDir = issuesDir
	dir := tempLogDir(t)
	lc := &launchContext{
		config:    c,
		pwd:       dir,
		runner:    nil,
		codeForge: forge.NewFake(),
	}

	it, f, s := researchLaunchStack(lc)
	t.Cleanup(f.Cleanup)

	if err := it.TransitionState("42", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}
	iss, err := it.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research") {
		t.Errorf("issue labels = %v, want agent-research", iss.Labels)
	}
	if f == nil {
		t.Fatal("researchLaunchStack factory = nil, want a research-kind *dispatch.Factory")
	}
	if _, ok := s.(*settle.ResearchSettle); !ok {
		t.Errorf("researchLaunchStack settle = %T, want *settle.ResearchSettle", s)
	}
}
