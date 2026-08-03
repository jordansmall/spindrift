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
