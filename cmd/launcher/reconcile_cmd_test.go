package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
)

// testCapabilities resolves capabilities the same way newReadContext does in
// production, keyed to c.codeForge and c.issueTracker (issue #2946, issue #3064).
func testCapabilities(t *testing.T, c config, cf forge.CodeForge, it forge.IssueTracker) forge.Capabilities {
	t.Helper()
	return forge.ResolveCapabilities(cf, it, mustDescriptor(t, c.codeForge), mustDescriptor(t, c.issueTracker))
}

func mustDescriptor(t *testing.T, name string) backend.Descriptor {
	t.Helper()
	d, ok := backend.ByName(name)
	if !ok {
		t.Fatalf("backend.ByName(%q): not found", name)
	}
	return d
}

// The tests below force caps toward the local branch regardless of the
// config's own backend name (issue #3064).
func localDescriptor(t *testing.T) backend.Descriptor {
	t.Helper()
	return mustDescriptor(t, "local")
}

// fakeLiveness is a no-op reconcile.LivenessProbe by default: LogStale reports
// not stale and ContainerLive always reports live=false, so it never triggers a
// reset on its own, which is what the Closed-only tests here need.
type fakeLiveness struct {
	stale     map[string]bool
	reachable map[string]bool
}

func (f fakeLiveness) LogStale(num string) bool { return f.stale[num] }

func (f fakeLiveness) ContainerLive(num string) (live, reachable bool) {
	return false, f.reachable[num]
}

var _ reconcile.LivenessProbe = fakeLiveness{}

func TestRunReconcile_ClosesMergedLandingIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, testCapabilities(t, c, f, f), "", &buf); err != nil {
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

// runReconcile reports an issue whose landing PR closed without merging as
// abandoned, distinct from a closed issue (ADR 0029).
func TestRunReconcile_ReportsAbandonedIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, testCapabilities(t, c, f, f), "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "abandoned") || !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to mention abandoned issue 42, got %q", buf.String())
	}
}

// For github or jira the refusal is a plain message, not an error, and the
// fixture still holds a merged landing PR so an accidental sweep would show up.
func TestRunReconcile_NonLocalTrackerIsClearNoOp(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, fakeLiveness{}, testCapabilities(t, c, f, f), "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to do") {
		t.Errorf("want a clear no-op message, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}

func TestRunReconcile_ReportsResetIssue(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	var buf bytes.Buffer
	if err := runReconcile(c, f, f, lp, testCapabilities(t, c, f, f), "", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "reset 1 issue(s): 42") {
		t.Errorf("want output to report reset issue 42, got %q", buf.String())
	}
}

func TestReconcileAfterDispatch_LocalTracker_ClosesMergedLanding(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, testCapabilities(t, c, f, f), "", &buf); err != nil {
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

// The dispatch auto-invoke prints nothing for a github or jira tracker, unlike
// the standalone `spindrift reconcile` verb's explicit refusal message.
func TestReconcileAfterDispatch_NonLocalTracker_SilentNoOp(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, testCapabilities(t, c, f, f), "", &buf); err != nil {
		t.Fatalf("reconcileAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output for a non-local tracker, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}

// Pins issue #3064 for both reconcile entry points: the guard must read
// caps.TrackerDescriptor, not look c.issueTracker up again. The fixture
// deliberately disagrees with itself, naming a non-local tracker in c while
// forcing caps.TrackerDescriptor to "local", so a guard reading c alone would
// say "no-op" and the sweep would not run.
func TestReconcileGuards_ReadTrackerDescriptorFromCaps(t *testing.T) {
	reconcileFuncs := []struct {
		name string
		fn   func(config, forge.IssueTracker, forge.CodeForge, reconcile.LivenessProbe, forge.Capabilities, string, io.Writer) error
	}{
		{"runReconcile", runReconcile},
		{"reconcileAfterDispatch", reconcileAfterDispatch},
	}
	for _, tt := range reconcileFuncs {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig()
			c.issueTracker = "github"
			f := forge.NewFake()
			f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
			f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

			caps := forge.ResolveCapabilities(f, f, backend.Descriptor{}, localDescriptor(t))

			var buf bytes.Buffer
			if err := tt.fn(c, f, f, fakeLiveness{}, caps, "", &buf); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if !strings.Contains(buf.String(), "42") {
				t.Errorf("want the sweep to run and report closed issue 42 (caps.TrackerDescriptor says in-box-unreachable), got %q", buf.String())
			}
			iss, err := f.Issue("42")
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if iss.State != forge.IssueClosed {
				t.Errorf("State = %v, want IssueClosed", iss.State)
			}
		})
	}
}

// writeSeamIssue writes the minimal local issue file shape that
// surfaceAfterDispatch's SeamLister query reads: a parent and a closed
// frontmatter field.
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

// setGitIdentityEnv gives forgetest.NewGitRepoFixture's own commits an
// identity, mirroring the local package's bundle_test.go helper of the same
// name.
func setGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}

// mustInitCheckout builds the bare-bones operator checkout that stands in for
// pwd in the surface tests.
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

type surfaceFixture struct {
	repo              *forgetest.GitRepoFixture
	pwd               string
	c                 config
	it                forge.IssueTracker
	lw                *localloop.Wired
	integrationBranch string
}

// The call is ResolveParent("", parent), not ResolveParent(parent, ""): parent
// stands in for the raw parent frontmatter value writeSeamIssue writes, not an
// issue's own number or slug, so it belongs in the rawParent argument.
func newSurfaceFixture(t *testing.T, parent, codeForge string) surfaceFixture {
	t.Helper()
	setGitIdentityEnv(t)
	sanitizedParent := local.ResolveParent("", parent)
	integrationBranch := local.IntegrationBranch(sanitizedParent)
	repo := forgetest.NewGitRepoFixture(t, integrationBranch)
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)
	writeSeamIssue(t, issuesDir, "seam-2", parent, true)

	c := baseConfig()
	c.codeForge = codeForge
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)
	return surfaceFixture{repo: repo, pwd: pwd, c: c, it: it, lw: lw, integrationBranch: integrationBranch}
}

// Issue #1730 AC1 and AC7: once every seam issue is closed, the Integration
// branch lands in pwd as a local branch named after the ticket.
func TestSurfaceAfterDispatch_AllSeamsClosed_SurfacesBranch(t *testing.T) {
	const parent = "1700"
	fx := newSurfaceFixture(t, parent, "local")

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(fx.c, fx.lw, testCapabilities(t, fx.c, nil, fx.it), fx.pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), parent) {
		t.Errorf("want output to mention %s, got %q", parent, buf.String())
	}

	got := revParseTest(t, fx.pwd, "refs/heads/"+parent)
	want := revParseTest(t, fx.repo.Bare, "refs/heads/"+fx.integrationBranch)
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
}

// Issue #1833: surfaceAfterDispatch must surface through the *localloop.Wired
// it is handed, not mint a fresh one from (c, it). c's AccumulationRepoDir
// points at a decoy repo while only the passed lw points at the repo carrying
// the ticket's Integration branch, so a surfaceAfterDispatch that builds its
// own Wired surfaces the decoy's tip and fails.
func TestSurfaceAfterDispatch_UsesPassedWiredInstance(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	sanitizedParent := local.ResolveParent("", parent)
	integrationBranch := local.IntegrationBranch(sanitizedParent)
	decoy := forgetest.NewGitRepoFixture(t, integrationBranch)
	real := forgetest.NewGitRepoFixture(t, integrationBranch)
	real.AdvanceBase()
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = decoy.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloop.Config{AccumulationRepoDir: real.Bare}, it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}

	got := revParseTest(t, pwd, "refs/heads/"+parent)
	want := revParseTest(t, real.Bare, "refs/heads/"+integrationBranch)
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (the passed *Wired's own Integration branch tip)", parent, got, want)
	}
	decoyTip := revParseTest(t, decoy.Bare, "refs/heads/"+integrationBranch)
	if got == decoyTip {
		t.Errorf("refs/heads/%s = decoy repo's tip %s, want surfaceAfterDispatch to ignore c's AccumulationRepoDir and use the passed *Wired's", parent, decoyTip)
	}
}

// Issue #1730 AC3: surfaceAfterDispatch surfaces no branch while any seam is
// still open, and issue #1811 requires a held verdict naming that seam rather
// than the earlier silent no-op.
func TestSurfaceAfterDispatch_OpenSeamRemains_PrintsHeldVerdict(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(local.ResolveParent("", parent)))
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)
	writeSeamIssue(t, issuesDir, "seam-2", parent, false)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	want := "surface: 1700 held — open seam #seam-2\n"
	if buf.String() != want {
		t.Errorf("surfaceAfterDispatch output = %q, want %q", buf.String(), want)
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent); err == nil {
		t.Errorf("refs/heads/%s exists, want no branch surfaced", parent)
	}
}

// CODE_FORGE=local's Accumulation repo does not exist under any other
// codeForge, so there is nothing to surface from even with a local tracker and
// a configured parent.
func TestSurfaceAfterDispatch_NonLocalCodeForge_NoOp(t *testing.T) {
	const parent = "1700"
	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-1", parent, true)

	c := baseConfig()
	c.codeForge = "github"
	c.issueTracker = "local"
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), "/nonexistent/pwd", &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output for a non-local codeForge, got %q", buf.String())
	}
}

// Pins issue #3064: the auto-surface guard must read caps.ForgeDescriptor, not
// look c.codeForge up again. The fixture names a non-local codeForge in c
// while forcing caps.ForgeDescriptor to "local", so a guard reading c alone
// would say "no-op" instead of surfacing the Integration branch.
func TestSurfaceAfterDispatch_ReadsForgeDescriptorFromCaps(t *testing.T) {
	const parent = "1700"
	fx := newSurfaceFixture(t, parent, "github")

	caps := forge.ResolveCapabilities(nil, fx.it, localDescriptor(t), backend.Descriptor{})

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(fx.c, fx.lw, caps, fx.pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), parent) {
		t.Errorf("want the sweep to run and report %s (caps.ForgeDescriptor says host-mediated), got %q", parent, buf.String())
	}
	got := revParseTest(t, fx.pwd, "refs/heads/"+parent)
	want := revParseTest(t, fx.repo.Bare, "refs/heads/"+fx.integrationBranch)
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
}

// ADR 0033 and issue #1734: the sweep iterates every distinct resolved parent
// among the tracker's issues instead of collapsing onto a single env-wide
// parent the way the removed CODE_FORGE_INTEGRATION_PARENT knob did. The batch
// mixes a fully closed ticket with one that still has an open seam so both
// outcomes have to appear in a single run.
func TestSurfaceAfterDispatch_MixedParentBatch_SurfacesOnlyCompletedTickets(t *testing.T) {
	setGitIdentityEnv(t)
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(local.ResolveParent("", "broad-a")))
	repo.SeedBranch(local.IntegrationBranch(local.ResolveParent("", "broad-b")), "1")
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-a1", "broad-a", true)
	writeSeamIssue(t, issuesDir, "seam-a2", "broad-a", true)
	writeSeamIssue(t, issuesDir, "seam-b1", "broad-b", true)
	writeSeamIssue(t, issuesDir, "seam-b2", "broad-b", false)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), "surface: broad-a surfaced") {
		t.Errorf("want output to mention completed ticket broad-a, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "surface: broad-b held — open seam #seam-b2") {
		t.Errorf("want a held verdict naming still-open ticket broad-b, got %q", buf.String())
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/broad-a"); err != nil {
		t.Errorf("refs/heads/broad-a missing, want it surfaced: %v", err)
	}
	if err := runGit(pwd, "rev-parse", "--verify", "--quiet", "refs/heads/broad-b"); err == nil {
		t.Error("refs/heads/broad-b exists, want no branch surfaced for the still-open ticket")
	}
}

// Issue #1734 review follow-up: one parent's SurfaceIntegrationBranch error
// must not abort the sweep. Leaving pwd un-inited makes every parent's current
// branch resolution fail the same way, so both names have to appear in the
// combined error for the sweep to have reached the second one.
func TestSurfaceAfterDispatch_OneParentErrors_StillAttemptsTheOthers(t *testing.T) {
	setGitIdentityEnv(t)
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(local.ResolveParent("", "broad-a")))
	repo.SeedBranch(local.IntegrationBranch(local.ResolveParent("", "broad-b")), "1")
	pwd := t.TempDir() // deliberately never git-inited

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "seam-a1", "broad-a", true)
	writeSeamIssue(t, issuesDir, "seam-b1", "broad-b", true)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil)
	if err == nil {
		t.Fatal("surfaceAfterDispatch: want an error since pwd is not a git repo, got nil")
	}
	if !strings.Contains(err.Error(), "broad-a") {
		t.Errorf("want the combined error to mention broad-a, got %q", err)
	}
	if !strings.Contains(err.Error(), "broad-b") {
		t.Errorf("want the combined error to mention broad-b too — the sweep must not stop after the first parent's error, got %q", err)
	}
}

// Issue #1730 AC1 end to end: the ticket's last seam closes during this very
// sweep, rather than arriving already fully closed, and the Integration branch
// still reaches pwd in the same call.
func TestRunReconcile_ClosingLastSeamSurfacesIntegrationBranch(t *testing.T) {
	setGitIdentityEnv(t)
	const parent = "1700"
	sanitizedParent := local.ResolveParent("", parent)
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(sanitizedParent))
	repo.SeedBranch("agent/issue-42", "42")

	cf := local.NewLocalCodeForge(repo.Bare, local.IntegrationBranch(sanitizedParent), sanitizedParent, "Test Bot", "bot@example.com", "agent/issue-")
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
	if err := runReconcile(c, it, cf, fakeLiveness{}, testCapabilities(t, c, cf, it), pwd, &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to report closed issue 42, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), parent) {
		t.Errorf("want output to report surfaced ticket %s, got %q", parent, buf.String())
	}

	got := revParseTest(t, pwd, "refs/heads/"+parent)
	want := revParseTest(t, repo.Bare, "refs/heads/"+local.IntegrationBranch(sanitizedParent))
	if got != want {
		t.Errorf("refs/heads/%s = %s, want %s (Integration branch tip)", parent, got, want)
	}
}

// Issue #1739: each closed parentless issue is its own singleton broad ticket
// (ADR 0033, issue #1734), so a tracker holding hundreds of standalone issues
// that never went through CODE_FORGE=local would print that many identical
// never-landed skip lines on every sweep. They collapse into one summary line.
func TestSurfaceAfterDispatch_ManyNeverLandedParents_CollapsesIntoOneSummaryLine(t *testing.T) {
	setGitIdentityEnv(t)
	repo := forgetest.NewGitRepoFixture(t, "main")
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "main")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "9001", "", true)
	writeSeamIssue(t, issuesDir, "9002", "", true)
	writeSeamIssue(t, issuesDir, "9003", "", true)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if got := strings.Count(buf.String(), "skipped"); got != 1 {
		t.Errorf("want exactly one summary line mentioning skipped parents, got %d occurrences in %q", got, buf.String())
	}
	want := "surface: 3 broad ticket(s) skipped — no seam has landed yet\n"
	if buf.String() != want {
		t.Errorf("surfaceAfterDispatch output = %q, want %q", buf.String(), want)
	}
}

// Issue #1739: the collapse is specific to the permanent never-landed skip. A
// parent skipped because it is currently checked out in pwd is transient and
// the operator can act on it, so it keeps its own line.
func TestSurfaceAfterDispatch_NeverLandedAndCheckedOut_OnlyNeverLandedCollapses(t *testing.T) {
	setGitIdentityEnv(t)
	repo := forgetest.NewGitRepoFixture(t, local.IntegrationBranch(local.ResolveParent("9010", "")))
	pwd := t.TempDir()
	mustInitCheckout(t, pwd, "9010")

	issuesDir := t.TempDir()
	writeSeamIssue(t, issuesDir, "9001", "", true)
	writeSeamIssue(t, issuesDir, "9002", "", true)
	writeSeamIssue(t, issuesDir, "9010", "", true)

	c := baseConfig()
	c.codeForge = "local"
	c.issueTracker = "local"
	c.codeForgeAccumulationRepoDir = repo.Bare
	it := local.NewLocalTracker(issuesDir, dispatchLabels(c))
	lw := localloop.Wire(localloopConfig(c), it)

	var buf bytes.Buffer
	if err := surfaceAfterDispatch(c, lw, testCapabilities(t, c, nil, it), pwd, &buf, nil); err != nil {
		t.Fatalf("surfaceAfterDispatch: %v", err)
	}
	if !strings.Contains(buf.String(), "surface: 9010 held — 9010 is currently checked out\n") {
		t.Errorf("want 9010's checked-out held verdict reported on its own line, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "surface: 2 broad ticket(s) skipped — no seam has landed yet\n") {
		t.Errorf("want the two never-landed parents collapsed into one summary line, got %q", buf.String())
	}
	if got := strings.Count(buf.String(), "\n"); got != 2 {
		t.Errorf("want exactly two lines total (one checked-out, one summary), got %d in %q", got, buf.String())
	}
}

func revParseTest(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v: %s", ref, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
