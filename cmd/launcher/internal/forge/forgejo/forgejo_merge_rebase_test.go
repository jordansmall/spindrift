package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// newMergeTestForge stands up a forge.CodeForge pointed at an httptest
// server, configured with mergeMethod, for the Merge REST tests below.
func newMergeTestForge(t *testing.T, mergeMethod string, handler http.HandlerFunc) forge.CodeForge {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
		MergeMethod:  mergeMethod,
	})
}

// TestMerge_Success_DefaultRebase verifies Merge POSTs to the pull's merge
// endpoint with "Do":"rebase" (the default merge style) and
// delete_branch_after_merge:true, and returns nil on a 2xx response.
func TestMerge_Success_DefaultRebase(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	if err := cf.Merge("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("Merge(...) unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/pulls/206/merge" {
		t.Errorf("path = %q, want %q", gotPath, "/api/v1/repos/owner/repo/pulls/206/merge")
	}
	if gotBody["Do"] != "rebase" {
		t.Errorf(`body["Do"] = %v, want "rebase"`, gotBody["Do"])
	}
	if gotBody["delete_branch_after_merge"] != true {
		t.Errorf("body[delete_branch_after_merge] = %v, want true", gotBody["delete_branch_after_merge"])
	}
}

// TestMerge_Success_SquashConfig verifies a "squash" MergeMethod config maps
// onto "Do":"squash" in the merge request body.
func TestMerge_Success_SquashConfig(t *testing.T) {
	var gotBody map[string]any
	cf := newMergeTestForge(t, "squash", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	if err := cf.Merge("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("Merge(...) unexpected error: %v", err)
	}
	if gotBody["Do"] != "squash" {
		t.Errorf(`body["Do"] = %v, want "squash"`, gotBody["Do"])
	}
}

// TestMerge_Conflict verifies Merge returns an error satisfying
// errors.Is(err, forge.ErrMergeConflict) when the merge endpoint refuses the
// merge and the pull's own mergeable field reports false — distinguishing a
// genuine content conflict from a checks-blocked PR (issue #566).
func TestMerge_Conflict(t *testing.T) {
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusConflict)
		case http.MethodGet:
			w.Write([]byte(pullJSON(206, "open", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
		default:
			http.NotFound(w, r)
		}
	})
	err := cf.Merge("https://forge.test/owner/repo/pulls/206")
	if !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("Merge(...): want forge.ErrMergeConflict, got %v", err)
	}
}

// TestMerge_BlockedByChecks verifies Merge returns an error satisfying
// errors.Is(err, forge.ErrMergeBlockedByChecks) when the merge endpoint
// refuses the merge but the pull's own mergeable field reports true — the
// PR itself is fine, it's blocked by pending/failing required checks.
func TestMerge_BlockedByChecks(t *testing.T) {
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
		default:
			http.NotFound(w, r)
		}
	})
	err := cf.Merge("https://forge.test/owner/repo/pulls/206")
	if !errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("Merge(...): want forge.ErrMergeBlockedByChecks, got %v", err)
	}
}

// TestRebase_ResolvesHeadBranchAndRebases verifies Rebase resolves prURL to
// its PR's head branch via the REST GET pull endpoint, then delegates to the
// underlying git adapter's Rebase against a real bare-repo fixture: the
// fixture's branch ends up incorporating the base branch's latest commit,
// and Rebased(branch) confirms it.
func TestRebase_ResolvesHeadBranchAndRebases(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	const num = "301"
	branch := "agent/issue-" + num
	repo.SeedBranch(branch, num)
	repo.AdvanceBase()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/pulls/206" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", branch, "abc123", "main")))
	}))
	defer srv.Close()

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

	if err := cf.Rebase("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("Rebase(...) unexpected error: %v", err)
	}
	if !repo.Rebased(branch) {
		t.Fatalf("Rebase(...) reported success but %q never incorporated the base branch's latest commit", branch)
	}
}
