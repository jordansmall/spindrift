package forgejo_test

import (
	"errors"
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
	}, nil)
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

	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      fake.URL(),
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
	}, nil, repo.Bare)
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
	return forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
	}, nil, filepath.Join(h.t.TempDir(), "does-not-exist.git"))
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

// TestForgejoCodeForge_Probe_AuthFailure verifies the CodeForge seam's Probe
// surfaces forge.ErrAuthFailure (rather than wrapping it in ErrRepoNotFound)
// when Forgejo rejects the credentials -- the CodeForge contract suite only
// exercises success and unreachable-backend Probe, so this covers the 401/403
// discrimination branch forgejoCodeForge.Probe shares with the tracker's own
// (already-tested) Probe.
func TestForgejoCodeForge_Probe_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "bad-token",
	}, nil)
	if _, err := cf.Probe(); !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe() error = %v, want ErrAuthFailure", err)
	}
}

// TestForgejoCodeForge_BranchProtected_Protected verifies BranchProtected
// reports (true, nil) when Forgejo's branch_protections list endpoint
// returns a rule whose rule_name matches branch exactly.
func TestForgejoCodeForge_BranchProtected_Protected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/branch_protections" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"rule_name":"main"}]`))
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected() error = %v, want nil", err)
	}
	if !protected {
		t.Fatal("BranchProtected() = false, want true")
	}
}

// TestForgejoCodeForge_BranchProtected_GlobRuleName verifies BranchProtected
// matches a rule_name glob (e.g. "release/*") against branch, not just a
// literal branch name -- Forgejo's rule_name is a glob, so a branch can be
// protected without ever appearing verbatim as a rule_name.
func TestForgejoCodeForge_BranchProtected_GlobRuleName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"rule_name":"release/*"}]`))
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("release/1.0")
	if err != nil {
		t.Fatalf("BranchProtected() error = %v, want nil", err)
	}
	if !protected {
		t.Fatal("BranchProtected() = false, want true for a branch matching a glob rule_name")
	}
}

// TestForgejoCodeForge_BranchProtected_NotProtected verifies BranchProtected
// reports the definitive, successful (false, nil) result -- not an error --
// when no listed rule_name matches branch.
func TestForgejoCodeForge_BranchProtected_NotProtected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"rule_name":"release/*"}]`))
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected() error = %v, want nil", err)
	}
	if protected {
		t.Fatal("BranchProtected() = true, want false")
	}
}

// TestForgejoCodeForge_BranchProtected_NoRules verifies BranchProtected
// reports the definitive, successful (false, nil) result -- not an error --
// when Forgejo's branch_protections list endpoint returns 200 with an empty
// array, meaning the repo has no protection rules at all. This is the
// genuine "no rules" signal on Gitea/Forgejo's list endpoint, distinct from
// a 404 (see TestForgejoCodeForge_BranchProtected_GenericNotFound).
func TestForgejoCodeForge_BranchProtected_NoRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected() error = %v, want nil", err)
	}
	if protected {
		t.Fatal("BranchProtected() = true, want false")
	}
}

// TestForgejoCodeForge_BranchProtected_GenericNotFound verifies
// BranchProtected surfaces a non-nil error -- never a false "not
// protected" -- when the branch_protections list endpoint 404s. Unlike
// GitHub's per-branch endpoint, Forgejo's list endpoint always returns 200
// with an empty array for a repo with no rules (see
// TestForgejoCodeForge_BranchProtected_NoRules), so a 404 here means the
// repo or endpoint couldn't be resolved at all (old server, wrong mount,
// invisible repo) -- a genuine probe failure, not a definitive answer.
func TestForgejoCodeForge_BranchProtected_GenericNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected() error = nil, want non-nil")
	}
	if protected {
		t.Fatal("BranchProtected() = true, want false alongside a non-nil error")
	}
}

// TestForgejoCodeForge_BranchProtected_ProbeFailure verifies BranchProtected
// surfaces a non-nil error -- never a false "not protected" -- when the
// probe itself fails to determine the answer, e.g. a 403 auth failure.
func TestForgejoCodeForge_BranchProtected_ProbeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bp, ok := cf.(forge.BranchProtectionForge)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchProtectionForge")
	}
	protected, err := bp.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected() error = nil, want non-nil")
	}
	if protected {
		t.Fatal("BranchProtected() = true, want false alongside a non-nil error")
	}
}
