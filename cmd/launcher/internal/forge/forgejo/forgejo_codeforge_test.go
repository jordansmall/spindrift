package forgejo_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// TestNewForgejoCodeForge_ImplementsCodeForgeOnly asserts that
// NewForgejoCodeForge satisfies forge.CodeForge but not forge.PRForge — the
// adapter is push-only, mirroring the plain git adapter's rationale (Forgejo
// pull requests are not driven through this seam).
func TestNewForgejoCodeForge_ImplementsCodeForgeOnly(t *testing.T) {
	var cf forge.CodeForge = forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: "https://codeberg.org",
		Repo:    "owner/repo",
		Token:   "tok",
	})
	if _, ok := cf.(forge.PRForge); ok {
		t.Fatal("NewForgejoCodeForge satisfies forge.PRForge, want a push-only adapter implementing forge.CodeForge only")
	}
}

// forgejoCodeForgeHarness is a forgetest.CodeForgeHarness backed by a real
// bare git repo (forgetest.GitRepoFixture) for AgentBranch/BranchExists/
// Merge/Rebase, plus an httptest server standing in for the Forgejo REST API
// for Probe — mirroring the git adapter's contract harness
// (git/contract_test.go) with the REST probe layered on top.
type forgejoCodeForgeHarness struct {
	t    *testing.T
	repo *forgetest.GitRepoFixture
	srv  *httptest.Server
	cf   forge.CodeForge
}

func newForgejoCodeForgeHarness(t *testing.T) *forgejoCodeForgeHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	t.Cleanup(srv.Close)

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: repo.Bare,
	})
	return &forgejoCodeForgeHarness{t: t, repo: repo, srv: srv, cf: cf}
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

func (h *forgejoCodeForgeHarness) IsPushOnly() {}

func (h *forgejoCodeForgeHarness) branchName(num string) string { return h.BranchPrefix() + num }

func (h *forgejoCodeForgeHarness) SeedLandable(num string) string {
	branch := h.branchName(num)
	h.repo.SeedBranch(branch, num)
	return branch
}

func (h *forgejoCodeForgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *forgejoCodeForgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *forgejoCodeForgeHarness) Rebased(num string) bool {
	return h.repo.Rebased(h.branchName(num))
}

func (h *forgejoCodeForgeHarness) FailNextMerge(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func (h *forgejoCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(strings.TrimPrefix(ref, h.BranchPrefix()))
}

func TestForgejoClient_CodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, newForgejoCodeForgeHarness(t))
}
