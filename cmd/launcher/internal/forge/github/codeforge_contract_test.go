package github

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fakeGHCodeForge is a stateful stand-in for the gh CLI, backing the
// codeforgeHarness below.
//
//go:embed testdata/fake-gh-codeforge.sh
var fakeGHCodeForge string

// codeforgeHarness is a forgetest.CodeForgeHarness backed by a real bare
// git repo (forgetest.GitRepoFixture, the fake gh script's REMOTE) plus a
// scripted `gh` stand-in for the PR-indirection calls (repo clone, pr view,
// pr merge, repo/auth probes) — Rebase's own checkout/rebase/force-push run
// against real git straight through, exactly as the production adapter
// does.
type codeforgeHarness struct {
	t      *testing.T
	repo   *forgetest.GitRepoFixture
	prsDir string
	base   string
	cf     forge.CodeForge
}

func newCodeForgeHarness(t *testing.T) *codeforgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	stateDir := t.TempDir()
	prsDir := filepath.Join(stateDir, "prs")
	if err := os.MkdirAll(prsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scriptDir, "gh"), []byte(fakeGHCodeForge), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scriptDir+":"+os.Getenv("PATH"))
	t.Setenv("REMOTE", repo.Bare)
	t.Setenv("STATE_DIR", stateDir)

	return &codeforgeHarness{
		t:      t,
		repo:   repo,
		prsDir: prsDir,
		base:   "main",
		cf:     NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-"),
	}
}

func (h *codeforgeHarness) Forge() forge.CodeForge { return h.cf }

func (h *codeforgeHarness) Unreachable() forge.CodeForge {
	return NewExecClient("owner/does-not-exist", forge.DispatchLabels{}, "agent/issue-")
}

func (h *codeforgeHarness) BranchPrefix() string { return "agent/issue-" }

func (h *codeforgeHarness) branchName(num string) string { return h.BranchPrefix() + num }

func (h *codeforgeHarness) prURL(num string) string {
	return "https://github.com/owner/repo/pull/" + num
}

func prNum(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// SeedLandable creates branch agent/issue-<num> one commit ahead of main's
// current tip, pushes it, and registers the head/base mapping the fake gh
// script's `pr view`/`pr merge` handlers look up. Returns the PR URL
// Merge/Rebase expect.
func (h *codeforgeHarness) SeedLandable(num string) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)

	prDir := filepath.Join(h.prsDir, num)
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "head"), []byte(branch), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "base"), []byte(h.base), 0o644); err != nil {
		h.t.Fatal(err)
	}

	return h.prURL(num)
}

func (h *codeforgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *codeforgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *codeforgeHarness) Rebased(num string) bool { return h.repo.Rebased(h.branchName(num)) }

// FailNextMerge/FailNextRebase provoke a genuine conflict via
// GitRepoFixture.ConflictBase: the fake gh script's `pr merge` handler does
// a real git merge attempt (discovering the same conflict), and exec_pr.go's
// Rebase runs a real `git rebase` directly, unscripted.
func (h *codeforgeHarness) FailNextMerge(ref string) {
	h.repo.ConflictBase(prNum(ref))
}

func (h *codeforgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(prNum(ref))
}

func TestExecClient_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newCodeForgeHarness(t))
}
