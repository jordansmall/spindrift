package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestReadContext_ReconcileLivenessProbe_LocalTracker_ReturnsNonNil verifies
// reconcileLivenessProbe builds a real probe for ISSUE_TRACKER=local, the
// only tracker reconcile's LivenessProbe check reaches (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_LocalTracker_ReturnsNonNil(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	rc := readContext{config: c}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}

// TestReadContext_ReconcileLivenessProbe_GithubTracker_ReturnsNil verifies
// reconcileLivenessProbe returns nil for a non-local tracker, skipping the
// runner build entirely for the common github/jira "nothing to do" refusal
// (issue #2941 AC2).
func TestReadContext_ReconcileLivenessProbe_GithubTracker_ReturnsNil(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	rc := readContext{config: c}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp != nil {
		t.Errorf("reconcileLivenessProbe(pwd) = %v, want nil for ISSUE_TRACKER=github", lp)
	}
}

// TestNewReadContext_FullyLocal_ConstructsClean verifies newReadContext
// wires the IssueTracker/CodeForge for a fully-local run (ISSUE_TRACKER=
// local, CODE_FORGE=local) — the read prefix doctor.go's cmdDoctor and
// reconcile_cmd.go's cmdReconcile now share instead of each building its own
// copy inline (issue #2941).
func TestNewReadContext_FullyLocal_ConstructsClean(t *testing.T) {
	t.Setenv("ISSUE_TRACKER", "local")
	t.Setenv("CODE_FORGE", "local")
	t.Setenv("LOCAL_ISSUES_DIR", t.TempDir())
	t.Setenv("MERGE_MODE", "immediate")
	t.Setenv("CODE_FORGE_ACCUMULATION_REPO_DIR", t.TempDir())
	t.Setenv("GIT_USER_NAME", "bot")
	t.Setenv("GIT_USER_EMAIL", "bot@example.com")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")

	rc := newReadContext()

	if rc.config.issueTracker != "local" {
		t.Errorf("rc.config.issueTracker = %q, want %q", rc.config.issueTracker, "local")
	}
	if rc.issueTracker == nil {
		t.Error("rc.issueTracker = nil, want a non-nil IssueTracker")
	}
	if rc.codeForge == nil {
		t.Error("rc.codeForge = nil, want a non-nil CodeForge")
	}
}

// TestReadContext_ConstructibleAgainstForgeFake verifies readContext can be
// built directly from forge.NewFake(), the fake-based half of #2920's
// Testing Decisions ("each tier constructible against the forge fake and
// env fixtures") that newReadContext's env-fixture test above doesn't cover
// (issue #2941 AC4).
func TestReadContext_ConstructibleAgainstForgeFake(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	fake := forge.NewFake()
	rc := readContext{config: c, issueTracker: fake, codeForge: fake}

	lp := rc.reconcileLivenessProbe(t.TempDir())

	if lp == nil {
		t.Error("reconcileLivenessProbe(pwd) = nil, want a non-nil LivenessProbe for ISSUE_TRACKER=local")
	}
}
