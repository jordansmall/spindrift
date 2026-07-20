package git

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// gitCodeForgeHarness is a forgetest.CodeForgeHarness backed by a real bare
// git repo (forgetest.GitRepoFixture) — the git adapter's actual production
// shape, so Merge/Rebase exercise genuine git plumbing (including genuine
// merge/rebase conflicts) rather than a scripted stand-in.
type gitCodeForgeHarness struct {
	t    *testing.T
	repo *forgetest.GitRepoFixture
	cf   forge.CodeForge
}

func newGitCodeForgeHarness(t *testing.T) *gitCodeForgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	return &gitCodeForgeHarness{
		t:    t,
		repo: repo,
		cf:   NewGitClient(repo.Bare, "main", "Test Bot", "bot@example.com", "agent/issue-"),
	}
}

func (h *gitCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

func (h *gitCodeForgeHarness) Unreachable() forge.CodeForge {
	return NewGitClient(filepath.Join(h.t.TempDir(), "does-not-exist.git"), "main", "Test Bot", "bot@example.com", "agent/issue-")
}

func (h *gitCodeForgeHarness) BranchPrefix() string { return "agent/issue-" }

func (h *gitCodeForgeHarness) IsPushOnly() {}

func (h *gitCodeForgeHarness) branchName(num string) string { return h.BranchPrefix() + num }

func (h *gitCodeForgeHarness) SeedLandable(num string) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)
	return branch
}

func (h *gitCodeForgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *gitCodeForgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *gitCodeForgeHarness) Rebased(num string) bool { return h.repo.Rebased(h.branchName(num)) }

func (h *gitCodeForgeHarness) FailNextMerge(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func (h *gitCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func TestGitClient_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newGitCodeForgeHarness(t))
}
