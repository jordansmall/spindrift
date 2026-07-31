package forgejo_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// TestNewForgejoCodeForge_ImplementsPRForge asserts that NewForgejoCodeForge
// satisfies forge.PRForge — the Forgejo Code Forge is the second full-parity
// PRForge backend beside github (issue #1961): it opens PRs, watches CI, and
// drives merge/auto-merge/draft-ready through the same seam.
func TestNewForgejoCodeForge_ImplementsPRForge(t *testing.T) {
	var cf forge.CodeForge = forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	})
	if _, ok := cf.(forge.PRForge); !ok {
		t.Fatal("NewForgejoCodeForge does not satisfy forge.PRForge, want the full-parity PRForge adapter")
	}
}

// forgejoCodeForgeHarness is a forgetest.CodeForgeHarness backed by a real
// bare git repo (forgetest.GitRepoFixture) for AgentBranch/BranchExists/
// Rebase's git plumbing, plus fakeForgejo (forgejo_fake_test.go) standing in
// for the Forgejo REST API that Merge and Rebase's PR-head resolution now
// drive (slice 6, issue #1961): Merge/Rebase take a PR URL, not a raw branch
// name, so SeedLandable seeds both a real git branch and an open PR whose
// head ref names it. fakeForgejo's mergeHook performs a genuine git merge
// against the bare repo so a scripted merge outcome (land or conflict) is
// backed by the same git plumbing production Rebase uses directly.
type forgejoCodeForgeHarness struct {
	t    *testing.T
	repo *forgetest.GitRepoFixture
	fake *fakeForgejo
	cf   forge.CodeForge
}

func newForgejoCodeForgeHarness(t *testing.T) *forgejoCodeForgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	fake := newFakeForgejo(t)

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      fake.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: repo.Bare,
	})
	h := &forgejoCodeForgeHarness{t: t, repo: repo, fake: fake, cf: cf}
	fake.mergeHook = h.realMerge
	return h
}

func (h *forgejoCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

// Unreachable returns a forge whose REST base URL points at a closed
// httptest server (so Probe's REST call fails) and whose git remote points
// at a nonexistent path.
func (h *forgejoCodeForgeHarness) Unreachable() forge.CodeForge {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	return forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: filepath.Join(h.t.TempDir(), "does-not-exist.git"),
	})
}

func (h *forgejoCodeForgeHarness) BranchPrefix() string { return "agent/issue-" }

func (h *forgejoCodeForgeHarness) branchName(num string) string { return h.BranchPrefix() + num }

// SeedLandable seeds a real git branch (one commit ahead of main, carrying
// num's marker) and an open PR whose head ref names that branch, and
// returns the PR's html_url — the ref Merge/Rebase expect now that both are
// PR-URL/REST-based.
func (h *forgejoCodeForgeHarness) SeedLandable(num string) string {
	h.repo.SeedBranch(h.branchName(num), num)
	return h.fake.SeedOpenPR(num)
}

func (h *forgejoCodeForgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *forgejoCodeForgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *forgejoCodeForgeHarness) Rebased(num string) bool {
	return h.repo.Rebased(h.branchName(num))
}

// FailNextMerge provokes a genuine conflict via GitRepoFixture.ConflictBase
// (so realMerge's git merge actually fails) and flips the PR's mergeable
// flag false in the fake (so the adapter's classifyMergeFailure, which
// queries Mergeable over REST after the merge POST's non-2xx response,
// reports forge.ErrMergeConflict rather than forge.ErrMergeBlockedByChecks).
func (h *forgejoCodeForgeHarness) FailNextMerge(ref string) {
	num := prNumFromURL(ref)
	h.repo.ConflictBase(num)
	h.fake.SetMergeable(num, false)
}

// FailNextRebase provokes a genuine conflict via GitRepoFixture.ConflictBase
// — the underlying git adapter's real `git rebase` discovers it directly,
// unscripted, and maps it to forge.ErrMergeConflict itself.
func (h *forgejoCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(prNumFromURL(ref))
}

// realMerge is fakeForgejo's mergeHook: a genuine git merge of num's agent
// branch onto main against the bare repo backing h.repo, mirroring the
// github adapter's fake-gh-codeforge.sh `pr-merge` case. On success it
// pushes the merge commit back to main; on conflict it aborts the merge and
// returns an error, which the fake's merge route turns into a non-2xx
// response for the adapter's classifyMergeFailure to interpret.
func (h *forgejoCodeForgeHarness) realMerge(num string) error {
	h.t.Helper()
	work := h.t.TempDir()
	if out, err := exec.Command("git", "clone", h.repo.Bare, work).CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", work, "checkout", "main").CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout main: %w: %s", err, out)
	}
	head := h.branchName(num)
	if out, err := exec.Command("git", "-C", work, "merge", "--no-ff", "origin/"+head, "-m", "merge "+head).CombinedOutput(); err != nil {
		exec.Command("git", "-C", work, "merge", "--abort").Run()
		return fmt.Errorf("merge conflict: %s", out)
	}
	if out, err := exec.Command("git", "-C", work, "push", "origin", "HEAD:main").CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w: %s", err, out)
	}
	return nil
}

func TestForgejoClient_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newForgejoCodeForgeHarness(t))
}
