package localloop_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

const testBaseBranch = "main"

var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Recoverable:  "agent-recoverable",
}

func setGitIdentityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v: %s", args, dir, err, out)
	}
	return string(out)
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, "rev-parse", ref))
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newOperatorCheckout builds the operator's local working directory. It
// deliberately configures no remote, mirroring how an operator's own checkout
// has none pointing at the Accumulation repo.
func newOperatorCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", testBaseBranch)
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	run(t, dir, "add", "base.txt")
	run(t, dir, "commit", "-m", "base")
	return dir
}

// writeLocalIssue writes num's issue file in the local tracker's frontmatter
// grammar (ADR 0013). The test writes the file itself because LocalTracker has
// no issue-creation API.
func writeLocalIssue(t *testing.T, dir, num, title, parent, state string) {
	t.Helper()
	writeLocalIssueBody(t, dir, num, title, parent, state, "body\n")
}

// writeLocalIssueWithBlocker adds a "## Blocked by" section naming blockerNum
// by its filename slug. That body-sourced grammar (forge.DepSourceBody) is the
// only way the local tracker records a blocker, so DepsOf(num) reads it there.
func writeLocalIssueWithBlocker(t *testing.T, dir, num, title, parent, state, blockerNum string) {
	t.Helper()
	writeLocalIssueBody(t, dir, num, title, parent, state, "body\n\n## Blocked by\n- "+blockerNum+"\n")
}

func writeLocalIssueBody(t *testing.T, dir, num, title, parent, state, body string) {
	t.Helper()
	writeLocalIssueBodyAt(t, dir, num, title, parent, state, body, time.Now())
}

// writeLocalIssueBodyAt takes an explicit created: timestamp for tests that
// need a deterministic created-ascending order. Successive time.Now() calls
// tie at RFC3339 second granularity, and AllIssues then falls back to
// sort.SliceStable's directory-order tiebreak.
func writeLocalIssueBodyAt(t *testing.T, dir, num, title, parent, state, body string, created time.Time) {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "state: %s\n", state)
	b.WriteString("labels: []\n")
	fmt.Fprintf(&b, "created: %s\n", created.Format(time.RFC3339))
	if parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", parent)
	}
	b.WriteString("---\n")
	b.WriteString(body)
	writeFile(t, filepath.Join(dir, num+".md"), b.String())
}

// bundleFixtureCommit stands in for the Agent's "commit on the agent branch"
// contract under CODE_FORGE=local (issue #1808). It bundles through the real
// producer, bundleout.Run, rather than a hand-written `git bundle create`, so
// RelayBundle sees exactly what a real Box's code-out leaves behind.
func bundleFixtureCommit(t *testing.T, accumDir, base, branch, num, outboxDir string) string {
	t.Helper()
	work := t.TempDir()
	run(t, "", "clone", accumDir, work)
	run(t, work, "checkout", base)
	run(t, work, "checkout", "-b", branch)
	writeFile(t, filepath.Join(work, "feature-"+num+".txt"), "feature\n")
	run(t, work, "add", "feature-"+num+".txt")
	run(t, work, "commit", "-m", "feature "+num)
	sha := revParse(t, work, "HEAD")
	priorLine := outcome.Outcome{Issue: num, Landing: branch, Status: "ready"}.Line()
	if err := bundleout.Run(bundleout.Config{
		Repo:             work,
		Base:             base,
		Branch:           branch,
		OutboxDir:        outboxDir,
		Issue:            num,
		PriorOutcomeLine: priorLine,
	}, io.Discard); err != nil {
		t.Fatalf("bundleout.Run: %v", err)
	}
	return sha
}

// A failed IssueTracker lookup falls back to num's own sanitized slug, the
// same posture an issue with no parent: gets. Callers like BASE_BRANCH
// forwarding have a func(string) string shape with no way to return an error.
func TestResolveParent_IssueLookupError_FallsBackToOwnSlug(t *testing.T) {
	fc := forge.NewFake()
	fc.IssueErr = errors.New("issue file unreadable")

	if got, want := localloop.ResolveParent(fc, "Broad Ticket").String(), "broad-ticket"; got != want {
		t.Errorf("ResolveParent = %q, want %q", got, want)
	}
}

// Wire resolves each issue's parent exactly once (issue #1810), so the forge
// constructor, base-branch resolver, and surface grouping sharing one *Wired
// reuse that resolution instead of each re-deriving it.
func TestWired_ResolveParent_MemoizesPerIssue(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Parent: "Calc Engine"})

	lw := localloop.Wire(localloop.Config{}, fc)
	first := lw.ResolveParent("42")
	second := lw.ResolveParent("42")

	if first != second {
		t.Errorf("ResolveParent(42) = %v then %v, want the same resolved value", first, second)
	}
	if got := len(fc.IssueCalls); got != 1 {
		t.Errorf("IssueCalls = %v (%d calls), want exactly 1 -- ResolveParent must resolve issue 42's parent once, not on every call", fc.IssueCalls, got)
	}
}

// SeedScopeOf pairs the sanitized parent with the rendered Integration branch
// label in one forge.SeedScope that both the dispatch command path and the
// Console consume (issue #2150), so the two cannot disagree about which
// blocker landing gates a dependent.
func TestSeedScopeOf_PairsSanitizedParentWithIntegrationLabel(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", Parent: "Render Pipeline"})

	if got, want := localloop.SeedScopeOf(fc, "11").String(), "integration/render-pipeline"; got != want {
		t.Errorf("SeedScopeOf(11).String() = %q, want %q", got, want)
	}
}

// Every forge but local lacks forge.LandingContainmentQuery, so
// waves.Config.SeedScopeOf stays nil, the seed-branch containment check
// (#2130) never fires, and a blocker is judged solely by its PR/issue state.
func TestSeedScopeResolver_NonLocalForge_ReturnsNil(t *testing.T) {
	fc := forge.NewFake()
	caps := forge.ResolveCapabilities(fc, fc, backend.Descriptor{}, backend.Descriptor{})
	if got := localloop.SeedScopeResolver(fc, caps); got != nil {
		t.Error("SeedScopeResolver(non-containment forge) returned non-nil, want nil")
	}
}

func TestSeedScopeResolver_LocalForge_ResolvesDependentsParent(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", Parent: "Render Pipeline"})
	cf := fc.AsLocal()
	caps := forge.ResolveCapabilities(cf, fc, backend.Descriptor{}, backend.Descriptor{})
	resolve := localloop.SeedScopeResolver(fc, caps)
	if resolve == nil {
		t.Fatal("SeedScopeResolver(local forge) = nil, want a non-nil resolver")
	}
	if got := resolve("11").String(); got != "integration/render-pipeline" {
		t.Errorf("resolve(11) = %q, want %q", got, "integration/render-pipeline")
	}
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// One seam end to end through localloop.Wire's own wiring, exactly as
// production runs it: a real bundle, a real settle, a real reconcile, and a
// real surface (issue #1806 AC2/AC3).
func TestWire_ComposedLoop_HappyPath(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "42"
	writeLocalIssue(t, issuesDir, num, "seam 42", "", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	parent := lw.ResolveParent(num)
	if parent.String() != num {
		t.Fatalf("ResolveParent(%s) = %q, want %q (parentless seam is its own broad ticket)", num, parent, num)
	}
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)

	fixtureSHA := bundleFixtureCommit(t, accumDir, testBaseBranch, branch, num, lw.OutboxDir(num))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	iss, err := it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if !containsLabel(iss.Labels, testLabels.Complete) {
		t.Fatalf("issue %s labels = %v, want %s after settle", num, iss.Labels, testLabels.Complete)
	}

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	// With no parent: frontmatter, the sanitized title names the surfaced
	// branch (issue #1811), not the slug ResolveParent keyed the Integration
	// branch on.
	const wantBranch = "seam-42"
	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent.String() + " surfaced → branch " + wantBranch + " (1 seams)"
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}

	surfacedTip := revParse(t, operatorDir, "refs/heads/"+wantBranch)
	wantTip := revParse(t, accumDir, "refs/heads/"+local.IntegrationBranch(parent))
	if surfacedTip != wantTip {
		t.Errorf("surfaced branch %s tip = %s, want %s (Integration branch tip)", wantBranch, surfacedTip, wantTip)
	}
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", fixtureSHA, "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("fixture commit %s not reachable from surfaced branch %s", fixtureSHA, wantBranch)
	}
}

// A title made entirely of characters SanitizeParent strips must not surface
// an empty-string branch name. Surface falls back to the ticket's own slug
// (issue #1811 AC3).
func TestWire_ComposedLoop_EmptyTitleSanitizesToSlug(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "47"
	writeLocalIssue(t, issuesDir, num, "!!!", "", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	parent := lw.ResolveParent(num)
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branch, num, lw.OutboxDir(num))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent.String() + " surfaced → branch " + parent.String() + " (1 seams)"
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}
	if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent.String()).Run(); err != nil {
		t.Errorf("refs/heads/%s missing — want the slug fallback branch surfaced", parent)
	}
}

// A parent: field that sanitizes to empty must name its branch like an unset
// parent. local.ResolveParent already folds it into its own broad ticket (ADR
// 0033, issue #1734), so Surface's title-derived naming (issue #1811) has to
// recognize it too rather than only testing the raw parent: string for "".
func TestWire_ComposedLoop_GarbageParentUsesTitleNaming(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "48"
	writeLocalIssue(t, issuesDir, num, "seam 48", "!!!", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	parent := lw.ResolveParent(num)
	if parent.String() != num {
		t.Fatalf("ResolveParent(%s) = %q, want %q (a garbage parent: is its own broad ticket)", num, parent, num)
	}
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branch, num, lw.OutboxDir(num))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	const wantBranch = "seam-48"
	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent.String() + " surfaced → branch " + wantBranch + " (1 seams)"
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}
	if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("refs/heads/%s missing — want the title-derived branch surfaced", wantBranch)
	}
}

// Reconcile's healing path (issue #1809): a branch merged cleanly but left
// with a raw pre-merge landing, as if settle's post-merge LandingRef upgrade
// never ran. The next sweep sees the branch is an ancestor of the Integration
// branch, upgrades the landing, and closes the seam instead of leaving it
// stuck open.
func TestWire_ComposedLoop_HealsStuckBranchRefLanding(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "46"
	writeLocalIssue(t, issuesDir, num, "seam 46", "", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	parent := lw.ResolveParent(num)
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branch, num, lw.OutboxDir(num))

	// Relay and merge directly through cf, standing in for settle's
	// mergeImmediate having already succeeded, then record only the raw branch
	// as the landing. That sabotages exactly the post-merge upgrade step issue
	// #1809 heals.
	if err := cf.(forge.BundleRelay).RelayBundle(lw.OutboxDir(num), branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := it.RecordLanding(num, branch); err != nil {
		t.Fatalf("RecordLanding: %v", err)
	}

	res, err := reconcile.Run(it, cf, nil, forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}), func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	iss, err := it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if iss.State != forge.IssueClosed {
		t.Fatalf("issue %s state = %v, want IssueClosed", num, iss.State)
	}
	wantPrefix := local.IntegrationBranch(parent) + "@"
	if !strings.HasPrefix(iss.Landing, wantPrefix) {
		t.Errorf("issue %s landing = %q, want it upgraded to %q<sha>", num, iss.Landing, wantPrefix)
	}
}

// The Agent produced nothing, so settle's relay fails and the seam blocks as
// agent-complete, never agent-failed (ADR 0033). Reconcile then reports it
// stuck (issue #1809) and Surface holds the broad ticket rather than
// surfacing it (issue #1806 AC4, issue #1811).
func TestWire_ComposedLoop_MissingBundleBlocksNotFailed(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "43"
	writeLocalIssue(t, issuesDir, num, "seam 43", "", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)
	parent := lw.ResolveParent(num)
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)

	// No bundleFixtureCommit call: the outbox stays empty, standing in for an
	// Agent that produced no code-out.

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	iss, err := it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if !containsLabel(iss.Labels, testLabels.Complete) {
		t.Fatalf("issue %s labels = %v, want %s (blocked stays agent-complete)", num, iss.Labels, testLabels.Complete)
	}
	if containsLabel(iss.Labels, testLabels.Failed) {
		t.Fatalf("issue %s labels = %v, must NOT carry %s after a blocked relay", num, iss.Labels, testLabels.Failed)
	}

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Fatalf("reconcile.Run closed = %v, want none (landing never verified)", res.Closed)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent.String() + " held — stuck landing — branch " + branch + " not merged into " + local.IntegrationBranch(parent)
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}
	if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent.String()).Run(); err == nil {
		t.Errorf("refs/heads/%s must not exist — parent's only seam never landed", parent)
	}
}

// ADR 0039's local push-only recovery path (issue #2254): a Box that emitted
// no parseable outcome line but left a success self-report and a real bundle
// must not park agent-failed. tryMarkRecoverable promotes it to Recoverable,
// and only SettleRelayedBranch, the method recoverByNumber drives, actually
// lands the bundle.
func TestWire_ComposedLoop_NoOutcomeBundlePresentRecoversAndLands(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const num = "70"
	writeLocalIssue(t, issuesDir, num, "seam 70", "", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	parent := lw.ResolveParent(num)
	cf := lw.CodeForgeForIssue(num)
	branch := cf.AgentBranch(num)

	// The fixture stands in for a Box that finished its work and relayed it to
	// the outbox, but whose final print was cut short and left no parseable
	// outcome line.
	fixtureSHA := bundleFixtureCommit(t, accumDir, testBaseBranch, branch, num, lw.OutboxDir(num))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	iss, err := it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if !containsLabel(iss.Labels, testLabels.Recoverable) {
		t.Fatalf("issue %s labels = %v, want %s after settle with no outcome line + bundle present", num, iss.Labels, testLabels.Recoverable)
	}
	if containsLabel(iss.Labels, testLabels.Failed) {
		t.Fatalf("issue %s labels = %v, must NOT carry %s -- a recoverable bundle is not a failure", num, iss.Labels, testLabels.Failed)
	}
	if containsLabel(iss.Labels, testLabels.Complete) {
		t.Fatalf("issue %s labels = %v, must NOT carry %s yet -- the branch has not landed until recover runs", num, iss.Labels, testLabels.Complete)
	}

	// The recover path drives the same Settler method and dispatch.Result shape
	// recoverByNumber (main.go) builds from the driver's last self-report in
	// the pass logs.
	recoverResult := dispatch.Result{Resolved: outcome.Resolved{SelfReport: outcome.SelfReport{Status: "ready"}, SelfReportFound: true}}
	sit := s.SituationFor(num, false, recoverResult)
	if !s.SettleRelayedBranch(dispatch.NewFake(), num, 0, sit, recoverResult) {
		t.Fatalf("SettleRelayedBranch(%s) = false, want true", num)
	}

	iss, err = it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if !containsLabel(iss.Labels, testLabels.Complete) {
		t.Fatalf("issue %s labels = %v, want %s after recover lands it", num, iss.Labels, testLabels.Complete)
	}
	if containsLabel(iss.Labels, testLabels.Failed) {
		t.Fatalf("issue %s labels = %v, must NOT carry %s after a successful recover", num, iss.Labels, testLabels.Failed)
	}

	integ := local.IntegrationBranch(parent)
	if err := exec.Command("git", "-C", accumDir, "merge-base", "--is-ancestor", fixtureSHA, "refs/heads/"+integ).Run(); err != nil {
		t.Errorf("fixture commit %s not reachable from Integration branch %s after recover", fixtureSHA, integ)
	}

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}
}

// With one sibling still open, Surface must not publish the parent's
// Integration branch into the operator's checkout, even though that branch
// already exists in the Accumulation repo (issue #1806 AC4).
func TestWire_ComposedLoop_OneOpenSiblingNotSurfaced(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const parent = "1700"
	const landedNum = "44"
	const openNum = "45"
	writeLocalIssue(t, issuesDir, landedNum, "seam 44", parent, testLabels.InProgress)
	writeLocalIssue(t, issuesDir, openNum, "seam 45", parent, testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)
	sanitizedParent := lw.ResolveParent(landedNum)
	if got := sanitizedParent.String(); got != parent {
		t.Fatalf("ResolveParent(%s) = %q, want %q", landedNum, got, parent)
	}

	cf := lw.CodeForgeForIssue(landedNum)
	branch := cf.AgentBranch(landedNum)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branch, landedNum, lw.OutboxDir(landedNum))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: landedNum, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), landedNum, 0, result)

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != landedNum {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, landedNum)
	}

	// Confirm the Integration branch landed, so the assertion below tests the
	// sibling-open gate rather than a "never landed" false negative.
	if err := exec.Command("git", "-C", accumDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+local.IntegrationBranch(sanitizedParent)).Run(); err != nil {
		t.Fatalf("Integration branch %s missing from Accumulation repo after landedNum settled", local.IntegrationBranch(sanitizedParent))
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent + " held — open seam #" + openNum
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}
	if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent).Run(); err == nil {
		t.Errorf("refs/heads/%s must not exist — sibling %s is still open", parent, openNum)
	}
}

// A broad ticket that is itself an issue in the tracker must never count as
// one of its own seams. Both created: orderings are covered because issue
// #3439's AC2 says the exclusion holds regardless of that ordering.
func TestWire_ComposedLoop_BroadTicketIssueExcludedFromOwnSeams(t *testing.T) {
	cases := []struct {
		name       string
		broadFirst bool
	}{
		{"broad ticket created before its seam", true},
		{"seam created before its broad-ticket issue", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGitIdentityEnv(t)
			operatorDir := newOperatorCheckout(t)
			t.Chdir(operatorDir)

			accumDir := filepath.Join(t.TempDir(), "accum.git")
			if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
				t.Fatalf("SeedAccumulationRepo: %v", err)
			}

			issuesDir := t.TempDir()
			it := local.NewLocalTracker(issuesDir, testLabels)
			const parent = "1700"
			const seamNum = "44"
			base := time.Now()
			broadCreated, seamCreated := base, base.Add(time.Second)
			if !tc.broadFirst {
				broadCreated, seamCreated = base.Add(time.Second), base
			}
			writeLocalIssueBodyAt(t, issuesDir, parent, "Broad Ticket 1700", "", testLabels.InProgress, "body\n", broadCreated)
			writeLocalIssueBodyAt(t, issuesDir, seamNum, "seam 44", parent, testLabels.InProgress, "body\n", seamCreated)

			lw := localloop.Wire(localloop.Config{
				AccumulationRepoDir: accumDir,
				BaseBranch:          testBaseBranch,
				GitUserName:         "Test Bot",
				GitUserEmail:        "bot@example.com",
				BranchPrefix:        "agent/issue-",
			}, it)
			if got := lw.ResolveParent(seamNum).String(); got != parent {
				t.Fatalf("ResolveParent(%s) = %q, want %q", seamNum, got, parent)
			}

			cf := lw.CodeForgeForIssue(seamNum)
			branch := cf.AgentBranch(seamNum)
			bundleFixtureCommit(t, accumDir, testBaseBranch, branch, seamNum, lw.OutboxDir(seamNum))

			cfg := settle.Config{
				MergeMode:         "immediate",
				CompleteLabel:     testLabels.Complete,
				OutboxDir:         lw.OutboxDir,
				CodeForgeForIssue: lw.CodeForgeForIssue,
				Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
			}
			s := settle.New(cfg, it, cf)
			result := dispatch.Result{
				Success: true,
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: seamNum, Landing: branch, Status: "ready"},
				},
			}
			s.Settle(dispatch.NewFake(), seamNum, 0, result)

			res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
				p := lw.ResolveParent(num)
				return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
			})
			if err != nil {
				t.Fatalf("reconcile.Run: %v", err)
			}
			if len(res.Closed) != 1 || res.Closed[0] != seamNum {
				t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, seamNum)
			}

			var out strings.Builder
			if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
				t.Fatalf("Surface: %v", err)
			}
			wantVerdict := "surface: " + parent + " surfaced → branch " + parent + " (1 seams)"
			if !strings.Contains(out.String(), wantVerdict) {
				t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
			}
			if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent).Run(); err != nil {
				t.Errorf("refs/heads/%s must exist after the group's only real seam closed", parent)
			}

			broadTicket, err := it.Issue(parent)
			if err != nil {
				t.Fatalf("it.Issue(%s): %v", parent, err)
			}
			if broadTicket.State != forge.IssueOpen {
				t.Errorf("broad-ticket issue %s state = %v, want IssueOpen -- reconcile must never close a broad-ticket issue", parent, broadTicket.State)
			}
		})
	}
}

// A broad ticket that has a real seam AND was itself dispatched with a
// landing that goes stuck must still surface: #3439's exclusion drops the
// broad ticket from its own seams group (the group keeps a real seam, so
// #3439's drop-none fallback does not apply), so verdictFor's
// stuck[s.Number] check in the per-seam loop never sees it, even though
// reconcile — which walks every open issue, exclusion or not — reports it
// stuck (issue #3440).
func TestWire_ComposedLoop_StuckBroadTicketLandingSurfacesAnyway(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const parent = "1701"
	const seamNum = "45"
	writeLocalIssue(t, issuesDir, parent, "Broad Ticket 1701", "", testLabels.InProgress)
	writeLocalIssue(t, issuesDir, seamNum, "seam 45", parent, testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(lw.CodeForgeForIssue(seamNum), it, backend.Descriptor{}, backend.Descriptor{}),
	}

	// The seam lands normally: bundle it, then settle.
	seamCF := lw.CodeForgeForIssue(seamNum)
	seamBranch := seamCF.AgentBranch(seamNum)
	bundleFixtureCommit(t, accumDir, testBaseBranch, seamBranch, seamNum, lw.OutboxDir(seamNum))
	settle.New(cfg, it, seamCF).Settle(dispatch.NewFake(), seamNum, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: seamNum, Landing: seamBranch, Status: "ready"},
		},
	})

	// The broad ticket's own landing goes stuck: no bundleFixtureCommit call,
	// so the outbox stays empty and settle's relay fails (mirrors
	// TestWire_ComposedLoop_MissingBundleBlocksNotFailed).
	broadCF := lw.CodeForgeForIssue(parent)
	broadBranch := broadCF.AgentBranch(parent)
	settle.New(cfg, it, broadCF).Settle(dispatch.NewFake(), parent, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: parent, Landing: broadBranch, Status: "ready"},
		},
	})

	res, err := reconcile.Run(it, seamCF, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if got, ok := res.Stuck[parent]; !ok || got != broadBranch {
		t.Fatalf("reconcile.Run Stuck[%s] = %q, %v, want %q, true -- the broad ticket's own stuck landing must be visible to reconcile", parent, got, ok, broadBranch)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	// Scope both assertions to the broad ticket's own verdict line: a
	// whole-output grep for "stuck landing" would also trip on some other
	// group's held verdict, which says nothing about this exclusion.
	var parentLine string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "surface: "+parent+" ") {
			parentLine = line
			break
		}
	}
	wantVerdict := "surface: " + parent + " surfaced → branch " + parent + " (1 seams)"
	if !strings.Contains(parentLine, wantVerdict) {
		t.Errorf("Surface verdict line for %s = %q, want it to contain %q -- full output %q", parent, parentLine, wantVerdict, out.String())
	}
	if strings.Contains(parentLine, "stuck landing") {
		t.Errorf("Surface verdict line for %s = %q, must not contain %q -- the broad ticket's own stuck landing is excluded from its seams group", parent, parentLine, "stuck landing")
	}
	if err := exec.Command("git", "-C", operatorDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+parent).Run(); err != nil {
		t.Errorf("refs/heads/%s must exist -- the group's only real seam closed", parent)
	}
}

// Two parentless issues whose filenames sanitize to the same token collide
// into one group the exclusion pass would otherwise empty. Surface's
// len(kept) > 0 guard keeps both, so the group still gates on an open member
// and surfaces only once both close (issue #3439).
func TestWire_ComposedLoop_DegenerateAllMembersCollide_KeepsBothMembers(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	// SanitizeParent maps any run of non-[a-z0-9] to a single dash, so these
	// two distinct issue files land on the same key and each becomes the
	// other's whole group. The underscore replaces the docs' illustrative
	// space because AgentBranch appends num unsanitized and git rejects a ref
	// containing a space.
	const numA = "foo_bar"
	const numB = "foo-bar"
	const wantParent = "foo-bar"
	base := time.Now()
	writeLocalIssueBodyAt(t, issuesDir, numA, "Alpha", "", testLabels.InProgress, "body\n", base)
	writeLocalIssueBodyAt(t, issuesDir, numB, "Beta", "", testLabels.InProgress, "body\n", base.Add(time.Second))

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)
	if got := lw.ResolveParent(numA).String(); got != wantParent {
		t.Fatalf("ResolveParent(%s) = %q, want %q", numA, got, wantParent)
	}
	if got := lw.ResolveParent(numB).String(); got != wantParent {
		t.Fatalf("ResolveParent(%s) = %q, want %q", numB, got, wantParent)
	}

	cf := lw.CodeForgeForIssue(numA)
	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)

	// Land numA only. With numB still open, the group must gate on numB rather
	// than surfacing on a wrongly emptied member list.
	branchA := cf.AgentBranch(numA)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branchA, numA, lw.OutboxDir(numA))
	s.Settle(dispatch.NewFake(), numA, 0, dispatch.Result{
		Success:  true,
		Resolved: outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: numA, Landing: branchA, Status: "ready"}},
	})
	scopeFor := func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	}
	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, scopeFor)
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != numA {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, numA)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantHeld := "surface: " + wantParent + " held — open seam #" + numB
	if !strings.Contains(out.String(), wantHeld) {
		t.Errorf("Surface output = %q, want it to contain %q -- the still-open colliding member must still gate the group", out.String(), wantHeld)
	}

	// Closing numB too shows the guard kept both members: the group is
	// satisfied only once both colliding members close.
	branchB := cf.AgentBranch(numB)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branchB, numB, lw.OutboxDir(numB))
	s.Settle(dispatch.NewFake(), numB, 0, dispatch.Result{
		Success:  true,
		Resolved: outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: numB, Landing: branchB, Status: "ready"}},
	})
	res, err = reconcile.Run(it, cf, nil, cfg.Capabilities, scopeFor)
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != numB {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, numB)
	}

	out.Reset()
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	// The branch name comes from the first-created colliding member's title,
	// not from wantParent: the guard's restored g.issues stays parentless, so
	// verdictFor takes its title-derivation branch.
	wantSurfaced := "surface: " + wantParent + " surfaced → branch alpha (2 seams)"
	if !strings.Contains(out.String(), wantSurfaced) {
		t.Errorf("Surface output = %q, want it to contain %q -- neither colliding member was dropped", out.String(), wantSurfaced)
	}
}

// With a broad-ticket issue in the group, the held verdict must name an open
// real seam. The broad ticket's own open state must never surface as an open
// seam (issue #3439).
func TestWire_ComposedLoop_BroadTicketIssuePresent_OpenSeamNamesSeam(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const parent = "1700"
	const landedNum = "44"
	const openNum = "45"
	base := time.Now()
	writeLocalIssueBodyAt(t, issuesDir, parent, "Broad Ticket 1700", "", testLabels.InProgress, "body\n", base)
	writeLocalIssueBodyAt(t, issuesDir, landedNum, "seam 44", parent, testLabels.InProgress, "body\n", base.Add(time.Second))
	writeLocalIssueBodyAt(t, issuesDir, openNum, "seam 45", parent, testLabels.InProgress, "body\n", base.Add(2*time.Second))

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	cf := lw.CodeForgeForIssue(landedNum)
	branch := cf.AgentBranch(landedNum)
	bundleFixtureCommit(t, accumDir, testBaseBranch, branch, landedNum, lw.OutboxDir(landedNum))

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: landedNum, Landing: branch, Status: "ready"},
		},
	}
	s.Settle(dispatch.NewFake(), landedNum, 0, result)

	res, err := reconcile.Run(it, cf, nil, cfg.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != landedNum {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, landedNum)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + parent + " held — open seam #" + openNum
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q", out.String(), wantVerdict)
	}
	unwanted := "open seam #" + parent
	if strings.Contains(out.String(), unwanted) {
		t.Errorf("Surface output = %q, must never name the broad-ticket issue itself as an open seam", out.String())
	}
}

// A middle issue still gates its grandparent's group even though a third
// issue names it as a parent. The exclusion is scoped to an issue whose
// resolved key equals its own sanitized slug, never to anything some other
// issue names as a parent (issue #3439).
func TestWire_ComposedLoop_ThreeLevelChain_MiddleIssueGatesGrandparent(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const grandparent = "1800"
	const middle = "44"
	const leaf = "45"
	base := time.Now()
	writeLocalIssueBodyAt(t, issuesDir, grandparent, "Grandparent 1800", "", testLabels.InProgress, "body\n", base)
	writeLocalIssueBodyAt(t, issuesDir, middle, "middle 44", grandparent, testLabels.InProgress, "body\n", base.Add(time.Second))
	writeLocalIssueBodyAt(t, issuesDir, leaf, "leaf 45", middle, testLabels.InProgress, "body\n", base.Add(2*time.Second))

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	cf := lw.CodeForgeForIssue(middle)
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, nil, caps); err != nil {
		t.Fatalf("Surface: %v", err)
	}
	wantVerdict := "surface: " + grandparent + " held — open seam #" + middle
	if !strings.Contains(out.String(), wantVerdict) {
		t.Errorf("Surface output = %q, want it to contain %q -- the still-open middle issue must gate its grandparent's group", out.String(), wantVerdict)
	}
	unwanted := "open seam #" + grandparent
	if strings.Contains(out.String(), unwanted) {
		t.Errorf("Surface output = %q, must never name the grandparent broad-ticket issue itself as an open seam", out.String())
	}
}

// Two seams with distinct parents through one *Wired (issue #1810 AC4): each
// must land and surface onto its own Integration branch rather than
// collapsing onto a single one. TestWired_ResolveParent_MemoizesPerIssue
// covers the resolved-exactly-once guarantee itself.
func TestWire_ComposedLoop_MixedParentBatch_EachOwnIntegrationBranch(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const numA, numB = "60", "61"
	writeLocalIssue(t, issuesDir, numA, "seam 60", "Calc Engine", testLabels.InProgress)
	writeLocalIssue(t, issuesDir, numB, "seam 61", "Render Pipeline", testLabels.InProgress)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	for num, want := range map[string]string{numA: "calc-engine", numB: "render-pipeline"} {
		if got := lw.ResolveParent(num).String(); got != want {
			t.Fatalf("ResolveParent(%s) = %q, want %q", num, got, want)
		}
	}

	cfA, cfB := lw.CodeForgeForIssue(numA), lw.CodeForgeForIssue(numB)
	branchA, branchB := cfA.AgentBranch(numA), cfB.AgentBranch(numB)
	fixtureShaA := bundleFixtureCommit(t, accumDir, testBaseBranch, branchA, numA, lw.OutboxDir(numA))
	fixtureShaB := bundleFixtureCommit(t, accumDir, testBaseBranch, branchB, numB, lw.OutboxDir(numB))

	cfgA := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cfA, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	sA := settle.New(cfgA, it, cfA)
	sA.Settle(dispatch.NewFake(), numA, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: numA, Landing: branchA, Status: "ready"},
		},
	})
	cfgB := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cfB, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	sB := settle.New(cfgB, it, cfB)
	sB.Settle(dispatch.NewFake(), numB, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: numB, Landing: branchB, Status: "ready"},
		},
	})

	res, err := reconcile.Run(it, cfA, nil, cfgA.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 2 {
		t.Fatalf("reconcile.Run closed = %v, want both %s and %s", res.Closed, numA, numB)
	}

	if err := lw.Surface(operatorDir, io.Discard, res.Stuck, cfgA.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}

	for num, branch := range map[string]string{"calc-engine": numA, "render-pipeline": numB} {
		surfacedTip := revParse(t, operatorDir, "refs/heads/"+num)
		wantTip := revParse(t, accumDir, "refs/heads/integration/"+num)
		if surfacedTip != wantTip {
			t.Errorf("surfaced branch %s tip = %s, want %s (Integration branch tip)", num, surfacedTip, wantTip)
		}
		var fixtureSHA string
		if num == "calc-engine" {
			fixtureSHA = fixtureShaA
		} else {
			fixtureSHA = fixtureShaB
		}
		if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", fixtureSHA, "refs/heads/"+num).Run(); err != nil {
			t.Errorf("fixture commit for issue %s not reachable from surfaced branch %s", branch, num)
		}
	}
}

// The #2130 seed-branch containment gate end to end: blocker #01 lands onto
// integration/calc-engine but stays open, and dependent #02 unblocks in that
// same run, the window #1850 closes. The discriminator is the released-reason
// string blockerReady prints, not ready=true: the removed no-scope fallback
// (issue #2151) released here too, but printed a different reason.
func TestWire_ComposedLoop_SameParentBlockerChainLandsInOneRun(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const blockerNum, dependentNum = "01", "02"
	writeLocalIssue(t, issuesDir, blockerNum, "seam 01", "Calc Engine", testLabels.InProgress)
	writeLocalIssueWithBlocker(t, issuesDir, dependentNum, "seam 02", "Calc Engine", testLabels.InProgress, blockerNum)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	cf01 := lw.CodeForgeForIssue(blockerNum)
	branch01 := cf01.AgentBranch(blockerNum)
	sha01 := bundleFixtureCommit(t, accumDir, testBaseBranch, branch01, blockerNum, lw.OutboxDir(blockerNum))

	cfg01 := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf01, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s01 := settle.New(cfg01, it, cf01)
	s01.Settle(dispatch.NewFake(), blockerNum, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: blockerNum, Landing: branch01, Status: "ready"},
		},
	})

	// Confirm the blocker's commit really is on the shared parent's
	// Integration branch, so the readiness assertion below tests the
	// containment gate rather than a "never landed" false negative.
	parent01 := lw.ResolveParent(blockerNum)
	if got := parent01.String(); got != "calc-engine" {
		t.Fatalf("ResolveParent(%s) = %q, want %q", blockerNum, got, "calc-engine")
	}
	integ := local.IntegrationBranch(parent01)
	if err := exec.Command("git", "-C", accumDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+integ).Run(); err != nil {
		t.Fatalf("Integration branch %s missing from Accumulation repo after blocker settled", integ)
	}
	if err := exec.Command("git", "-C", accumDir, "merge-base", "--is-ancestor", sha01, "refs/heads/"+integ).Run(); err != nil {
		t.Fatalf("blocker commit %s not reachable from %s", sha01, integ)
	}

	// The blocker's issue is still open here, which rules out the IssueClosed
	// shortcut. That alone does not distinguish the pre-#2130 fallback, so the
	// released-reason string asserted below is what pins the release to
	// #2130's seed-branch gate.
	rdy, err := waves.NewReadiness(it, []waves.Issue{{Number: dependentNum}})
	if err != nil {
		t.Fatalf("waves.NewReadiness: %v", err)
	}
	cf02 := lw.CodeForgeForIssue(dependentNum)
	wcfg := waves.Config{
		FailedLabel:   testLabels.Failed,
		CompleteLabel: testLabels.Complete,
		SeedScopeOf:   func(num string) forge.SeedScope { return localloop.SeedScopeOf(it, num) },
	}
	var ready bool
	var failed, unready []string
	// caps02 is resolved fresh against cf02 because CODE_FORGE=local wires a
	// CodeForge per issue, so capabilities do not carry across blockerNum's
	// instance.
	caps02 := forge.ResolveCapabilities(cf02, it, backend.Descriptor{}, backend.Descriptor{})
	output := captureStdout(t, func() {
		ready, failed, unready = rdy.Status(wcfg, it, cf02, caps02, dependentNum)
	})
	if !ready {
		t.Errorf("waves.Readiness.Status(%s) ready = false, want true (blocker's landing already reaches this seam's own %s)", dependentNum, integ)
	}
	if len(unready) != 0 {
		t.Errorf("waves.Readiness.Status(%s) unready = %v, want none", dependentNum, unready)
	}
	if len(failed) != 0 {
		t.Errorf("waves.Readiness.Status(%s) failed = %v, want none", dependentNum, failed)
	}

	wantReleaseReason := fmt.Sprintf("landing present on integration/%s (this seam's own integration branch)", lw.ResolveParent(dependentNum).String())
	if !strings.Contains(output, wantReleaseReason) {
		t.Errorf("captured stdout = %q, want it to contain the #2130 released-via-containment reason %q -- pre-#2130 would print the removed no-scope fallback's \"verified merged into Integration\" reason instead", output, wantReleaseReason)
	}

	iss01, err := it.Issue(blockerNum)
	if err != nil {
		t.Fatalf("Issue(%s): %v", blockerNum, err)
	}
	if iss01.State == forge.IssueClosed {
		t.Fatalf("issue %s state = closed, want still open -- readiness above must come from the seed-branch containment gate, not the IssueClosed fallback", blockerNum)
	}

	// The dependent's seed branch already carries the blocker's work, and the
	// Box would clone it, so #02's fixture commit builds on top of it.
	seedBase := local.IntegrationBranch(lw.ResolveParent(dependentNum))
	if seedBase != integ {
		t.Fatalf("dependent's seed base = %q, want %q (shared parent)", seedBase, integ)
	}
	exists, err := cf02.BranchExists(seedBase)
	if err != nil {
		t.Fatalf("BranchExists(%s): %v", seedBase, err)
	}
	if !exists {
		t.Fatalf("seed branch %s does not exist -- want it to carry the blocker's landed commit", seedBase)
	}

	branch02 := cf02.AgentBranch(dependentNum)
	sha02 := bundleFixtureCommit(t, accumDir, seedBase, branch02, dependentNum, lw.OutboxDir(dependentNum))

	cfg02 := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf02, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s02 := settle.New(cfg02, it, cf02)
	s02.Settle(dispatch.NewFake(), dependentNum, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: dependentNum, Landing: branch02, Status: "ready"},
		},
	})

	res, err := reconcile.Run(it, cf02, nil, cfg02.Capabilities, func(num string) forge.SeedScope {
		p := lw.ResolveParent(num)
		return forge.NewSeedScope(p.String(), local.IntegrationBranch(p))
	})
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 2 {
		t.Fatalf("reconcile.Run closed = %v, want both %s and %s", res.Closed, blockerNum, dependentNum)
	}
	closed := map[string]bool{}
	for _, n := range res.Closed {
		closed[n] = true
	}
	if !closed[blockerNum] || !closed[dependentNum] {
		t.Fatalf("reconcile.Run closed = %v, want both %s and %s", res.Closed, blockerNum, dependentNum)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck, cfg02.Capabilities); err != nil {
		t.Fatalf("Surface: %v", err)
	}

	// The surfaced branch name is title-derived from the shared parent (issue
	// #1811). Both commits reachable from it is the end-to-end proof the
	// dependent's work landed on top of the blocker's.
	const wantBranch = "calc-engine"
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", sha01, "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("blocker commit %s not reachable from surfaced branch %s", sha01, wantBranch)
	}
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", sha02, "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("dependent commit %s not reachable from surfaced branch %s", sha02, wantBranch)
	}
}

// captureStdout observes blockerReady's held-reason fmt.Printf line
// (waves/blocker.go), which nothing else exposes. The reader goroutine starts
// before fn so a write larger than the pipe's kernel buffer cannot deadlock
// fn against an unread pipe. It mutates process-global os.Stdout, so callers
// must not run under t.Parallel().
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()

	fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}

// #2131's regression test for the cross-parent leak #2130 closes: a blocker
// landed onto an Integration branch the dependent never seeds from must hold,
// not release. The removed no-scope fallback (issue #2151) merge-base-checked
// the blocker's own Integration branch, so a blocker merged anywhere released
// every waiting dependent and ready would come back true here.
func TestWire_ComposedLoop_CrossParentBlockerHoldsLoudly(t *testing.T) {
	setGitIdentityEnv(t)
	operatorDir := newOperatorCheckout(t)
	t.Chdir(operatorDir)

	accumDir := filepath.Join(t.TempDir(), "accum.git")
	if err := local.SeedAccumulationRepo(accumDir, operatorDir, testBaseBranch); err != nil {
		t.Fatalf("SeedAccumulationRepo: %v", err)
	}

	issuesDir := t.TempDir()
	it := local.NewLocalTracker(issuesDir, testLabels)
	const blockerNum, dependentNum = "11", "12"
	writeLocalIssue(t, issuesDir, blockerNum, "seam 11", "Alpha Engine", testLabels.InProgress)
	writeLocalIssueWithBlocker(t, issuesDir, dependentNum, "seam 12", "Beta Engine", testLabels.InProgress, blockerNum)

	lw := localloop.Wire(localloop.Config{
		AccumulationRepoDir: accumDir,
		BaseBranch:          testBaseBranch,
		GitUserName:         "Test Bot",
		GitUserEmail:        "bot@example.com",
		BranchPrefix:        "agent/issue-",
	}, it)

	// Land only the blocker, onto its own parent's Integration branch, which
	// the dependent (parent "Beta Engine") never seeds from.
	cf11 := lw.CodeForgeForIssue(blockerNum)
	branch11 := cf11.AgentBranch(blockerNum)
	sha11 := bundleFixtureCommit(t, accumDir, testBaseBranch, branch11, blockerNum, lw.OutboxDir(blockerNum))

	cfg11 := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
		Capabilities:      forge.ResolveCapabilities(cf11, it, backend.Descriptor{}, backend.Descriptor{}),
	}
	s11 := settle.New(cfg11, it, cf11)
	s11.Settle(dispatch.NewFake(), blockerNum, 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: blockerNum, Landing: branch11, Status: "ready"},
		},
	})

	// Confirm the blocker landed on its own Integration branch, so the
	// assertion below tests the cross-parent containment gate rather than a
	// "never landed" false negative.
	parent11 := lw.ResolveParent(blockerNum)
	if got := parent11.String(); got != "alpha-engine" {
		t.Fatalf("ResolveParent(%s) = %q, want %q", blockerNum, got, "alpha-engine")
	}
	integ11 := local.IntegrationBranch(parent11)
	if err := exec.Command("git", "-C", accumDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+integ11).Run(); err != nil {
		t.Fatalf("Integration branch %s missing from Accumulation repo after blocker settled", integ11)
	}
	if err := exec.Command("git", "-C", accumDir, "merge-base", "--is-ancestor", sha11, "refs/heads/"+integ11).Run(); err != nil {
		t.Fatalf("blocker commit %s not reachable from %s", sha11, integ11)
	}

	// The dependent's own seed branch never received the blocker's commit, so
	// #2130's LandingContainmentQuery reports not-contained. The blocker is
	// still open, which also rules out the IssueClosed fallback.
	rdy, err := waves.NewReadiness(it, []waves.Issue{{Number: dependentNum}})
	if err != nil {
		t.Fatalf("waves.NewReadiness: %v", err)
	}
	cf12 := lw.CodeForgeForIssue(dependentNum)
	wcfg := waves.Config{
		FailedLabel:   testLabels.Failed,
		CompleteLabel: testLabels.Complete,
		SeedScopeOf:   func(num string) forge.SeedScope { return localloop.SeedScopeOf(it, num) },
	}

	var ready bool
	var failed, unready []string
	// caps12 is resolved fresh against cf12 because CODE_FORGE=local wires a
	// CodeForge per issue, so capabilities do not carry across blockerNum's
	// instance.
	caps12 := forge.ResolveCapabilities(cf12, it, backend.Descriptor{}, backend.Descriptor{})
	output := captureStdout(t, func() {
		ready, failed, unready = rdy.Status(wcfg, it, cf12, caps12, dependentNum)
	})

	if ready {
		t.Errorf("waves.Readiness.Status(%s) ready = true, want false (blocker landed on a different parent's integration branch)", dependentNum)
	}
	if want := []string{blockerNum}; !reflect.DeepEqual(unready, want) {
		t.Errorf("waves.Readiness.Status(%s) unready = %v, want %v", dependentNum, unready, want)
	}
	if len(failed) != 0 {
		t.Errorf("waves.Readiness.Status(%s) failed = %v, want none", dependentNum, failed)
	}

	seedParent := lw.ResolveParent(dependentNum).String()
	if got := seedParent; got != "beta-engine" {
		t.Fatalf("ResolveParent(%s) = %q, want %q", dependentNum, got, "beta-engine")
	}
	wantReason := fmt.Sprintf("landed but not yet on integration/%s (this seam's own integration branch); holding", seedParent)
	if !strings.Contains(output, wantReason) {
		t.Errorf("captured stdout = %q, want it to contain the #2130 held reason %q", output, wantReason)
	}

	iss11, err := it.Issue(blockerNum)
	if err != nil {
		t.Fatalf("Issue(%s): %v", blockerNum, err)
	}
	if iss11.State == forge.IssueClosed {
		t.Fatalf("issue %s state = closed, want still open -- the hold above must come from the seed-branch containment gate, not an IssueClosed fallback", blockerNum)
	}

	// The dependent's own Integration branch must never exist: the gate held
	// rather than letting #12 seed from the bare base branch.
	integ12 := local.IntegrationBranch(lw.ResolveParent(dependentNum))
	exists, err := cf12.BranchExists(integ12)
	if err != nil {
		t.Fatalf("BranchExists(%s): %v", integ12, err)
	}
	if exists {
		t.Errorf("Integration branch %s exists, want it absent -- the dependent must never have been dispatched/landed while held", integ12)
	}
}
