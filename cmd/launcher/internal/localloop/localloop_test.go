package localloop_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
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

// bundleFixtureCommit stands in for the Agent and the not-yet-built
// bundle-out verb (T2): it clones accumDir, commits one marker file on
// branch off base, and bundles just that new commit into
// outboxDir/seam.bundle, so RelayBundle has exactly what a real Box's
// code-out would have left there. Returns the fixture commit's sha.
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
	if err := os.MkdirAll(outboxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, work, "bundle", "create", filepath.Join(outboxDir, local.BundleFileName), base+".."+branch)
	return sha
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
	if parent != num {
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

	res, err := reconcile.Run(it, cf, nil)
	if err != nil {
		t.Fatalf("reconcile.Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != num {
		t.Fatalf("reconcile.Run closed = %v, want [%s]", res.Closed, num)
	}

	if err := lw.Surface(operatorDir, io.Discard); err != nil {
		t.Fatalf("Surface: %v", err)
	}

	surfacedTip := revParse(t, operatorDir, "refs/heads/"+parent)
	wantTip := revParse(t, accumDir, "refs/heads/"+local.IntegrationBranch(parent))
	if surfacedTip != wantTip {
		t.Errorf("surfaced branch %s tip = %s, want %s (Integration branch tip)", parent, surfacedTip, wantTip)
	}
	if err := exec.Command("git", "-C", operatorDir, "merge-base", "--is-ancestor", fixtureSHA, "refs/heads/"+parent).Run(); err != nil {
		t.Errorf("fixture commit %s not reachable from surfaced branch %s", fixtureSHA, parent)
	}
}
