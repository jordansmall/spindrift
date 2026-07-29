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
	writeLocalIssueBody(t, dir, num, title, parent, state, "body\n")
}

// writeLocalIssueWithBlocker is writeLocalIssue plus a "## Blocked by"
// section naming blockerNum by its own filename slug — the local tracker's
// body-sourced dependency grammar (forge.DepSourceBody, since the local
// tracker has no native blocker relationship), so DepsOf(num) resolves
// blockerNum as num's declared blocker.
func writeLocalIssueWithBlocker(t *testing.T, dir, num, title, parent, state, blockerNum string) {
	t.Helper()
	writeLocalIssueBody(t, dir, num, title, parent, state, "body\n\n## Blocked by\n- "+blockerNum+"\n")
}

// writeLocalIssueBody writes num's issue file under dir in the local
// tracker's frontmatter grammar (ADR 0013) with an explicit body — the
// shared core of writeLocalIssue and writeLocalIssueWithBlocker, which differ
// only in the body they supply.
func writeLocalIssueBody(t *testing.T, dir, num, title, parent, state, body string) {
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
	b.WriteString(body)
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

// TestSeedScopeOf_PairsSanitizedParentWithIntegrationLabel verifies
// SeedScopeOf resolves num's sanitized seed-branch parent (ResolveParent) and
// the local adapter's rendered Integration branch label
// (local.IntegrationBranch) into the same waves.SeedScope both the dispatch
// command path and the Console will consume (issue #2150), so the two can
// never disagree about which blocker landing gates a dependent.
func TestSeedScopeOf_PairsSanitizedParentWithIntegrationLabel(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "11", Parent: "Render Pipeline"})

	if got, want := localloop.SeedScopeOf(fc, "11").String(), "integration/render-pipeline"; got != want {
		t.Errorf("SeedScopeOf(11).String() = %q, want %q", got, want)
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

// TestWire_ComposedLoop_SameParentBlockerChainLandsInOneRun drives the
// #2130 seed-branch containment gate through the full composed loop rather
// than a scripted Fake. Blocker #01 and dependent #02 share parent "Calc
// Engine" (#02's body declares #01 as its blocker via local tracker
// body-sourced DepsOf); #01 lands via settle — merged onto
// integration/calc-engine — but is deliberately left OPEN (reconcile never
// runs on it yet), standing in for the same-run window #1850 closes. #02
// then unblocks in that SAME run via the #2130 seed-branch containment
// gate: waves.Readiness.Status finds #01's landing already present on
// integration/calc-engine, #02's own seed branch. #02 then genuinely seeds
// from that integration branch (the Box would have cloned it, carrying
// #01's commit forward) and lands its own commit on top, and both close in
// one reconcile.Run + Surface pass.
//
// The load-bearing pre-#2130 discriminator here is the RELEASED-REASON
// string blockerReady prints to stdout (blocker.go:236): "landing present
// on integration/<parent> (this seam's own integration branch)". Pre-#2130
// the LandingVerifier fallback releases too, but prints a different reason
// ("landing verified merged into Integration") — so asserting this exact
// string goes red if #2130's containment gate is reverted. The release-gate
// *result* (ready=true) is NOT itself a discriminator: in this same-parent
// geometry it is identical pre- and post-#2130 (the cross-parent companion
// test, TestWire_ComposedLoop_CrossParentBlockerHoldsLoudly, is the
// release-gate's own pre-#2130 regression guard).
//
// #01 staying OPEN through the readiness assertion is a SUPPORTING check
// only: it rules out the trivial IssueClosed/IssueMerged shortcut (which
// would also release, but for a non-#2130 reason), so combined with the
// released-reason string it pins the release specifically to the #2130
// containment path. It does NOT, by itself, distinguish pre-#2130 behavior
// — the pre-#2130 LandingVerifier fallback also releases with the blocker
// still open.
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

	// Land the blocker #01.
	cf01 := lw.CodeForgeForIssue(blockerNum)
	branch01 := cf01.AgentBranch(blockerNum)
	sha01 := bundleFixtureCommit(t, accumDir, testBaseBranch, branch01, blockerNum, lw.OutboxDir(blockerNum))

	cfg01 := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
	}
	s01 := settle.New(cfg01, it, cf01)
	s01.Settle(dispatch.NewFake(), blockerNum, 0, dispatch.Result{
		Success: true, OutcomeFound: true,
		Outcome: outcome.Outcome{Issue: blockerNum, Landing: branch01, Status: "ready"},
	})

	// Sanity: the blocker's commit really is on the shared parent's
	// Integration branch in the Accumulation repo, so the readiness
	// assertion below tests the containment gate specifically, not a
	// "never landed" false negative.
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

	// The dependent unblocks in-run via the seed-branch containment gate
	// (#2130) while the blocker's issue is still OPEN — proving it is the
	// seed-branch gate, not the IssueClosed fallback, doing the releasing.
	rdy, err := waves.NewReadiness(it, []waves.Issue{{Number: dependentNum}})
	if err != nil {
		t.Fatalf("waves.NewReadiness: %v", err)
	}
	cf02 := lw.CodeForgeForIssue(dependentNum)
	wcfg := waves.Config{
		InProgressLabel: testLabels.InProgress,
		FailedLabel:     testLabels.Failed,
		CompleteLabel:   testLabels.Complete,
		SeedScopeOf:     func(num string) waves.SeedScope { return localloop.SeedScopeOf(it, num) },
	}
	var ready bool
	var failed, unready []string
	output := captureStdout(t, func() {
		ready, failed, unready = rdy.Status(wcfg, it, cf02, dependentNum)
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
		t.Errorf("captured stdout = %q, want it to contain the #2130 released-via-containment reason %q -- pre-#2130 would print the LandingVerifier \"verified merged into Integration\" reason instead", output, wantReleaseReason)
	}

	iss01, err := it.Issue(blockerNum)
	if err != nil {
		t.Fatalf("Issue(%s): %v", blockerNum, err)
	}
	if iss01.State == forge.IssueClosed {
		t.Fatalf("issue %s state = closed, want still open -- readiness above must come from the seed-branch containment gate, not the IssueClosed fallback", blockerNum)
	}

	// The dependent's own seed branch (integration/calc-engine) already
	// exists and carries the blocker's work -- the Box would clone it, so
	// build #02's fixture commit on top of it, then land and close both.
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
	}
	s02 := settle.New(cfg02, it, cf02)
	s02.Settle(dispatch.NewFake(), dependentNum, 0, dispatch.Result{
		Success: true, OutcomeFound: true,
		Outcome: outcome.Outcome{Issue: dependentNum, Landing: branch02, Status: "ready"},
	})

	res, err := reconcile.Run(it, cf02, nil, func(num string) string { return lw.ResolveParent(num).String() })
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
	if err := lw.Surface(operatorDir, &out, res.Stuck); err != nil {
		t.Fatalf("Surface: %v", err)
	}

	// The surfaced branch is title-derived from the shared parent, "Calc
	// Engine" -> "calc-engine" (issue #1811) -- both commits must be
	// reachable from it, the end-to-end proof the dependent's work landed
	// on top of the blocker's on the shared Integration branch.
	const wantBranch = "calc-engine"
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", sha01, "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("blocker commit %s not reachable from surfaced branch %s", sha01, wantBranch)
	}
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", sha02, "refs/heads/"+wantBranch).Run(); err != nil {
		t.Errorf("dependent commit %s not reachable from surfaced branch %s", sha02, wantBranch)
	}
}

// captureStdout redirects os.Stdout to a pipe for the duration of fn, then
// restores it and returns everything fn wrote — the harness
// TestWire_ComposedLoop_CrossParentBlockerHoldsLoudly uses to observe
// blockerReady's held-reason fmt.Printf line (waves/blocker.go), which has
// no other externally observable surface. The reader goroutine is started
// before fn runs so a write larger than the pipe's kernel buffer can never
// deadlock fn against an unread pipe.
// captureStdout swaps the global os.Stdout for a pipe while fn runs and
// returns what fn wrote. Because it mutates process-global os.Stdout, callers
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

// TestWire_ComposedLoop_CrossParentBlockerHoldsLoudly is #2131's regression
// test for #2130's landed-behavior: a dependent whose blocker landed, but
// only onto an Integration branch the dependent does NOT itself seed from
// (a different parent), must stay HELD with the distinct #2130 held reason
// — never released the way a same-parent blocker chain is (see
// TestWire_ComposedLoop_SameParentBlockerChainLandsInOneRun, right above,
// for that companion case) — and must never be dispatched onto the bare
// base branch as a result.
//
// Before #2130, the local forge satisfied forge.LandingVerifier, and
// blockerReady's fallback called VerifyLanding(issue.Landing), which merge-
// base-checks the blocker's landing against the blocker's OWN Integration
// branch (integration/alpha-engine here) — not the dependent's seed branch
// (integration/beta-engine). A blocker merged onto ANY Integration branch
// therefore read as "verified merged into Integration" and released every
// waiting dependent, regardless of which seam it actually seeded from — the
// cross-parent leak #2130 closes. This test drives that exact setup (a
// blocker landed on a DIFFERENT parent's Integration branch than the
// dependent's own) through the composed wiring and asserts the gate now
// holds instead of releasing: it fails against the pre-#2130
// LandingVerifier fallback (ready would come back true there) and passes
// against current HEAD, where forge/local's LandingContainmentQuery checks
// containment against the dependent's own seed-branch parent
// (the SeedScope, from cfg.SeedScopeOf) instead.
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

	// Land ONLY the blocker #11, onto its own parent's Integration branch,
	// integration/alpha-engine -- a seam the dependent (parent "Beta
	// Engine") never seeds from.
	cf11 := lw.CodeForgeForIssue(blockerNum)
	branch11 := cf11.AgentBranch(blockerNum)
	sha11 := bundleFixtureCommit(t, accumDir, testBaseBranch, branch11, blockerNum, lw.OutboxDir(blockerNum))

	cfg11 := settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     testLabels.Complete,
		OutboxDir:         lw.OutboxDir,
		CodeForgeForIssue: lw.CodeForgeForIssue,
	}
	s11 := settle.New(cfg11, it, cf11)
	s11.Settle(dispatch.NewFake(), blockerNum, 0, dispatch.Result{
		Success: true, OutcomeFound: true,
		Outcome: outcome.Outcome{Issue: blockerNum, Landing: branch11, Status: "ready"},
	})

	// Sanity: the blocker really did land on ITS OWN Integration branch in
	// the Accumulation repo, so the assertion below tests the cross-parent
	// containment gate specifically, not a "never landed" false negative.
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

	// The dependent must stay HELD: its own seed branch,
	// integration/beta-engine, never received the blocker's commit, so
	// #2130's LandingContainmentQuery reports not-contained rather than
	// falling back to IssueClosed (the blocker is still open) or a
	// cross-branch LandingVerifier check.
	rdy, err := waves.NewReadiness(it, []waves.Issue{{Number: dependentNum}})
	if err != nil {
		t.Fatalf("waves.NewReadiness: %v", err)
	}
	cf12 := lw.CodeForgeForIssue(dependentNum)
	wcfg := waves.Config{
		InProgressLabel: testLabels.InProgress,
		FailedLabel:     testLabels.Failed,
		CompleteLabel:   testLabels.Complete,
		SeedScopeOf:     func(num string) waves.SeedScope { return localloop.SeedScopeOf(it, num) },
	}

	var ready bool
	var failed, unready []string
	output := captureStdout(t, func() {
		ready, failed, unready = rdy.Status(wcfg, it, cf12, dependentNum)
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
	wantReason := fmt.Sprintf("landed but not yet on this seam's integration branch (integration/%s); holding", seedParent)
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

	// Never dispatched onto bare base: the dependent's own Integration
	// branch, integration/beta-engine, must never have come into being --
	// the gate held rather than letting #12 seed from the bare base branch.
	integ12 := local.IntegrationBranch(lw.ResolveParent(dependentNum))
	exists, err := cf12.BranchExists(integ12)
	if err != nil {
		t.Fatalf("BranchExists(%s): %v", integ12, err)
	}
	if exists {
		t.Errorf("Integration branch %s exists, want it absent -- the dependent must never have been dispatched/landed while held", integ12)
	}
}
