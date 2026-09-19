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

// prReader covers only the PR-object methods forgejoCodeForge implements so
// far. Asserting forge.PRForge directly would fail until later slices add the
// remaining methods.
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

func newPRForgeTestForge(t *testing.T, handler http.HandlerFunc) prReader {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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

// Forgejo reports a merged pull as state=closed, merged=true, so PRState must
// read merged rather than the raw state string.
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
}

// OpenPRForBranch must adopt a draft pull exactly as it adopts a non-draft one
// (issue #2408). The returned forge.PR no longer carries draft status, so this
// asserts only the adoption.
func TestOpenPRForBranch_AdoptsDraftPR(t *testing.T) {
	pr := newPRForgeTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[" + pullJSON(206, "open", false, true, true, "WIP: add feature", "agent/issue-206", "abc123", "main") + "]"))
	})
	got, ok, err := pr.OpenPRForBranch("agent/issue-206")
	if err != nil {
		t.Fatalf("OpenPRForBranch(...) unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("OpenPRForBranch(...) ok = false, want true (a draft pull must be adopted)")
	}
	if got.URL != "https://forge.test/owner/repo/pulls/206" {
		t.Fatalf("OpenPRForBranch(...) URL = %q, want %q", got.URL, "https://forge.test/owner/repo/pulls/206")
	}
}

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

// Unlike OpenPRForBranch, PRForBranch matches a pull in any state, so the
// fixture deliberately serves a closed and merged one.
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

// Each pull gets a distinct head ref "branch-N" so a test can target one that
// lands on a specific page.
func forgejoPullsPage(start, count int) string {
	var parts []string
	for i := 0; i < count; i++ {
		n := start + i
		parts = append(parts, pullJSON(n, "open", false, true, false, "add feature", "branch-"+strconv.Itoa(n), "sha"+strconv.Itoa(n), "main"))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// listPulls once fetched a single bounded page (issue #2265). The target branch
// sits only on the short second page, so finding it proves Paginate merged both
// pages instead of consulting the first.
func TestOpenPRForBranch_WalksAllPages(t *testing.T) {
	const pageSize = forge.ResultPageLimit
	var gotPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/pulls" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if state := q.Get("state"); state != "open" {
			t.Errorf("state query param = %q, want %q", state, "open")
		}
		if limit := q.Get("limit"); limit != strconv.Itoa(pageSize) {
			t.Errorf("limit query param = %q, want %q", limit, strconv.Itoa(pageSize))
		}
		page, err := strconv.Atoi(q.Get("page"))
		if err != nil {
			t.Fatalf("invalid page query param: %v", err)
		}
		gotPages = append(gotPages, q.Get("page"))
		w.WriteHeader(http.StatusOK)
		switch page {
		case 1:
			w.Write([]byte(forgejoPullsPage(1, pageSize)))
		case 2:
			// A short page (2 < pageSize) is how the server tells Paginate the
			// walk is done.
			w.Write([]byte(forgejoPullsPage(pageSize+1, 2)))
		default:
			t.Errorf("server received request for page %d, want no request beyond the short page 2", page)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}

	got, ok, err := pr.OpenPRForBranch("branch-" + strconv.Itoa(pageSize+2))
	if err != nil {
		t.Fatalf("OpenPRForBranch(...) unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("OpenPRForBranch(...) ok = false, want true (branch is on the second page)")
	}
	wantURL := "https://forge.test/owner/repo/pulls/" + strconv.Itoa(pageSize+2)
	if got.URL != wantURL {
		t.Fatalf("OpenPRForBranch(...) URL = %q, want %q", got.URL, wantURL)
	}
	if len(gotPages) != 2 || gotPages[0] != "1" || gotPages[1] != "2" {
		t.Fatalf("server saw page requests %v, want exactly [1 2]", gotPages)
	}
}

// pullHandler serves a fixed pull (head sha "abc123", head ref
// "agent/issue-206", base ref "main") and delegates every other path to next,
// so the CheckState, FailureDetail and NeedsUpdate tests below script only
// their own endpoint.
func pullHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/pulls/206" {
			w.Write([]byte(pullJSON(206, "open", false, true, false, "add feature", "agent/issue-206", "abc123", "main")))
			return
		}
		next(w, r)
	}
}

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

// A commit with no statuses registered can still report a nonempty state, so
// total_count=0 must win and map to forge.StateNone.
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

// The fixture renders well past 4000 bytes on purpose. Shrink it and the
// truncation never fires, so the test passes while checking nothing.
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

func TestNeedsUpdate_True(t *testing.T) {
	pr := newPRForgeTestForge(t, pullHandler(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/repos/owner/repo/compare/") {
			http.NotFound(w, r)
			return
		}
		// Forgejo's compare endpoint counts only commits ahead, so the refs go
		// in head...base order and total_commits counts the behind set instead.
		if got := r.URL.Path; !strings.HasSuffix(got, "/compare/agent/issue-206...main") {
			t.Errorf("compare path = %q, want head...base order .../compare/agent/issue-206...main", got)
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

// The merge_when_checks_succeed flag is what makes Forgejo schedule the merge.
// Without it the same POST merges the pull immediately.
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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

// MarkReady must gate on isDraftPull (the draft field or either WIP-title
// convention), not isDraftTitle's narrower "WIP:"-only check. Gating on the
// narrow check strands a "[WIP]:"-titled draft PR that OpenPRForBranch adopted.
func TestMarkReady_ActsOnDraftFieldWithBracketedWIPTitle(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, true, "[WIP]: add feature", "agent/issue-206", "abc123", "main")))
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
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

// The symmetric case to TestMarkReady_ActsOnDraftFieldWithBracketedWIPTitle.
// MarkDraft must gate on the same isDraftPull predicate, or a pull already
// draft by field but plainly titled gets redundantly PATCHed back to draft.
func TestMarkDraft_AlreadyDraftFieldNoOpWithoutWIPTitle(t *testing.T) {
	patched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/pulls/206":
			w.Write([]byte(pullJSON(206, "open", false, true, true, "add feature", "agent/issue-206", "abc123", "main")))
		case r.Method == http.MethodPatch:
			patched = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BranchPrefix: "agent/issue-",
	}, nil, "unused")
	pr, ok := cf.(prReader)
	if !ok {
		t.Fatalf("forgejoCodeForge does not satisfy prReader (methods not yet implemented)")
	}
	if err := pr.MarkDraft("https://forge.test/owner/repo/pulls/206"); err != nil {
		t.Fatalf("MarkDraft(...) unexpected error: %v", err)
	}
	if patched {
		t.Fatal("MarkDraft(...) issued a PATCH for a pull already draft by field, want no-op")
	}
}
