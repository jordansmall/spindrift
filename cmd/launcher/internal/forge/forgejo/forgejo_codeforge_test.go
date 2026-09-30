package forgejo_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// Forgejo is the second full-parity PRForge backend beside github (issue
// #1961), so it must open PRs, watch CI, and drive merge/auto-merge/draft-ready
// through the same seam.
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

// The harness needs both halves because Merge and Rebase take a PR URL, not a
// raw branch name (slice 6, issue #1961): a real bare git repo for the git
// plumbing, and fakeForgejo for the REST calls that resolve the PR head. Its
// mergeHook runs a genuine git merge against the bare repo, so a land or
// conflict outcome comes from the same git plumbing production Rebase uses.
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

// The server is closed on purpose so Probe's REST call fails, and the git
// remote path does not exist.
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

// Both a real branch (one commit ahead of main, carrying num's marker) and an
// open PR naming it are needed: Merge and Rebase take the returned html_url.
func (h *forgejoCodeForgeHarness) SeedLandable(num string) string {
	h.repo.SeedBranch(h.branchName(num), num)
	return h.fake.SeedOpenPR(num)
}

func (h *forgejoCodeForgeHarness) AdvanceBase() { h.repo.AdvanceBase() }

func (h *forgejoCodeForgeHarness) Landed(num string) bool { return h.repo.Landed(num) }

func (h *forgejoCodeForgeHarness) Rebased(num string) bool {
	return h.repo.Rebased(h.branchName(num))
}

// ConflictBase makes realMerge's git merge genuinely fail; the mergeable flag
// must also go false because classifyMergeFailure queries Mergeable over REST
// after the non-2xx merge POST, and without it reports
// forge.ErrMergeBlockedByChecks instead of forge.ErrMergeConflict.
func (h *forgejoCodeForgeHarness) FailNextMerge(ref string) {
	num := prNumFromURL(ref)
	h.repo.ConflictBase(num)
	h.fake.SetMergeable(num, false)
}

// Nothing is scripted here: the git adapter's real rebase discovers the
// conflict and maps it to forge.ErrMergeConflict itself.
func (h *forgejoCodeForgeHarness) FailNextRebase(ref string) {
	h.repo.ConflictBase(prNumFromURL(ref))
}

// realMerge is fakeForgejo's mergeHook, mirroring the github adapter's
// fake-gh-codeforge.sh pr-merge case. The error it returns on conflict is what
// the fake's merge route turns into the non-2xx response that the adapter's
// classifyMergeFailure interprets.
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

// The CodeForge contract suite only exercises success and unreachable-backend
// Probe, so this covers the 401/403 branch: rejected credentials must report
// forge.ErrAuthFailure, not ErrRepoNotFound.
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

// Forgejo's rule_name is a glob, so a branch can be protected without ever
// appearing verbatim as a rule_name.
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

// No matching rule_name is a definitive answer, so BranchProtected must report
// (false, nil) rather than an error.
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

// A 200 with an empty array is the genuine "no rules" signal on
// Gitea/Forgejo's list endpoint, so BranchProtected must report (false, nil).
// It is distinct from a 404 (see
// TestForgejoCodeForge_BranchProtected_GenericNotFound).
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

// Unlike GitHub's per-branch endpoint, Forgejo's list endpoint returns 200 with
// an empty array for a repo with no rules, so a 404 means the repo or endpoint
// could not be resolved at all (old server, wrong mount, invisible repo). That
// is a probe failure, so BranchProtected must error rather than report a false
// "not protected".
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

// DeleteBranch issues Forgejo's own branch-delete endpoint verbatim (issue
// #4112).
func TestForgejoCodeForge_DeleteBranch_IssuesDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bd, ok := cf.(forge.BranchDeleter)
	if !ok {
		t.Fatal("forgejoCodeForge does not implement forge.BranchDeleter")
	}
	if err := bd.DeleteBranch("agent/issue-9004", "main"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want %q", gotMethod, http.MethodDelete)
	}
	wantPath := "/api/v1/repos/owner/repo/branches/agent%2Fissue-9004"
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
}

// A refused delete (e.g. a branch that does not exist) must surface the
// forge's own message.
func TestForgejoCodeForge_DeleteBranch_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"message": "branch does not exist"})
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL: srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	}, nil)
	bd := cf.(forge.BranchDeleter)
	err := bd.DeleteBranch("agent/issue-9005", "main")
	if err == nil {
		t.Fatal("DeleteBranch against a missing branch: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "branch does not exist") {
		t.Errorf("DeleteBranch error = %q, want it to mention the forge's message", err.Error())
	}
}

// DeleteBranch refuses branch == base and an invalid branch name before any
// request, matching PushBranch's create-only guard (issue #4104).
func TestForgejoCodeForge_DeleteBranch_RefusesBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name, branch, base, wantErrSubstr string
	}{
		{"empty branch", "", "main", "invalid branch"},
		{"dash branch", "-x", "main", "invalid branch"},
		{"branch is base", "main", "main", "base branch"},
		{"branch is base, refs/heads/ prefixed", "refs/heads/main", "main", "base branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
				BaseURL: srv.URL,
				Repo:    "owner/repo",
				Token:   "tok",
			}, nil)
			bd := cf.(forge.BranchDeleter)
			err := bd.DeleteBranch(tc.branch, tc.base)
			if err == nil {
				t.Fatal("DeleteBranch with an invalid arg: got nil error, want one")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("DeleteBranch error = %q, want it to contain %q", err.Error(), tc.wantErrSubstr)
			}
			if called {
				t.Error("DeleteBranch with an invalid arg: request was sent, want it skipped")
			}
		})
	}
}

// A 403 leaves the answer undetermined, so BranchProtected must error rather
// than report a false "not protected".
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
