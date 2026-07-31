package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// prReader is a local, minimal interface covering exactly the PR-object read
// methods this slice adds to forgejoCodeForge. It lets these tests assert
// the concrete adapter satisfies the growing PR surface without asserting
// the full forge.PRForge interface, which forgejoCodeForge does not yet
// fully implement (later slices add the remaining methods, at which point a
// direct forge.PRForge assertion becomes possible).
type prReader interface {
	PRState(url string) (forge.PRState, error)
	HeadCommitSHA(url string) (string, error)
	Mergeable(url string) (forge.MergeableState, error)
	OpenPRForBranch(branch string) (forge.PR, bool, error)
	PRForBranch(branch string) (string, bool, error)
	CheckState(url string) (forge.RollupState, error)
	FailureDetail(url string) (string, error)
	ListPRFiles(url string) ([]string, error)
	NeedsUpdate(url string) (bool, error)
	CanAutoMerge() (bool, error)
	EnqueueAutoMerge(prURL string) error
	MarkReady(prURL string) error
	MarkDraft(prURL string) error
}

// newPullServer stands in for the Forgejo REST API's pull endpoints: a
// single GET /pulls/{index} for a fixed pull payload, and GET /pulls for
// list-based lookups (OpenPRForBranch/PRForBranch), scripted by the caller's
// handler.
func newPRForgeTestForge(t *testing.T, handler http.HandlerFunc) prReader {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	return pr
}

func pullJSON(number int, state string, merged, mergeable, draft bool, title, headRef, headSHA, baseRef string) string {
	payload := map[string]any{
		"number":    number,
		"html_url":  "https://forge.test/owner/repo/pulls/" + strconv.Itoa(number),
		"state":     state,
		"merged":    merged,
		"mergeable": mergeable,
		"draft":     draft,
		"title":     title,
		"head":      map[string]any{"ref": headRef, "sha": headSHA},
		"base":      map[string]any{"ref": baseRef, "sha": "basesha"},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// TestPRState_Open verifies PRState maps an open, unmerged pull to
// forge.PROpen.
func TestPRState_Open(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/pulls/206" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
	})
	got, err := pr.PRState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("PRState(...) unexpected error: %v", err)
	}
	if got != forge.PROpen {
		t.Fatalf("PRState(...) = %q, want %q", got, forge.PROpen)
	}
}

// TestPRState_Closed verifies PRState maps a closed, unmerged pull to
// forge.PRClosed.
func TestPRState_Closed(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pullJSON(206, "closed", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
	})
	got, err := pr.PRState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("PRState(...) unexpected error: %v", err)
	}
	if got != forge.PRClosed {
		t.Fatalf("PRState(...) = %q, want %q", got, forge.PRClosed)
	}
}

// TestPRState_Merged verifies PRState maps a merged pull to forge.PRMerged
// regardless of its raw state string (Forgejo reports merged pulls as
// state=closed, merged=true).
func TestPRState_Merged(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pullJSON(206, "closed", true, false, false, "add feature", "agent/issue-206", "abc123", "main")))
	})
	got, err := pr.PRState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("PRState(...) unexpected error: %v", err)
	}
	if got != forge.PRMerged {
		t.Fatalf("PRState(...) = %q, want %q", got, forge.PRMerged)
	}
}

// TestHeadCommitSHA verifies HeadCommitSHA returns the pull's head.sha.
func TestHeadCommitSHA(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "deadbeef", "main")))
	})
	got, err := pr.HeadCommitSHA("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("HeadCommitSHA(...) unexpected error: %v", err)
	}
	if got != "deadbeef" {
		t.Fatalf("HeadCommitSHA(...) = %q, want %q", got, "deadbeef")
	}
}

// TestMergeable_True verifies Mergeable maps mergeable=true to
// forge.MergeableMergeable.
func TestMergeable_True(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
	})
	got, err := pr.Mergeable("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("Mergeable(...) unexpected error: %v", err)
	}
	if got != forge.MergeableMergeable {
		t.Fatalf("Mergeable(...) = %q, want %q", got, forge.MergeableMergeable)
	}
}

// TestMergeable_False verifies Mergeable maps mergeable=false to
// forge.MergeableConflicting.
func TestMergeable_False(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pullJSON(206, "open", false, false, false, "add feature", "agent/issue-206", "abc123", "main")))
	})
	got, err := pr.Mergeable("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("Mergeable(...) unexpected error: %v", err)
	}
	if got != forge.MergeableConflicting {
		t.Fatalf("Mergeable(...) = %q, want %q", got, forge.MergeableConflicting)
	}
}

// TestOpenPRForBranch_Found verifies OpenPRForBranch returns the open,
// non-draft pull matching branch.
func TestOpenPRForBranch_Found(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/pulls" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != "open" {
			t.Fatalf("state query = %q, want %q", r.URL.Query().Get("state"), "open")
		}
		w.Write([]byte("[" + pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main") + "]"))
	})
	got, ok, err := pr.OpenPRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("OpenPRForBranch(...) unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("OpenPRForBranch(...) ok = false, want true")
	}
	if got.URL != "https://forge.test/owner/repo/pulls/206" {
		t.Fatalf("OpenPRForBranch(...) URL = %q, want %q", got.URL, "https://forge.test/owner/repo/pulls/206")
	}
	if got.IsDraft {
		t.Fatal("OpenPRForBranch(...) IsDraft = true, want false")
	}
}

// TestOpenPRForBranch_DraftSkipped verifies OpenPRForBranch does not adopt a
// draft pull (title-prefixed "WIP:" or draft=true), even when its head
// branch matches.
func TestOpenPRForBranch_DraftSkipped(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[" + pullJSON(206, "open", false, true, true, "WIP: add feature", "agent/issue-206", "abc123", "main") + "]"))
	})
	_, ok, err := pr.OpenPRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("OpenPRForBranch(...) unexpected error: %v", err)
	}
	if ok {
		t.Fatal("OpenPRForBranch(...) ok = true, want false (draft pull must not be adopted)")
	}
}

// TestOpenPRForBranch_Absent verifies OpenPRForBranch reports ok=false when
// no open pull's head matches branch.
func TestOpenPRForBranch_Absent(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[]"))
	})
	_, ok, err := pr.OpenPRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("OpenPRForBranch(...) unexpected error: %v", err)
	}
	if ok {
		t.Fatal("OpenPRForBranch(...) ok = true, want false")
	}
}

// TestPRForBranch_Found verifies PRForBranch returns the URL of any pull
// (regardless of state or draft) whose head matches branch.
func TestPRForBranch_Found(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "all" {
			t.Fatalf("state query = %q, want %q", r.URL.Query().Get("state"), "all")
		}
		w.Write([]byte("[" + pullJSON(206, "closed", true, false, false, "add feature", "agent/issue-206", "abc123", "main") + "]"))
	})
	got, ok, err := pr.PRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("PRForBranch(...) unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("PRForBranch(...) ok = false, want true")
	}
	if got != "https://forge.test/owner/repo/pulls/206" {
		t.Fatalf("PRForBranch(...) = %q, want %q", got, "https://forge.test/owner/repo/pulls/206")
	}
}

// TestPRForBranch_Absent verifies PRForBranch reports ok=false when no
// pull's head matches branch.
func TestPRForBranch_Absent(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[]"))
	})
	_, ok, err := pr.PRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("PRForBranch(...) unexpected error: %v", err)
	}
	if ok {
		t.Fatal("PRForBranch(...) ok = true, want false")
	}
}

// pullHandler returns a handler serving GET /pulls/206 with a fixed pull
// (head sha "abc123", head ref "agent/issue-206", base ref "main"), and
// delegating any other path to next — the shared fixture the CheckState/
// FailureDetail/ListPRFiles/NeedsUpdate tests below layer their own
// commit-status/compare/files handler on top of.
func pullHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/pulls/206" {
			w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
			return
		}
		next(w, r)
	}
}

// TestCheckState_Success verifies CheckState maps a combined status of
// state=success to forge.StateSuccess.
func TestCheckState_Success(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/commits/abc123/status" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"state":"success","total_count":2}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StateSuccess {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StateSuccess)
	}
}

// TestCheckState_Pending verifies CheckState maps state=pending to
// forge.StatePending.
func TestCheckState_Pending(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"pending","total_count":1}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StatePending {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StatePending)
	}
}

// TestCheckState_Failure verifies CheckState maps state=failure to
// forge.StateFailure.
func TestCheckState_Failure(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"failure","total_count":1}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StateFailure {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StateFailure)
	}
}

// TestCheckState_Error verifies CheckState maps state=error to
// forge.StateError.
func TestCheckState_Error(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"error","total_count":1}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StateError {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StateError)
	}
}

// TestCheckState_NoneEmptyState verifies CheckState maps an empty state
// string to forge.StateNone.
func TestCheckState_NoneEmptyState(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"","total_count":0}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StateNone {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StateNone)
	}
}

// TestCheckState_NoneZeroTotalCount verifies CheckState maps a zero
// total_count to forge.StateNone even when state carries a nonempty value —
// e.g. a commit with no statuses registered at all.
func TestCheckState_NoneZeroTotalCount(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"state":"success","total_count":0}`))
	}))
	got, err := pr.CheckState("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("CheckState(...) unexpected error: %v", err)
	}
	if got != forge.StateNone {
		t.Fatalf("CheckState(...) = %q, want %q", got, forge.StateNone)
	}
}

// TestFailureDetail_Empty verifies FailureDetail returns "" when every
// status on the head commit is passing.
func TestFailureDetail_Empty(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/commits/abc123/statuses" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"context":"build","state":"success","description":"all good"}]`))
	}))
	got, err := pr.FailureDetail("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("FailureDetail(...) unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("FailureDetail(...) = %q, want empty", got)
	}
}

// TestFailureDetail_RendersFailingStatus verifies FailureDetail renders a
// failing status's context, upper-cased state, and description, while
// omitting a passing status entirely.
func TestFailureDetail_RendersFailingStatus(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"context":"build","state":"failure","description":"exit status 1"},
			{"context":"lint","state":"success","description":"clean"}
		]`))
	}))
	got, err := pr.FailureDetail("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("FailureDetail(...) unexpected error: %v", err)
	}
	if !strings.Contains(got, "build") {
		t.Fatalf("FailureDetail(...) = %q, want it to contain %q", got, "build")
	}
	if !strings.Contains(got, "FAILURE") {
		t.Fatalf("FailureDetail(...) = %q, want it to contain %q", got, "FAILURE")
	}
	if !strings.Contains(got, "exit status 1") {
		t.Fatalf("FailureDetail(...) = %q, want it to contain %q", got, "exit status 1")
	}
	if strings.Contains(got, "lint") {
		t.Fatalf("FailureDetail(...) = %q, want it to omit the passing %q status", got, "lint")
	}
}

// TestFailureDetail_Bounded verifies FailureDetail truncates its rendered
// excerpt to at most 4000 bytes even when many failing statuses, each with a
// long description, would otherwise produce a larger excerpt.
func TestFailureDetail_Bounded(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		longDesc := strings.Repeat("x", 500)
		var statuses []map[string]any
		for i := 0; i < 20; i++ {
			statuses = append(statuses, map[string]any{
				"context":     "check-" + strconv.Itoa(i),
				"state":       "failure",
				"description": longDesc,
			})
		}
		b, _ := json.Marshal(statuses)
		w.Write(b)
	}))
	got, err := pr.FailureDetail("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("FailureDetail(...) unexpected error: %v", err)
	}
	if len(got) > 4000 {
		t.Fatalf("FailureDetail(...) length = %d, want <= 4000", len(got))
	}
}

// TestListPRFiles verifies ListPRFiles returns every filename in the pull's
// changed-files listing.
func TestListPRFiles(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/pulls/206/files" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"filename":"a.go"},{"filename":"b/c.go"}]`))
	})
	got, err := pr.ListPRFiles("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("ListPRFiles(...) unexpected error: %v", err)
	}
	want := []string{"a.go", "b/c.go"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ListPRFiles(...) = %v, want %v", got, want)
	}
}

// TestNeedsUpdate_True verifies NeedsUpdate reports true when the compare
// API reports commits the head branch has not yet incorporated.
func TestNeedsUpdate_True(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/repos/owner/repo/compare/") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"total_commits":3}`))
	}))
	got, err := pr.NeedsUpdate("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("NeedsUpdate(...) unexpected error: %v", err)
	}
	if !got {
		t.Fatal("NeedsUpdate(...) = false, want true")
	}
}

// TestNeedsUpdate_False verifies NeedsUpdate reports false when the compare
// API reports zero commits the head branch is missing from base.
func TestNeedsUpdate_False(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_commits":0}`))
	}))
	got, err := pr.NeedsUpdate("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("NeedsUpdate(...) unexpected error: %v", err)
	}
	if got {
		t.Fatal("NeedsUpdate(...) = true, want false")
	}
}

// TestCanAutoMerge_True verifies CanAutoMerge reports true when the repo
// permits at least one merge style (here, only allow_rebase is set).
func TestCanAutoMerge_True(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"allow_merge_commits":false,"allow_rebase":true,"allow_squash_merge":false}`))
	})
	got, err := pr.CanAutoMerge()
	if err != nil {
		t.Fatalf("CanAutoMerge() unexpected error: %v", err)
	}
	if !got {
		t.Fatal("CanAutoMerge() = false, want true")
	}
}

// TestCanAutoMerge_False verifies CanAutoMerge reports false when the repo
// permits no merge style at all.
func TestCanAutoMerge_False(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"allow_merge_commits":false,"allow_rebase":false,"allow_squash_merge":false}`))
	})
	got, err := pr.CanAutoMerge()
	if err != nil {
		t.Fatalf("CanAutoMerge() unexpected error: %v", err)
	}
	if got {
		t.Fatal("CanAutoMerge() = true, want false")
	}
}

// TestEnqueueAutoMerge_PostsMergeWhenChecksSucceed verifies EnqueueAutoMerge
// POSTs to the pull's merge endpoint with merge_when_checks_succeed=true, so
// Forgejo enqueues native scheduled merge-when-checks-succeed rather than
// merging immediately.
func TestEnqueueAutoMerge_PostsMergeWhenChecksSucceed(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.EnqueueAutoMerge("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("EnqueueAutoMerge(...) unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/pulls/206/merge" {
		t.Errorf("path = %q, want %q", gotPath, "/api/v1/repos/owner/repo/pulls/206/merge")
	}
	if gotBody["merge_when_checks_succeed"] != true {
		t.Errorf("body[merge_when_checks_succeed] = %v, want true", gotBody["merge_when_checks_succeed"])
	}
}

// TestMarkReady_StripsWIPPrefix verifies MarkReady PATCHes the title with
// the WIP prefix stripped when the PR is currently a draft.
func TestMarkReady_StripsWIPPrefix(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, true, "WIP: add feature", "agent/issue-206", "abc123", "main")))
		case r.Method == http.MethodPatch:
			gotPath = r.URL.Path
			gotMethod = r.Method
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.MarkReady("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("MarkReady(...) unexpected error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/pulls/206" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v1/repos/owner/repo/pulls/206")
	}
	if gotBody["title"] != "add feature" {
		t.Fatalf("body[title] = %v, want %q", gotBody["title"], "add feature")
	}
}

// TestMarkReady_AlreadyReadyNoOp verifies MarkReady issues no PATCH and
// returns nil when the PR is already not a draft.
func TestMarkReady_AlreadyReadyNoOp(t *testing.T) {
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
		case r.Method == http.MethodPatch:
			patched = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.MarkReady("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("MarkReady(...) unexpected error: %v", err)
	}
	if patched {
		t.Fatal("MarkReady(...) issued a PATCH for an already-ready PR, want no-op")
	}
}

// TestMarkDraft_AddsWIPPrefix verifies MarkDraft PATCHes the title with a
// leading "WIP: " when the PR is currently ready (not draft).
func TestMarkDraft_AddsWIPPrefix(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
		case r.Method == http.MethodPatch:
			gotPath = r.URL.Path
			gotMethod = r.Method
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.MarkDraft("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("MarkDraft(...) unexpected error: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/pulls/206" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v1/repos/owner/repo/pulls/206")
	}
	if gotBody["title"] != "WIP: add feature" {
		t.Fatalf("body[title] = %v, want %q", gotBody["title"], "WIP: add feature")
	}
}

// TestMarkDraft_AlreadyDraftNoOp verifies MarkDraft issues no PATCH and
// returns nil when the PR is already a draft (WIP-titled).
func TestMarkDraft_AlreadyDraftNoOp(t *testing.T) {
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, true, "WIP: add feature", "agent/issue-206", "abc123", "main")))
		case r.Method == http.MethodPatch:
			patched = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		GitRemoteURL: "unused",
		BranchPrefix: "agent/issue-",
	})
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.MarkDraft("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("MarkDraft(...) unexpected error: %v", err)
	}
	if patched {
		t.Fatal("MarkDraft(...) issued a PATCH for an already-draft PR, want no-op")
	}
}
