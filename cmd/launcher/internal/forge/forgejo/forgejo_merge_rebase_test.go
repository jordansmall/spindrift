package forgejo_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

func newMergeTestForge(t *testing.T, mergeMethod string, handler http.HandlerFunc) forge.CodeForge {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
		MergeMethod:  mergeMethod,
	}, nil, "unused")
}

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

// Forgejo's refusal statuses (405/409) do not say whether the PR has a content
// conflict or is blocked by checks. The pull's own mergeable field, false here,
// is what tells the two apart (issue #566).
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

// The merge endpoint refuses but mergeable is true, so the PR itself is fine
// and pending or failing required checks are what block it (issue #566).
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

// Only Forgejo's refusal statuses (405/409) get disambiguated through the
// pull's mergeable field. Every other non-2xx must come back as a raw error
// naming the status, so a 403, 429 or 500 is never misread as a conflict. The
// github adapter gates the same way on IsMergeConflict(stderr) before it
// classifies (exec_pr.go, issue #566).
func TestMerge_NonRefusalStatus_SurfacesRawError(t *testing.T) {
	for _, status := range []int{
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var gotGet bool
			cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(status)
				case http.MethodGet:
					gotGet = true
					w.Write([]byte(pullJSON(206, "open", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
				default:
					http.NotFound(w, r)
				}
			})
			err := cf.Merge("https://forge.test/owner/repo/pulls/206")
			if err == nil {
				t.Fatalf("Merge(...): want error for status %d, got nil", status)
			}
			if errors.Is(err, forge.ErrMergeConflict) {
				t.Errorf("Merge(...) status %d: masked as ErrMergeConflict: %v", status, err)
			}
			if errors.Is(err, forge.ErrMergeBlockedByChecks) {
				t.Errorf("Merge(...) status %d: masked as ErrMergeBlockedByChecks: %v", status, err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("Merge(...) status %d: error %q does not name the status", status, err)
			}
			if gotGet {
				t.Errorf("Merge(...) status %d: queried mergeable state for a non-refusal failure", status)
			}
		})
	}
}

// The fixture advances the base branch after seeding the head branch, so a
// no-op Rebase cannot pass: only a real rebase pulls in that later commit.
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

	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
	}, nil, repo.Bare)

	if err := cf.Rebase("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("Rebase(...) unexpected error: %v", err)
	}
	if !repo.Rebased(branch) {
		t.Fatalf("Rebase(...) reported success but %q never incorporated the base branch's latest commit", branch)
	}
}
