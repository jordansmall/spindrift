package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
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

	if err := seedAccumulationRepoIfLocal(c, checkout); err != nil {
		t.Fatalf("seedAccumulationRepoIfLocal: %v", err)
	}

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

	if err := seedAccumulationRepoIfLocal(c, "/nonexistent/pwd"); err != nil {
		t.Errorf("seedAccumulationRepoIfLocal(CODE_FORGE=github) = %v, want nil (no-op)", err)
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

	if err := seedAccumulationRepoIfLocal(c, checkout); err != nil {
		t.Fatalf("seedAccumulationRepoIfLocal: %v", err)
	}

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

	if err := seedAccumulationRepoIfLocal(c, "/nonexistent/pwd"); err != nil {
		t.Errorf("seedAccumulationRepoIfLocal(research kind, selfContained) = %v, want nil (no-op)", err)
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
