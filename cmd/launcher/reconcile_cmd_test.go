package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/reconcile"
)

// fakeLiveness is a no-op reconcile.LivenessProbe by default: LogStale
// defaults to false (not stale) and ContainerLive always reports live=false,
// so it never triggers a reset on its own — exactly what the Closed-only
// tests in this file need from the seam.
type fakeLiveness struct {
	stale     map[string]bool
	reachable map[string]bool
}

func (f fakeLiveness) LogStale(num string) bool { return f.stale[num] }

func (f fakeLiveness) ContainerLive(num string) (live, reachable bool) {
	return false, f.reachable[num]
}

var _ reconcile.LivenessProbe = fakeLiveness{}

// TestRunReconcile_ClosesMergedLandingIssue verifies runReconcile drives the
// reconcile.Run seam against a local-tracker config and reports the closed
// issue number in its output.
func TestRunReconcile_ClosesMergedLandingIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to mention closed issue 42, got %q", buf.String())
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestRunReconcile_ReportsAbandonedIssue verifies runReconcile reports an
// issue flagged abandoned (its landing PR closed without merging) in its
// output, distinct from a closed issue (ADR 0029).
func TestRunReconcile_ReportsAbandonedIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "abandoned") || !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to mention abandoned issue 42, got %q", buf.String())
	}
}

// TestRunReconcile_NonLocalTrackerIsClearNoOp verifies runReconcile refuses
// cleanly (a plain message, not an error) for github/jira, and never touches
// the forge even when a merged landing PR exists to close against.
func TestRunReconcile_NonLocalTrackerIsClearNoOp(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to do") {
		t.Errorf("want a clear no-op message, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}

// TestRunReconcile_ReportsResetIssue verifies runReconcile reports an
// InProgress issue that reconcile.Run reset, alongside the (empty) closed
// report, so an operator running `spindrift reconcile` sees which issues
// came back to Dispatchable.
func TestRunReconcile_ReportsResetIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, lp, "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "reset 1 issue(s): 42") {
		t.Errorf("want output to report reset issue 42, got %q", buf.String())
	}
}

// --- reconcileAfterDispatch tests (dispatch's local-only auto-invoke) ---

// TestReconcileAfterDispatch_LocalTracker_ClosesMergedLanding verifies a
// dispatch run's final auto-invoke reaches the same reconcile.Run seam
// runReconcile drives, when the tracker is local.
func TestReconcileAfterDispatch_LocalTracker_ClosesMergedLanding(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, "", &buf); err != nil {
		t.Fatalf("reconcileAfterDispatch: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestReconcileAfterDispatch_NonLocalTracker_SilentNoOp verifies a dispatch
// run's final auto-invoke does nothing — and prints nothing — for a
// github/jira tracker, unlike the standalone `spindrift reconcile` verb's
// explicit refusal message.
func TestReconcileAfterDispatch_NonLocalTracker_SilentNoOp(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, "", &buf); err != nil {
		t.Fatalf("reconcileAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output for a non-local tracker, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}

// --- surfaceAfterDispatch tests (CODE_FORGE=local's auto-surface exit, ADR 0033, issue #1730) ---

// writeSeamIssue writes a minimal local issue file named slug+".md" under
// dir, carrying parent and closed frontmatter fields — the shape
// surfaceAfterDispatch's SeamLister query reads.
func writeSeamIssue(t *testing.T, dir, slug, parent string, closed bool) {
	t.Helper()
	body := "---\ntitle: " + slug + "\nstate: agent-complete\nlabels: []\ncreated: 2026-07-09T12:00:00Z\nparent: " + parent + "\n"
	if closed {
		body += "closed: true\n"
	}
	body += "---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setGitIdentityEnv gives ambient git commands (forgetest.NewGitRepoFixture's
// own commits) a commit identity, mirroring the local package's own
// bundle_test.go helper of the same name.
func setGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}

// mustInitCheckout creates a plain git repo at dir, checked out on branch,
// with one commit — a bare-bones operator checkout standing in for pwd.
func mustInitCheckout(t *testing.T, dir, branch string) {
	t.Helper()
	mustRunGit(t, dir, "init", "-b", branch)
	mustRunGit(t, dir, "config", "user.email", "test@example.com")
	mustRunGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, "add", "base.txt")
	mustRunGit(t, dir, "commit", "-m", "base")
}

// TestSurfaceAfterDispatch_AllSeamsClosed_SurfacesBranch verifies
// surfaceAfterDispatch fetches the completed broad ticket's Integration
// branch into pwd as a local branch named after the ticket, and reports it
// through a one-line notice, once every one of its seam issues is closed
// (issue #1730 AC1, AC7).
func TestSurfaceAfterDispatch_AllSeamsClosed_SurfacesBranch(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(parent))
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)
	writeSeamIssue(t, issuesDir, "seam-2", parent, true)

	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, it, pwd, &buf); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), parent) {
		t.Errorf("want output to mention %s, got %q", parent, buf.String())
	}

	got := revParseTest(t, pwd, "refs/heads/"+parent)
	want := revParseTest(t, repo.Bare, "refs/heads/"+local.IntegrationBranch(parent))
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
}

// TestSurfaceAfterDispatch_OpenSeamRemains_NoOp verifies surfaceAfterDispatch
// does nothing — no branch, no notice — while any one of the ticket's seams
// is still open (issue #1730 AC3).
func TestSurfaceAfterDispatch_OpenSeamRemains_NoOp(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(parent))
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)
	writeSeamIssue(t, issuesDir, "seam-2", parent, false)

	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, it, pwd, &buf); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output while a seam is open, got %q", buf.String())
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent); err == nil {
		t.Errorf("refs/heads/%s exists, want no branch surfaced", parent)
	}
}

// TestSurfaceAfterDispatch_NonLocalCodeForge_NoOp verifies surfaceAfterDispatch
// does nothing for github/git codeForge, even with a local tracker and a
// configured parent — CODE_FORGE=local's Accumulation repo doesn't exist
// under any other codeForge, so there is nothing to surface from.
func TestSurfaceAfterDispatch_NonLocalCodeForge_NoOp(t *testing.T) {
	const parent = "1700"
	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)

	c := baseConfig()
	c.codeForge = "github"
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, it, "/nonexistent/pwd", &buf); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output for a non-local codeForge, got %q", buf.String())
	}
}

// TestSurfaceAfterDispatch_MixedParentBatch_SurfacesOnlyCompletedTickets
// verifies surfaceAfterDispatch iterates every distinct resolved parent
// among the tracker's issues (ADR 0033, issue #1734) — a mixed batch
// surfaces the broad ticket whose seams are all closed while leaving one
// with an open seam alone, instead of collapsing onto a single env-wide
// parent the way the removed CODE_FORGE_INTEGRATION_PARENT knob did.
func TestSurfaceAfterDispatch_MixedParentBatch_SurfacesOnlyCompletedTickets(t *testing.T) {
	setGitIdentityEnv(t)
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch("broad-a"))
	repo.SeedBranch(local.IntegrationBranch("broad-b"), "1")
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-a1", "broad-a", true)
	writeSeamIssue(t, issuesDir, "seam-a2", "broad-a", true)
	writeSeamIssue(t, issuesDir, "seam-b1", "broad-b", true)
	writeSeamIssue(t, issuesDir, "seam-b2", "broad-b", false)

	c := baseConfig()
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, it, pwd, &buf); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), "broad-a") {
		t.Errorf("want output to mention completed ticket broad-a, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "broad-b") {
		t.Errorf("want no mention of still-open ticket broad-b, got %q", buf.String())
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/broad-a"); err != nil {
		t.Errorf("refs/heads/broad-a missing, want it surfaced: %v", err)
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/broad-b"); err == nil {
		t.Error("refs/heads/broad-b exists, want no branch surfaced for the still-open ticket")
	}
}

// TestRunReconcile_ClosingLastSeamSurfacesIntegrationBranch verifies
// runReconcile's wiring end to end: closing a broad ticket's last open seam
// this very sweep — not just a ticket that was already fully closed coming
// in — surfaces the Integration branch into pwd in the same call (issue
// #1730 AC1), reported through the same writer reconcile's own messages go
// to.
func TestRunReconcile_ClosingLastSeamSurfacesIntegrationBranch(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(parent))
	repo.SeedBranch("agent/issue-42", "42")

	cf := local.NewLocalCodeForge(repo.Bare, local.IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-")
	if err := cf.Merge("agent/issue-42"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	landing, err := cf.(forge.LandingRef).LandingRef()
	if err != nil {
		t.Fatalf("LandingRef: %v", err)
	}

	issuesDir := t.TempDir()
	body := "---\ntitle: seam\nstate: agent-complete\nlabels: []\ncreated: 2026-07-09T12:00:00Z\nparent: " + parent + "\nlanding: " + landing + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(issuesDir, "42.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	c := baseConfig()
	c.issueTracker = "local"
	c.codeForge = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))

	var buf bytes.Buffer
	if err := runReconcile(c, it, cf, fakeLiveness{}, pwd, &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to report closed issue 42, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), parent) {
		t.Errorf("want output to report surfaced ticket %s, got %q", parent, buf.String())
	}

	got := revParseTest(t, pwd, "refs/heads/"+parent)
	want := revParseTest(t, repo.Bare, "refs/heads/"+local.IntegrationBranch(parent))
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
}

// revParseTest resolves ref inside the repo at dir, failing t on error.
func revParseTest(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v: %s", ref, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
