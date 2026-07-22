package localloop_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/settle"
)

const testBaseBranch = "main"

var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
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

// newOperatorCheckout creates a non-bare git repo standing in for the
// operator's local working directory, seeded with one commit on
// testBaseBranch — deliberately created with no remote, mirroring how an
// operator's own checkout has none configured toward the Accumulation repo.
func newOperatorCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", testBaseBranch)
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	run(t, dir, "add", "base.txt")
	run(t, dir, "commit", "-m", "base")
	return dir
}

// writeLocalIssue writes num's issue file directly under dir in the local
// tracker's frontmatter grammar (ADR 0013) — the composed test's stand-in
// for however the issue file first came to exist, since LocalTracker itself
// has no issue-creation API of its own.
func writeLocalIssue(t *testing.T, dir, num, title, parent, state string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "state: %s\n", state)
	b.WriteString("labels: []\n")
	fmt.Fprintf(&b, "created: %s\n", time.Now().Format(time.RFC3339))
	if parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", parent)
	}
	b.WriteString("---\n")
	b.WriteString("body\n")
	writeFile(t, filepath.Join(dir, num+".md"), b.String())
}

// bundleFixtureCommit stands in for the Agent: it clones accumDir and
// commits one marker file on branch off base, the "commit on the agent
// branch" contract every Agent now shares under CODE_FORGE=local (issue
// #1808). The bundle itself comes from the real bundle-out producer
// (bundleout.Run), not a hand-written `git bundle create` — the same
// producer driver-exec's bundle-out verb calls in production — so
// RelayBundle sees exactly what a real Box's code-out would have left
// there. Returns the fixture commit's sha.
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

// TestResolveParent_IssueLookupError_FallsBackToOwnSlug verifies
// ResolveParent falls back to num's own sanitized slug — the same posture
// local.ResolveParent gives an issue with no parent: set — when the
// IssueTracker lookup itself fails, rather than propagating the error
// through callers with no error return to give it (e.g. BASE_BRANCH
// forwarding's func(string) string shape).
func TestResolveParent_IssueLookupError_FallsBackToOwnSlug(t *testing.T) {
	fc := forge.NewFake()
	fc.IssueErr = errors.New("issue file unreadable")

	if got, want := localloop.ResolveParent(fc, "Broad Ticket").String(), "broad-ticket"; got != want {
		t.Errorf("ResolveParent = %q, want %q", got, want)
	}
}

// TestWired_ResolveParent_MemoizesPerIssue verifies Wire resolves each
// issue's parent exactly once (issue #1810): a second Wired.ResolveParent
// call for the same issue number reuses the first call's resolved value
// instead of hitting the IssueTracker again, so the forge constructor, base-
// branch resolver, and surface grouping consuming the same *Wired share one
// resolution per issue rather than each re-deriving it independently.
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

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// TestWire_ComposedLoop_HappyPath drives one seam end to end through
// localloop.Wire's own wiring, exactly as production does: a fixture commit
// standing in for the Agent, a real bundle in the outbox, a real settle
// (relay + merge onto the Integration branch), a real reconcile (the seam's
// issue closes), and a real surface (the resulting branch appears in the
// operator's checkout with the fixture commit reachable from it) — issue
// #1806 AC2/AC3.
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
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	iss, err := it.Issue(num)
	if err != nil {
		t.Fatalf("Issue(%s): %v", num, err)
	}
	if !containsLabel(iss.Labels, testLabels.Complete) {
		t.Fatalf("issue %s labels = %v, want %s after settle", num, iss.Labels, testLabels.Complete)
	}

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	// The seam has no parent: frontmatter — its title, "seam 42", is what
	// surfaces the branch name (sanitized, issue #1811), not the slug
	// ResolveParent used to key the Integration branch.
	const wantBranch = "seam-42"
	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
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

// TestWire_ComposedLoop_EmptyTitleSanitizesToSlug drives the parentless
// title-derived naming's slug fallback (issue #1811 AC3): a title made
// entirely of characters SanitizeParent strips (no [a-z0-9] survives) must
// not surface an empty-string branch name — Surface falls back to the
// ticket's own slug, the same name a parented ticket would use.
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
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
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

// TestWire_ComposedLoop_GarbageParentUsesTitleNaming verifies a seam whose
// parent: frontmatter sanitizes to empty (garbage made entirely of
// non-[a-z0-9] characters) is treated as parentless for surfaced-branch
// naming, exactly like an unset parent: local.ResolveParent already folds
// it into "its own broad ticket, keyed on its own slug" (ADR 0033, issue
// #1734), so Surface's title-derived naming (issue #1811) must recognize it
// the same way rather than only checking the raw parent: string for "".
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
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
	}
	s.Settle(dispatch.NewFake(), num, 0, result)

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	const wantBranch = "seam-48"
	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
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

// TestWire_ComposedLoop_HealsStuckBranchRefLanding drives Reconcile's
// healing path (issue #1809) through the composed wiring: a seam's branch is
// relayed and merged cleanly onto its Integration branch, but its recorded
// landing is left at the raw pre-merge branch name — standing in for
// settle's post-merge landing upgrade (LandingRef) never having run even
// though the merge itself succeeded. Reconcile's next sweep must recognize
// the branch as an ancestor of the Integration branch, upgrade the recorded
// landing to the rich IntegrationRef form, and close the seam — the seam
// heals itself instead of staying stuck open silently forever.
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
	// mergeImmediate having already succeeded — then record only the raw
	// branch as the landing, sabotaging exactly the post-merge upgrade step
	// issue #1809 heals.
	if err := cf.(forge.BundleRelay).RelayBundle(lw.OutboxDir(num), branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if err := cf.Merge(branch); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := it.RecordLanding(num, branch); err != nil {
		t.Fatalf("RecordLanding: %v", err)
	}

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
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

// TestWire_ComposedLoop_MissingBundleBlocksNotFailed drives the missing-
// bundle held path through the same composed surface: no bundle ever lands
// in the outbox (the Agent produced nothing), so settle's relay fails and
// the seam blocks — agent-complete, not agent-failed (ADR 0033) — reconcile
// leaves it open (its recorded raw branch never merged, so Run's healing
// path reports it stuck, issue #1809) and Surface reports the broad ticket
// held on that stuck landing rather than surfacing it (issue #1806 AC4,
// issue #1811).
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

	// No bundleFixtureCommit call: the outbox stays empty, standing in for
	// an Agent that produced no code-out.

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: num, Landing: branch, Status: "ready"},
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

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Fatalf("reconcile.Run closed = %v, want none (landing never verified)", res.Closed)
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
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

// TestWire_ComposedLoop_OneOpenSiblingNotSurfaced drives the one-open-
// sibling held path: a broad ticket's first seam lands and closes, but its
// sibling stays open — surface must not publish the parent's Integration
// branch into the operator's checkout until every seam is closed, even
// though that branch already exists in the Accumulation repo (issue #1806
// AC4).
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
	}
	s := settle.New(cfg, it, cf)
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: landedNum, Landing: branch, Status: "ready"},
	}
	s.Settle(dispatch.NewFake(), landedNum, 0, result)

	res, err := reconcile.Run(it, cf, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != landedNum {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, landedNum)
	}

	// Sanity: the parent's Integration branch really did land in the
	// Accumulation repo, so the assertion below tests the sibling-open
	// gate specifically, not a "never landed" false negative.
	if err := exec.Command("git", "-C", accumDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+local.IntegrationBranch(sanitizedParent)).Run(); err != nil {
		t.Fatalf("Integration branch %s missing from Accumulation repo after landedNum settled", local.IntegrationBranch(sanitizedParent))
	}

	var out strings.Builder
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
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

// TestWire_ComposedLoop_MixedParentBatch_EachOwnIntegrationBranch drives two
// seams with distinct parents through the same *Wired end to end — issue
// #1810 AC4's named scenario — asserting each lands, closes, and surfaces
// onto its own Integration branch rather than collapsing onto a single one
// (TestWired_ResolveParent_MemoizesPerIssue already covers the "resolved
// exactly once" guarantee itself against a call-counting fake).
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

	cfg := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
	}
	sA := settle.New(cfg, it, cfA)
	sA.Settle(dispatch.NewFake(), numA, 0, dispatch.Result{
		Success: true, OutcomeFound: true,
		Outcome: outcome.Outcome{Issue: numA, Landing: branchA, Status: "ready"},
	})
	sB := settle.New(cfg, it, cfB)
	sB.Settle(dispatch.NewFake(), numB, 0, dispatch.Result{
		Success: true, OutcomeFound: true,
		Outcome: outcome.Outcome{Issue: numB, Landing: branchB, Status: "ready"},
	})

	res, err := reconcile.Run(it, cfA, nil, func(num string) string { return lw.ResolveParent(num).String() })
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 2 {
		t.Fatalf("reconcile.Run closed = %v, want both %s and %s", res.Closed, numA, numB)
	}

	if err := lw.Surface(operatorDir, io.Discard, res.Stuck); err != nil {
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
