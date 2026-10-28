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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
		MergeMethod:  mergeMethod,
	}, nil, "unused")
	forgejo.SetMergeRecheckDelay(cf, 0)
	return cf
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
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var gets int
			cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(status)
				case http.MethodGet:
					gets++
					w.Write([]byte(pullJSON(206, "open", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
				default:
					http.NotFound(w, r)
				}
			})
			err := cf.Merge("https://forge.test/owner/repo/pulls/206")
			if !errors.Is(err, forge.ErrMergeConflict) {
				t.Fatalf("Merge(...): want forge.ErrMergeConflict, got %v", err)
			}
			if gets != 2 {
				t.Errorf("pull fetched %d times, want 2 (classify, then recheck)", gets)
			}
		})
	}
}

// Forgejo reports mergeable=false on a draft or WIP pull, so a refusal there is
// no evidence of a conflict; reporting one would demote and force-push a pull
// that has none (issue #6001).
func TestMerge_DraftPull_NotClassifiedAsConflict(t *testing.T) {
	cases := []struct {
		name  string
		draft bool
		title string
	}{
		{"draft field", true, "add feature"},
		{"WIP title", false, "WIP: add feature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gets int
			cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(http.StatusMethodNotAllowed)
				case http.MethodGet:
					gets++
					w.Write([]byte(pullJSON(206, "open", false, false, tc.draft, tc.title, "agent/issue-206", "abc123", "main")))
				default:
					http.NotFound(w, r)
				}
			})
			err := cf.Merge("https://forge.test/owner/repo/pulls/206")
			if err == nil {
				t.Fatal("Merge(...): want an error for a draft pull, got nil")
			}
			if errors.Is(err, forge.ErrMergeConflict) || errors.Is(err, forge.ErrMergeBlockedByChecks) {
				t.Fatalf("Merge(...): draft pull must not classify as a sentinel, got %v", err)
			}
			if !strings.Contains(err.Error(), "draft") {
				t.Errorf("Merge(...) error = %v, want it to name the draft", err)
			}
			if gets != 1 {
				t.Errorf("pull fetched %d times, want 1 (a draft is never re-read)", gets)
			}
		})
	}
}

// Forgejo's conflict check runs asynchronously and mergeable stays false until
// it settles, so a pull that turns mergeable on the recheck is blocked by
// checks, not conflicting (issue #6001).
func TestMerge_ConflictCheckStillRunning_BlockedByChecks(t *testing.T) {
	var gets int
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			gets++
			w.Write([]byte(pullJSON(206, "open", false, gets > 1, false, "add feature", "agent/issue-206", "abc123", "main")))
		default:
			http.NotFound(w, r)
		}
	})
	err := cf.Merge("https://forge.test/owner/repo/pulls/206")
	if !errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("Merge(...): want forge.ErrMergeBlockedByChecks, got %v", err)
	}
	if gets != 2 {
		t.Errorf("pull fetched %d times, want 2 (classify, then recheck)", gets)
	}
}

// A merge that lands while the conflict check settles shows up only on the
// recheck; that is success, not a conflict (issue #6001).
func TestMerge_MergedOnRecheck_Succeeds(t *testing.T) {
	var gets int
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			gets++
			w.Write([]byte(pullJSON(206, "open", gets > 1, false, false, "add feature", "agent/issue-206", "abc123", "main")))
		default:
			http.NotFound(w, r)
		}
	})
	if err := cf.Merge("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("Merge(...): want nil for a PR merged by the recheck, got %v", err)
	}
	if gets != 2 {
		t.Errorf("pull fetched %d times, want 2", gets)
	}
}

// A failed recheck read must surface as a lookup error, never as a conflict
// guessed from the first read (issue #6001).
func TestMerge_RecheckLookupFails_NotClassified(t *testing.T) {
	var gets int
	cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			gets++
			if gets > 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(pullJSON(206, "open", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
		default:
			http.NotFound(w, r)
		}
	})
	err := cf.Merge("https://forge.test/owner/repo/pulls/206")
	if err == nil {
		t.Fatal("Merge(...): want an error when the recheck lookup fails, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) || errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("Merge(...): failed lookup must not classify as a sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "PR lookup failed") {
		t.Errorf("Merge(...) error = %v, want it to contain %q", err, "PR lookup failed")
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

// A merge that landed by another path between the gate and the POST makes the
// endpoint refuse; the PR reporting merged is success, not a conflict.
func TestMerge_RefusedButAlreadyMerged_Succeeds(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cf := newMergeTestForge(t, "", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(status)
				case http.MethodGet:
					w.Write([]byte(pullJSON(206, "closed", true, false, false, "add feature", "agent/issue-206", "abc123", "main")))
				default:
					http.NotFound(w, r)
				}
			})
			if err := cf.Merge("https://forge.test/owner/repo/pulls/206"); err != nil {
				t.Fatalf("Merge(...): want nil for an already-merged PR, got %v", err)
			}
		})
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
