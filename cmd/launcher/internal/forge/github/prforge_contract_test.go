package github

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeGHPRForge is a stateful stand-in for the gh CLI, backing the
// prforgeHarness below.
//
//go:embed testdata/fake-gh-prforge.sh
var fakeGHPRForge string

// prforgeHarness is a forgetest.PRForgeHarness backed by a real bare git
// repo (forgetest.GitRepoFixture, the fake gh script's REMOTE) plus a
// scripted `gh` stand-in for every PR-indirection call (pr list/view/merge/
// ready, api graphql) — mirroring codeforgeHarness's split between real git
// plumbing and scripted PR-shaped lookups.
type prforgeHarness struct {
	t        *testing.T
	repo     *forgetest.GitRepoFixture
	stateDir string
	base     string
	cf       forge.CodeForge
}

func newPRForgeHarness(t *testing.T) *prforgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "prs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "branches"), 0o755); err != nil {
		t.Fatal(err)
	}

	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHPRForge), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE", repo.Bare)
	t.Setenv("STATE_DIR", stateDir)

	return &prforgeHarness{
		t:        t,
		repo:     repo,
		stateDir: stateDir,
		base:     "main",
		cf:       NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-"),
	}
}

func (h *prforgeHarness) Forge() forge.PRForge       { return h.cf.(forge.PRForge) }
func (h *prforgeHarness) CodeForge() forge.CodeForge { return h.cf }

func (h *prforgeHarness) branchName(num string) string { return "agent/issue-" + num }

func (h *prforgeHarness) prURL(num string) string {
	return "https://github.com/owner/repo/pull/" + num
}

// SeedOpenPR creates branch agent/issue-<num> one commit ahead of main's
// current tip, pushes it, and registers the head/base/prstate/branch
// mappings the fake gh script's `pr list`/`pr view`/`pr merge` handlers
// look up. Returns the PR URL every PRForge method expects.
func (h *prforgeHarness) SeedOpenPR(num string) string {
	return h.seedPR(num, false)
}

// SeedDraftPR mirrors SeedOpenPR but marks the PR draft (isDraft=true) —
// the regression coverage for issue #2408: OpenPRForBranch must adopt a
// draft PR precisely as it adopts a non-draft one.
func (h *prforgeHarness) SeedDraftPR(num string) string {
	return h.seedPR(num, true)
}

func (h *prforgeHarness) seedPR(num string, draft bool) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)

	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "head"), branch)
	writeFile(h.t, filepath.Join(prDir, "base"), h.base)
	writeFile(h.t, filepath.Join(prDir, "prstate"), "OPEN")
	writeFile(h.t, filepath.Join(prDir, "url"), h.prURL(num))
	writeFile(h.t, filepath.Join(prDir, "draft"), strconv.FormatBool(draft))

	branchFile := filepath.Join(h.stateDir, "branches", branch)
	if err := os.MkdirAll(filepath.Dir(branchFile), 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, branchFile, num)

	return h.prURL(num)
}

// SeedCheckStates writes the scripted RollupState queue CheckState pops
// from, one entry per line.
func (h *prforgeHarness) SeedCheckStates(url string, states []forge.RollupState) {
	num := prNum(url)
	lines := make([]string, len(states))
	for i, s := range states {
		lines[i] = string(s)
	}
	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "checks"), strings.Join(lines, "\n")+"\n")
}

// SeedFailingCheck writes a single failing CheckRun context so the real
// adapter's FailureDetail runs its genuine GraphQL-response parsing and
// rendering (ci_rollup.go), not a scripted pass-through.
func (h *prforgeHarness) SeedFailingCheck(url, name, conclusion, summary string) {
	num := prNum(url)
	prDir := filepath.Join(h.stateDir, "prs", num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	contexts := []failureDetailContext{{TypeName: "CheckRun", Name: name, Conclusion: conclusion, Summary: summary}}
	out, err := json.Marshal(contexts)
	if err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(prDir, "contexts.json"), string(out))
}

// SeedAutoMergeAllowed scripts the repo-wide CanAutoMerge result.
func (h *prforgeHarness) SeedAutoMergeAllowed(allowed bool) {
	writeFile(h.t, filepath.Join(h.stateDir, "automerge_allowed"), strconv.FormatBool(allowed))
}

// SeedNeedsUpdate scripts url's PR's behind_by count the fake gh script's
// `api repos/.../compare/...` case reads back.
func (h *prforgeHarness) SeedNeedsUpdate(url string, needsUpdate bool) {
	num := prNum(url)
	behindBy := "0"
	if needsUpdate {
		behindBy = "1"
	}
	writeFile(h.t, filepath.Join(h.stateDir, "prs", num, "behind_by"), behindBy)
}

// AutoMergeEnqueued reports whether the fake gh script's `pr merge --auto`
// handler recorded url's PR number as enqueued.
func (h *prforgeHarness) AutoMergeEnqueued(url string) bool {
	_, err := os.Stat(filepath.Join(h.stateDir, "prs", prNum(url), "automerge"))
	return err == nil
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecClient_PRForgeContract(t *testing.T) {
	forgetest.RunPRForgeContract(t, newPRForgeHarness(t))
}

// draftState reads back the fake gh script's own scripted draft flag for PR
// num — STATE_DIR/prs/<num>/draft, the file the script's pr-ready case
// writes "true"/"false" to. Used as the oracle for MarkReady/MarkDraft's
// effect now that OpenPRForBranch (the github adapter's own call) no longer
// round-trips isDraft at all (#2503).
func (h *prforgeHarness) draftState(url string) string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.stateDir, "prs", prNum(url), "draft"))
	if err != nil {
		h.t.Fatalf("read draft state for %q: %v", url, err)
	}
	return strings.TrimSpace(string(raw))
}

// TestExecClient_MarkReadyClearsDraft verifies MarkReady's `gh pr ready`
// call actually flips the fake script's scripted draft state — the fake gh
// script's pr-ready case previously just `exit 0`'d without touching the
// draft state file, making it unfaithful to real `gh pr ready`'s effect on
// GitHub's draft state (issue #2408). The oracle reads the fake script's own
// state file directly rather than round-tripping through
// OpenPRForBranch's draft field, since the github adapter no longer
// populates a draft field on the PR it returns (#2503).
func TestExecClient_MarkReadyClearsDraft(t *testing.T) {
	h := newPRForgeHarness(t)
	const num = "220"
	url := h.SeedDraftPR(num)

	if err := h.Forge().MarkReady(url); err != nil {
		t.Fatalf("MarkReady(%q): %v", url, err)
	}

	if got := h.draftState(url); got != "false" {
		t.Fatalf("draft state after MarkReady(%q) = %q, want %q", url, got, "false")
	}
}

// TestExecClient_MarkDraftSetsDraft is the inverse of
// TestExecClient_MarkReadyClearsDraft: MarkDraft's `gh pr ready --undo` call
// must flip an open PR back to draft, verified against the same fake-script
// state file oracle.
func TestExecClient_MarkDraftSetsDraft(t *testing.T) {
	h := newPRForgeHarness(t)
	const num = "221"
	url := h.SeedOpenPR(num)

	if err := h.Forge().MarkDraft(url); err != nil {
		t.Fatalf("MarkDraft(%q): %v", url, err)
	}

	if got := h.draftState(url); got != "true" {
		t.Fatalf("draft state after MarkDraft(%q) = %q, want %q", url, got, "true")
	}
}

// TestExecClient_HeadCommitSHA verifies the real adapter's HeadCommitSHA
// (gh pr view --json headRefOid) reports the branch's actual current tip,
// and that the value changes once a new commit genuinely lands on it — the
// signal settle's selfHealGate compares before and after a fix pass to tell
// a real push apart from a no-op fix (issue #1980). Kept outside the shared
// forgetest.RunPRForgeContract suite: forge.Fake's own HeadCommitSHA
// deliberately synthesizes a fresh value on every unscripted call (so
// settle's untouched fix-pass tests keep modeling "the push advanced the
// head" without scripting a SHA), which would make the "stays the same
// without a push" half of this test meaningless for that harness.
func TestExecClient_HeadCommitSHA(t *testing.T) {
	h := newPRForgeHarness(t)
	const num = "210"
	url := h.SeedOpenPR(num)
	branch := h.branchName(num)

	sha1, err := h.Forge().HeadCommitSHA(url)
	if err != nil {
		t.Fatalf("HeadCommitSHA(%q): %v", url, err)
	}
	if want := h.repo.BranchSHA(branch); sha1 != want {
		t.Fatalf("HeadCommitSHA(%q) = %q, want the branch's real head %q", url, sha1, want)
	}

	h.repo.AdvanceBranch(branch, num)

	sha2, err := h.Forge().HeadCommitSHA(url)
	if err != nil {
		t.Fatalf("HeadCommitSHA(%q) after advance: %v", url, err)
	}
	if sha2 == sha1 {
		t.Fatalf("HeadCommitSHA(%q) = %q, want it to change after a new commit landed on %s", url, sha2, branch)
	}
	if want := h.repo.BranchSHA(branch); sha2 != want {
		t.Fatalf("HeadCommitSHA(%q) after advance = %q, want the branch's real head %q", url, sha2, want)
	}
}
