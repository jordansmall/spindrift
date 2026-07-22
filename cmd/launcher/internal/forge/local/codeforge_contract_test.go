package local

import (
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// localCodeForgeHarness is a forgetest.CodeForgeHarness backed by a real bare
// git repo (forgetest.GitRepoFixture) standing in for the Accumulation repo
// — the local adapter's actual production shape, so Merge/Rebase exercise
// genuine git plumbing (including genuine merge/rebase conflicts) rather
// than a scripted stand-in.
type localCodeForgeHarness struct {
	t      *testing.T
	repo   *forgetest.GitRepoFixture
	parent string
	cf     forge.CodeForge
}

func newLocalCodeForgeHarness(t *testing.T) *localCodeForgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	const parent = "1694"
	repo := forgetest.NewGitRepoFixture(t, IntegrationBranch(parent))
	return &localCodeForgeHarness{
		t:      t,
		repo:   repo,
		parent: parent,
		cf:     NewLocalCodeForge(repo.Bare, IntegrationBranch(parent), parent, "Test Bot", "bot@example.com", "agent/issue-"),
	}
}

func (h *localCodeForgeHarness) Forge() forge.CodeForge { return h.cf }

func (h *localCodeForgeHarness) Unreachable() forge.CodeForge {
	return NewLocalCodeForge(filepath.Join(h.t.TempDir(), "does-not-exist.git"), IntegrationBranch(h.parent), h.parent, "Test Bot", "bot@example.com", "agent/issue-")
}

func (h *localCodeForgeHarness) BranchPrefix() string { return "agent/issue-" }

func (h *localCodeForgeHarness) IsPushOnly() {}

func (h *localCodeForgeHarness) branchName(num string) string { return h.BranchPrefix() + num }

func (h *localCodeForgeHarness) SeedLandable(num string) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)
	return branch
}

func (h *localCodeForgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *localCodeForgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *localCodeForgeHarness) Rebased(num string) bool { return h.repo.Rebased(h.branchName(num)) }

func (h *localCodeForgeHarness) FailNextMerge(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func (h *localCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func (h *localCodeForgeHarness) Parent() string { return h.parent }

// MarkLanded implements forgetest.LandingHarness (issue #1809): merges num's
// already-seeded branch for real and resolves the landed IntegrationRef via
// the same forge.LandingRef surface production's post-merge upgrade uses.
func (h *localCodeForgeHarness) MarkLanded(num string) string {
	h.t.Helper()
	branch := h.branchName(num)
	if err := h.cf.Merge(branch); err != nil {
		h.t.Fatalf("Merge(%q): %v", branch, err)
	}
	landing, err := h.cf.(forge.LandingRef).LandingRef()
	if err != nil {
		h.t.Fatalf("LandingRef: %v", err)
	}
	return landing
}

func TestLocalCodeForge_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newLocalCodeForgeHarness(t))
}
