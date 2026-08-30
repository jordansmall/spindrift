package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/ghapptoken"
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

// TestBootstrap_ValidateError_WrapsErrConfigInvalid verifies bootstrap()'s
// own return of a validate(c) failure now satisfies errors.Is(err,
// errConfigInvalid) (issue #2568 slice 1), so a caller can distinguish a
// config-validation failure from any other bootstrap failure -- without
// changing what validate(c) itself returns (see
// TestBootstrap_PropagatesValidateError, which still asserts the raw
// REPO_SLUG error text unchanged).
func TestBootstrap_ValidateError_WrapsErrConfigInvalid(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	_, err := bootstrap(true, dispatchKindWork, false)

	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
}

// TestBootstrap_BadRegistryProxyCredentialEnv_NoUpstreamURL_DoesNotError
// proves bootstrap() only resolves the registry proxy credential when
// REGISTRY_PROXY_UPSTREAM_URL is actually set (issue #2850 review finding),
// and that the registry-proxy-credential doctor row (issue #2853) agrees:
// REGISTRY_PROXY_UPSTREAM_URL is a runtime-only value while the credential
// fields may be committed in flake.nix as standing config, so a credential
// reference left over from that standing config must not abort a launcher
// invocation that never touches the registry proxy at all -- no proxy will
// ever start to use it. REGISTRY_PROXY_CREDENTIAL_ENV here names a variable
// that is deliberately never set with t.Setenv, so resolution would fail
// closed if it ran.
func TestBootstrap_BadRegistryProxyCredentialEnv_NoUpstreamURL_DoesNotError(t *testing.T) {
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
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Setenv("REGISTRY_PROXY_CREDENTIAL_ENV", "SPINDRIFT_TEST_REGISTRY_PROXY_CRED_DOES_NOT_EXIST")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want no error: an unresolved registry proxy credential ref must not matter when REGISTRY_PROXY_UPSTREAM_URL is unset", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}
	t.Cleanup(lc.cleanup)
}

// TestBootstrap_BadRegistryProxyCredentialEnv_WithUpstreamURL_WrapsErrConfigInvalid
// is the sibling of the test above: once REGISTRY_PROXY_UPSTREAM_URL is set,
// the proxy will actually start, so the same unresolved
// REGISTRY_PROXY_CREDENTIAL_ENV reference must now fail bootstrap(), and the
// error must wrap errConfigInvalid the same way the validate(c) error two
// lines above it does (issue #2850 review finding) -- a caller distinguishing
// "the loaded config failed validation" from other bootstrap failures via
// errors.Is must not see this case differently just because it's caught by
// resolveRegistryProxyCredential instead of validate(c) itself.
func TestBootstrap_BadRegistryProxyCredentialEnv_WithUpstreamURL_WrapsErrConfigInvalid(t *testing.T) {
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
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com")
	t.Setenv("REGISTRY_PROXY_CREDENTIAL_ENV", "SPINDRIFT_TEST_REGISTRY_PROXY_CRED_DOES_NOT_EXIST")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err == nil {
		t.Fatal("bootstrap() = nil error, want an error: REGISTRY_PROXY_UPSTREAM_URL is set and REGISTRY_PROXY_CREDENTIAL_ENV names a variable that is not set")
	}
	if lc != nil {
		t.Fatalf("bootstrap() on error = %+v, want nil launch context", lc)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
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
	t.Setenv("RUNNER_KIND", "bwrap")
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
	t.Setenv("RUNNER_KIND", "bwrap")
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

// stubExecutableOnPath creates an executable script named name in a
// throwaway dir and prepends that dir to PATH, so runner.ValidateRuntime's
// exec.LookPath(name) succeeds without the real CLI being installed —
// ValidateRuntime only probes presence via LookPath, it never runs the
// binary, so any executable file satisfies it. The script exits nonzero for
// every invocation, mimicking a real OCI CLI failing `image inspect` against
// a nonexistent image: the correct runner branch never invokes the stub (the
// bwrap branch's readiness checks are unconditional no-ops), but a runner
// selection wrongly routed to the OCI branch instead shells out to it,
// surfacing as a readiness failure instead of silently reporting "ready" —
// this is what lets a caller discriminate the correct branch from a wrong
// one rather than passing either way.
func stubExecutableOnPath(t *testing.T, name string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestBootstrap_RunnerKindBwrap_OverridesMismatchedRuntime proves runner
// selection keys off RUNNER_KIND, not a runtime-name comparison (issue
// #2538): RUNTIME is set to "podman" — a real OCI runtime name, which the
// old `c.runtime == "bwrap"` check would have read as "select OCI" — but
// RUNNER_KIND=bwrap must still route to the bwrap adapter. bwrapAdapter's
// IsReady() is an unconditional no-op (never shells out), so a full,
// otherwise-successful bootstrap() proves the bwrap branch was taken; had
// selection still keyed off RUNTIME, this would instead try to run `podman
// image inspect` and fail. A stub "podman" is put on PATH purely to satisfy
// runner.ValidateRuntime's upfront CLI-presence check (podman itself is
// never invoked once the bwrap branch is correctly selected).
func TestBootstrap_RunnerKindBwrap_OverridesMismatchedRuntime(t *testing.T) {
	stubExecutableOnPath(t, "podman")
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
	t.Setenv("RUNTIME", "podman")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(false, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() with RUNNER_KIND=bwrap and RUNTIME=podman = %v, want success (bwrap selected)", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}
	t.Cleanup(lc.cleanup)
}

// TestBootstrap_RunnerKindOCI_OverridesMatchingRuntime is
// TestBootstrap_RunnerKindBwrap_OverridesMismatchedRuntime's mirror: RUNTIME
// is set to "bwrap" — the literal value the old comparison read as "select
// bwrap" — but RUNNER_KIND=oci must still route to the OCI adapter.
// ociAdapter.IsReady() shells out to `$RUNTIME image inspect`; "bwrap" is
// not an OCI CLI, so that invocation fails and bootstrap surfaces an error
// instead of the trivial bwrap no-op success the old runtime-name
// comparison would have produced. stubExecutableOnPath puts a stub "bwrap"
// on PATH so ValidateRuntime's upfront LookPath("bwrap") check (keyed off
// RUNTIME, not RUNNER_KIND) succeeds on a host without the real bwrap CLI
// installed — without the stub, that LookPath failure would short-circuit
// before runner selection ever runs, and this test would pass vacuously
// regardless of which branch selection took. Asserting the OCI adapter's
// own "image absent" readiness message, rather than merely err != nil, is
// what then proves the OCI branch — not ValidateRuntime — produced this
// failure.
func TestBootstrap_RunnerKindOCI_OverridesMatchingRuntime(t *testing.T) {
	stubExecutableOnPath(t, "bwrap")
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
	t.Setenv("RUNNER_KIND", "oci")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
	t.Chdir(checkout)

	lc, err := bootstrap(false, dispatchKindWork, false)
	if err == nil || !strings.Contains(err.Error(), "image absent") {
		t.Fatalf("bootstrap() with RUNNER_KIND=oci and RUNTIME=bwrap = %v, want the OCI adapter's \"image absent\" readiness error", err)
	}
	if lc != nil {
		t.Fatalf("bootstrap() on readiness error = %+v, want nil launch context", lc)
	}
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

// --- applyGHAppToken / GitHub App installation token wiring (issue #2867) ---

// stubGHAppTokenWatch swaps the package-level ghAppTokenWatch var (bootstrap.go)
// for the duration of the calling test with a fake that returns token/err
// without any real network call, and — when writeFile is true — writes token
// to the tokenFile argument it's called with, mirroring what the real
// ghapptoken.Watch does on a successful first mint. Restored via t.Cleanup.
func stubGHAppTokenWatch(t *testing.T, token string, err error, writeFile bool) *int {
	t.Helper()
	calls := 0
	orig := ghAppTokenWatch
	ghAppTokenWatch = func(ctx context.Context, cfg ghapptoken.Config, tokenFile string, interval time.Duration, stop <-chan struct{}) (string, error) {
		calls++
		if err != nil {
			return "", err
		}
		if writeFile {
			if werr := os.WriteFile(tokenFile, []byte(token), 0o600); werr != nil {
				t.Fatalf("stubGHAppTokenWatch: WriteFile(tokenFile): %v", werr)
			}
		}
		return token, nil
	}
	t.Cleanup(func() { ghAppTokenWatch = orig })
	return &calls
}

// TestApplyGHAppToken_NoAppID_NoOp verifies applyGHAppToken is a no-op — no
// mint attempted, c left untouched — when c.ghAppID is unset, the
// pre-issue-#2867 default.
func TestApplyGHAppToken_NoAppID_NoOp(t *testing.T) {
	calls := stubGHAppTokenWatch(t, "should-not-be-used", nil, false)

	c := minimalValidConfig()
	c.ghAppID = ""
	c.ghToken = "ambient-token"

	tokenFile, err := applyGHAppToken(&c)
	if err != nil {
		t.Fatalf("applyGHAppToken() = %v, want nil error", err)
	}
	if tokenFile != "" {
		t.Errorf("applyGHAppToken() tokenFile = %q, want empty", tokenFile)
	}
	if c.ghToken != "ambient-token" {
		t.Errorf("applyGHAppToken() left c.ghToken = %q, want unchanged %q", c.ghToken, "ambient-token")
	}
	if *calls != 0 {
		t.Errorf("ghAppTokenWatch called %d times, want 0 (no-op path)", *calls)
	}
}

// TestApplyGHAppToken_PartialConfig_ErrorsWithoutMinting verifies a partial
// App config (only GH_APP_ID set) is rejected by applyGHAppToken's own
// validateGHAppConfig call before any mint is attempted.
func TestApplyGHAppToken_PartialConfig_ErrorsWithoutMinting(t *testing.T) {
	calls := stubGHAppTokenWatch(t, "should-not-be-used", nil, false)

	c := minimalValidConfig()
	c.ghAppID = "123"
	c.ghAppPrivateKeyFile = ""
	c.ghAppInstallationID = ""

	tokenFile, err := applyGHAppToken(&c)
	if err == nil {
		t.Fatal("applyGHAppToken() with only GH_APP_ID set = nil error, want a partial-config error")
	}
	if !strings.Contains(err.Error(), "GH_APP_PRIVATE_KEY_FILE") {
		t.Errorf("applyGHAppToken() error = %v, want it to mention GH_APP_PRIVATE_KEY_FILE", err)
	}
	if tokenFile != "" {
		t.Errorf("applyGHAppToken() tokenFile = %q, want empty on error", tokenFile)
	}
	if *calls != 0 {
		t.Errorf("ghAppTokenWatch called %d times, want 0: minting must not be attempted against a partial config", *calls)
	}
}

// TestApplyGHAppToken_FullConfigWithExplicitRefreshFile_ErrorsWithoutMinting
// verifies a fully configured App trio combined with an explicit
// GH_TOKEN_REFRESH_FILE is rejected before any mint is attempted (the
// mutual-exclusion half of validateGHAppConfig).
func TestApplyGHAppToken_FullConfigWithExplicitRefreshFile_ErrorsWithoutMinting(t *testing.T) {
	calls := stubGHAppTokenWatch(t, "should-not-be-used", nil, false)

	c := minimalValidConfig()
	c.ghAppID = "123"
	c.ghAppPrivateKeyFile = "/key.pem"
	c.ghAppInstallationID = "456"
	c.ghTokenRefreshFile = "/some/refresh/file"

	tokenFile, err := applyGHAppToken(&c)
	if err == nil {
		t.Fatal("applyGHAppToken() with an explicit GH_TOKEN_REFRESH_FILE alongside a full App trio = nil error, want a mutual-exclusion error")
	}
	if tokenFile != "" {
		t.Errorf("applyGHAppToken() tokenFile = %q, want empty on error", tokenFile)
	}
	if *calls != 0 {
		t.Errorf("ghAppTokenWatch called %d times, want 0: minting must not be attempted against a conflicting config", *calls)
	}
}

// TestApplyGHAppToken_FullConfig_MintsSetsTokenAndEnv verifies a fully
// configured App trio mints via ghAppTokenWatch, populates c.ghToken and the
// GH_TOKEN env var with the returned token, and returns a non-empty token
// file.
func TestApplyGHAppToken_FullConfig_MintsSetsTokenAndEnv(t *testing.T) {
	const wantToken = "fake-minted-token"
	origGHToken, hadGHToken := os.LookupEnv("GH_TOKEN")
	t.Cleanup(func() {
		if hadGHToken {
			_ = os.Setenv("GH_TOKEN", origGHToken)
		} else {
			_ = os.Unsetenv("GH_TOKEN")
		}
	})
	calls := stubGHAppTokenWatch(t, wantToken, nil, true)

	c := minimalValidConfig()
	c.ghToken = ""
	c.ghAppID = "123"
	c.ghAppPrivateKeyFile = "/key.pem"
	c.ghAppInstallationID = "456"

	tokenFile, err := applyGHAppToken(&c)
	if err != nil {
		t.Fatalf("applyGHAppToken() = %v, want nil error", err)
	}
	if *calls != 1 {
		t.Errorf("ghAppTokenWatch called %d times, want exactly 1", *calls)
	}
	if tokenFile == "" {
		t.Fatal("applyGHAppToken() tokenFile = empty, want a non-empty path")
	}
	if c.ghToken != wantToken {
		t.Errorf("applyGHAppToken() c.ghToken = %q, want %q", c.ghToken, wantToken)
	}
	if got := os.Getenv("GH_TOKEN"); got != wantToken {
		t.Errorf("applyGHAppToken() GH_TOKEN env = %q, want %q", got, wantToken)
	}
	data, rerr := os.ReadFile(tokenFile)
	if rerr != nil {
		t.Fatalf("ReadFile(tokenFile): %v", rerr)
	}
	if string(data) != wantToken {
		t.Errorf("tokenFile content = %q, want %q", string(data), wantToken)
	}
}

// TestApplyGHAppToken_MintError_ReturnsError verifies a ghAppTokenWatch
// failure surfaces as an applyGHAppToken error (which bootstrap() wraps in
// errConfigInvalid) rather than being swallowed, and leaves c.ghToken
// untouched.
func TestApplyGHAppToken_MintError_ReturnsError(t *testing.T) {
	wantErr := errors.New("boom: bad App credentials")
	stubGHAppTokenWatch(t, "", wantErr, false)

	c := minimalValidConfig()
	c.ghToken = ""
	c.ghAppID = "123"
	c.ghAppPrivateKeyFile = "/key.pem"
	c.ghAppInstallationID = "456"

	tokenFile, err := applyGHAppToken(&c)
	if err == nil {
		t.Fatal("applyGHAppToken() = nil error, want the mint failure surfaced")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("applyGHAppToken() error = %v, want it to wrap %v", err, wantErr)
	}
	if tokenFile != "" {
		t.Errorf("applyGHAppToken() tokenFile = %q, want empty on error", tokenFile)
	}
	if c.ghToken != "" {
		t.Errorf("applyGHAppToken() c.ghToken = %q, want unchanged empty on mint failure", c.ghToken)
	}
}

// --- bootstrap()'s GitHub App wiring end-to-end (issue #2867) ---

// ghAppEnvSetup sets the common env bootstrap() needs to reach the App
// wiring step successfully once GH_TOKEN itself comes from the mint rather
// than the ambient environment: everything TestBootstrap_Success_
// HoldsAccumLockUntilCleanup sets, minus GH_TOKEN.
func ghAppEnvSetup(t *testing.T, repoPath, issuesDir string) {
	t.Helper()
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GIT_USER_NAME", "Test")
	t.Setenv("GIT_USER_EMAIL", "test@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", repoPath)
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("RUNTIME", "bwrap")
	t.Setenv("RUNNER_KIND", "bwrap")
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("LOCAL_ISSUES_DIR", issuesDir)
}

func writeMinimalLocalIssue(t *testing.T, issuesDir string) {
	t.Helper()
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
}

// TestBootstrap_GHAppFullConfig_WiresTokenAndRefreshFile is the end-to-end
// success case (issue #2867): with GH_APP_ID/GH_APP_PRIVATE_KEY_FILE/
// GH_APP_INSTALLATION_ID set and no ambient GH_TOKEN at all, bootstrap()
// mints via the stubbed ghAppTokenWatch, ends up with c.ghToken set to the
// fake token (satisfying validate(c)'s "gh-token" required-knob check
// purely from the mint), GH_TOKEN itself set in the environment, and
// c.ghTokenRefreshFile pointing at a file that actually contains that token.
func TestBootstrap_GHAppFullConfig_WiresTokenAndRefreshFile(t *testing.T) {
	const wantToken = "fake-minted-token"
	origGHToken, hadGHToken := os.LookupEnv("GH_TOKEN")
	t.Cleanup(func() {
		if hadGHToken {
			_ = os.Setenv("GH_TOKEN", origGHToken)
		} else {
			_ = os.Unsetenv("GH_TOKEN")
		}
	})
	// The AC1 claim under test is "no ambient GH_TOKEN at all" -- saving and
	// restoring the surrounding env isn't enough to exercise that if the
	// process running this test happens to have GH_TOKEN exported already;
	// unset it explicitly so the test fails honestly if bootstrap() ever
	// starts relying on an ambient value instead of the mint.
	_ = os.Unsetenv("GH_TOKEN")
	stubGHAppTokenWatch(t, wantToken, nil, true)

	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	issuesDir := t.TempDir()
	writeMinimalLocalIssue(t, issuesDir)

	ghAppEnvSetup(t, repoPath, issuesDir)
	t.Setenv("GH_APP_ID", "123")
	t.Setenv("GH_APP_PRIVATE_KEY_FILE", "/key.pem")
	t.Setenv("GH_APP_INSTALLATION_ID", "456")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want success", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}
	t.Cleanup(lc.cleanup)

	if lc.config.ghToken != wantToken {
		t.Errorf("bootstrap() config.ghToken = %q, want %q", lc.config.ghToken, wantToken)
	}
	if got := os.Getenv("GH_TOKEN"); got != wantToken {
		t.Errorf("bootstrap() GH_TOKEN env = %q, want %q", got, wantToken)
	}
	if lc.config.ghTokenRefreshFile == "" {
		t.Fatal("bootstrap() config.ghTokenRefreshFile = empty, want it wired to the minted token file")
	}
	data, rerr := os.ReadFile(lc.config.ghTokenRefreshFile)
	if rerr != nil {
		t.Fatalf("ReadFile(config.ghTokenRefreshFile): %v", rerr)
	}
	if string(data) != wantToken {
		t.Errorf("config.ghTokenRefreshFile content = %q, want %q", string(data), wantToken)
	}

	// lc.cleanup (registered above via t.Cleanup) must remove the minted
	// token directory (bootstrap.go), not just the accumulation lock --
	// otherwise a live installation token stays readable on disk
	// indefinitely after the run ends. Calling it early and asserting here,
	// ahead of the t.Cleanup-registered call, verifies that directly; the
	// later, idempotent call from t.Cleanup is a no-op against an
	// already-removed directory.
	tokenDir := filepath.Dir(lc.config.ghTokenRefreshFile)
	lc.cleanup()
	if _, statErr := os.Stat(tokenDir); !os.IsNotExist(statErr) {
		t.Errorf("bootstrap() cleanup() left token dir %q behind, want it removed", tokenDir)
	}
}

// TestBootstrap_GHAppPartialConfig_WrapsErrConfigInvalid verifies bootstrap()
// rejects a partial App config (only GH_APP_ID set) with a clear
// errConfigInvalid-wrapped error naming the missing knobs, rather than the
// confusing generic "gh-token required" error a naive implementation might
// surface instead.
func TestBootstrap_GHAppPartialConfig_WrapsErrConfigInvalid(t *testing.T) {
	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	issuesDir := t.TempDir()
	writeMinimalLocalIssue(t, issuesDir)

	ghAppEnvSetup(t, repoPath, issuesDir)
	t.Setenv("GH_APP_ID", "123")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if lc != nil {
		t.Errorf("bootstrap() launch context = %+v, want nil on a partial App config error", lc)
	}
	if err == nil || !strings.Contains(err.Error(), "GH_APP_PRIVATE_KEY_FILE") {
		t.Fatalf("bootstrap() error = %v, want a GH_APP_PRIVATE_KEY_FILE partial-config error", err)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
}

// TestBootstrap_GHAppMintError_WrapsErrConfigInvalid verifies a
// ghAppTokenWatch mint failure surfaces from bootstrap() wrapped in
// errConfigInvalid, mirroring TestBootstrap_ValidateError_WrapsErrConfigInvalid's
// style for the other config-invalid paths.
func TestBootstrap_GHAppMintError_WrapsErrConfigInvalid(t *testing.T) {
	mintErr := errors.New("boom: bad App credentials")
	stubGHAppTokenWatch(t, "", mintErr, false)

	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	issuesDir := t.TempDir()
	writeMinimalLocalIssue(t, issuesDir)

	ghAppEnvSetup(t, repoPath, issuesDir)
	t.Setenv("GH_APP_ID", "123")
	t.Setenv("GH_APP_PRIVATE_KEY_FILE", "/key.pem")
	t.Setenv("GH_APP_INSTALLATION_ID", "456")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if lc != nil {
		t.Errorf("bootstrap() launch context = %+v, want nil on a mint failure", lc)
	}
	if err == nil || !strings.Contains(err.Error(), mintErr.Error()) {
		t.Fatalf("bootstrap() error = %v, want it to surface %v", err, mintErr)
	}
	if !errors.Is(err, errConfigInvalid) {
		t.Fatalf("bootstrap() error = %v, want errors.Is(err, errConfigInvalid) = true", err)
	}
}

// TestBootstrap_GHAppFullConfig_ValidateErrorAfterMint_CleansUpTokenDir
// verifies bootstrap() removes the minted token directory when a step after
// a *successful* mint fails -- here, validate(c) itself rejecting an
// invalid MERGE_MODE, the exact scenario a prior review round reproduced
// empirically (MERGE_MODE=not-a-real-mode left
// /tmp/spindrift-gh-app-token-*/token on disk). Before this fix, only
// applyGHAppToken's own internal errors were cleaned up; every later
// `return nil, err` in bootstrap() left a live installation token on disk
// indefinitely.
func TestBootstrap_GHAppFullConfig_ValidateErrorAfterMint_CleansUpTokenDir(t *testing.T) {
	origGHToken, hadGHToken := os.LookupEnv("GH_TOKEN")
	t.Cleanup(func() {
		if hadGHToken {
			_ = os.Setenv("GH_TOKEN", origGHToken)
		} else {
			_ = os.Unsetenv("GH_TOKEN")
		}
	})
	_ = os.Unsetenv("GH_TOKEN")

	origWatch := ghAppTokenWatch
	var tokenDir string
	ghAppTokenWatch = func(_ context.Context, _ ghapptoken.Config, tokenFile string, _ time.Duration, _ <-chan struct{}) (string, error) {
		tokenDir = filepath.Dir(tokenFile)
		if err := os.WriteFile(tokenFile, []byte("fake-minted-token"), 0o600); err != nil {
			t.Fatalf("write fake token file: %v", err)
		}
		return "fake-minted-token", nil
	}
	t.Cleanup(func() { ghAppTokenWatch = origWatch })

	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	issuesDir := t.TempDir()
	writeMinimalLocalIssue(t, issuesDir)

	ghAppEnvSetup(t, repoPath, issuesDir)
	t.Setenv("GH_APP_ID", "123")
	t.Setenv("GH_APP_PRIVATE_KEY_FILE", "/key.pem")
	t.Setenv("GH_APP_INSTALLATION_ID", "456")
	t.Setenv("MERGE_MODE", "not-a-real-mode")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if lc != nil {
		t.Errorf("bootstrap() launch context = %+v, want nil on a validate(c) error", lc)
	}
	if err == nil {
		t.Fatal("bootstrap() error = nil, want the invalid MERGE_MODE to surface")
	}
	if tokenDir == "" {
		t.Fatal("ghAppTokenWatch was never called, test setup broken")
	}
	if _, statErr := os.Stat(tokenDir); !os.IsNotExist(statErr) {
		t.Errorf("bootstrap() left token dir %q behind after a post-mint validate(c) failure, want it removed", tokenDir)
	}
}

// TestBootstrap_GHTokenAlone_RegressionUnaffectedByAppWiring verifies the
// pre-issue-#2867 GH_TOKEN-only path is byte-for-byte unaffected by the new
// App wiring: no GH_APP_* vars set, so applyGHAppToken is a no-op and
// bootstrap() behaves exactly as it did before this slice.
func TestBootstrap_GHTokenAlone_RegressionUnaffectedByAppWiring(t *testing.T) {
	calls := stubGHAppTokenWatch(t, "should-not-be-used", nil, false)

	checkout := mustSeedableCheckout(t)
	repoPath := filepath.Join(t.TempDir(), "accum.git")
	issuesDir := t.TempDir()
	writeMinimalLocalIssue(t, issuesDir)

	ghAppEnvSetup(t, repoPath, issuesDir)
	t.Setenv("GH_TOKEN", "test-token")
	t.Chdir(checkout)

	lc, err := bootstrap(true, dispatchKindWork, false)
	if err != nil {
		t.Fatalf("bootstrap() = %v, want success (GH_TOKEN-only regression path)", err)
	}
	if lc == nil {
		t.Fatal("bootstrap() launch context = nil, want a non-nil *launchContext on success")
	}
	t.Cleanup(lc.cleanup)

	if lc.config.ghToken != "test-token" {
		t.Errorf("bootstrap() config.ghToken = %q, want unchanged ambient %q", lc.config.ghToken, "test-token")
	}
	if lc.config.ghTokenRefreshFile != "" {
		t.Errorf("bootstrap() config.ghTokenRefreshFile = %q, want empty (no App minting happened)", lc.config.ghTokenRefreshFile)
	}
	if *calls != 0 {
		t.Errorf("ghAppTokenWatch called %d times, want 0", *calls)
	}
}
