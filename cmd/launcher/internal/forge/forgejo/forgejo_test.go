package forgejo_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/rest"
)

// Forgejo implements only the IssueTracker seam (ADR 0013); code still lands
// via the github CodeForge.
func TestForgejoClient_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = forgejo.NewForgejoClient(forgejo.ForgejoConfig{})
}

// Forgejo's issue-dependencies API is a genuine bidirectional native
// relationship, with a separate "blocks" endpoint for the reverse direction.
func TestForgejoClient_ImplementsBlockersLister(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.BlockersLister); !ok {
		t.Error("forgejoClient does not satisfy forge.BlockersLister, want it implemented")
	}
}

// backlogDedupIndex's capability path (issue #3873) needs a state-scoped,
// newest-first, body-populated scan, not ListOpenIssues's open-only page.
func TestForgejoClient_ImplementsLabeledBacklogLister(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.LabeledBacklogLister); !ok {
		t.Error("forgejoClient does not satisfy forge.LabeledBacklogLister, want it implemented")
	}
}

// Forgejo's whole DispatchState space reduces to one DispatchLabels value (no
// status-mapping blend like jira), so PickIssue's double-box guard (#1742) can
// shortcut it.
func TestForgejoClient_ImplementsLabeledTracker(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.LabeledTracker); !ok {
		t.Error("forgejoClient does not satisfy forge.LabeledTracker, want it implemented")
	}
}

func TestForgejoClient_Probe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	slug, err := fc.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "owner/repo" {
		t.Errorf("Probe() = %q, want %q", slug, "owner/repo")
	}
}

func TestForgejoClient_Probe_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "bad-token"})
	if _, err := fc.Probe(); !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe() error = %v, want ErrAuthFailure", err)
	}
}

func TestForgejoClient_Probe_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if _, err := fc.Probe(); !errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("Probe() error = %v, want ErrRepoNotFound", err)
	}
}

// Both the sentinel and the wire status must survive so a caller can tell an
// unmapped status from a genuine 404. Uses 400 rather than a 5xx: every 5xx is
// transient per rest's isTransientStatus, so a 5xx here would sleep through a
// real LinearBackoff before Do gives up and returns.
func TestForgejoClient_Probe_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	_, err := fc.Probe()
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("Probe() error = %v, want ErrRepoNotFound", err)
	}
	var statusErr rest.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Probe() error = %v, want rest.StatusError in chain", err)
	}
	if statusErr.Status != http.StatusBadRequest {
		t.Errorf("StatusError.Status = %d, want %d", statusErr.Status, http.StatusBadRequest)
	}
}

func TestForgejoClient_Probe_DecodeFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	_, err := fc.Probe()
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("Probe() error = %v, want ErrRepoNotFound", err)
	}
	var decodeErr rest.DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Probe() error = %v, want rest.DecodeError in chain", err)
	}
}

func TestForgejoClient_Comment_PostsBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if err := fc.Comment("42", "hello from the agent"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/42/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "hello from the agent" {
		t.Errorf("body = %v", gotBody)
	}
}

// The forgejo adapter must offer the same read-only issue-filing relay channel
// the github adapter implements (issue #1964).
func TestForgejoClient_ImplementsHostPostedIssueFiler(t *testing.T) {
	if _, ok := forgejo.NewForgejoClient(forgejo.ForgejoConfig{}).(forge.HostPostedIssueFiler); !ok {
		t.Error("forgejoClient does not satisfy forge.HostPostedIssueFiler, want it implemented")
	}
}

func TestForgejoClient_PostIssue_CreatesAndReturnsURL(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	labelsCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/labels") {
			labelsCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number":99,"html_url":"https://codeberg.org/owner/repo/issues/99"}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	filer := fc.(forge.HostPostedIssueFiler)
	url, err := filer.PostIssue("a title", "a body", nil)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if url != "https://codeberg.org/owner/repo/issues/99" {
		t.Errorf("url = %q", url)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["title"] != "a title" || gotBody["body"] != "a body" {
		t.Errorf("body = %v", gotBody)
	}
	if labelsCalled {
		t.Error("labels endpoint called with no labels given")
	}
}

// Forgejo's create endpoint wants label IDs, so PostIssue creates the issue
// first and then applies labels by name through the replace-all-labels PUT,
// which avoids ID bookkeeping.
func TestForgejoClient_PostIssue_AppliesLabels(t *testing.T) {
	var labelsPath, labelsMethod string
	var labelsBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/labels") {
			labelsPath = r.URL.Path
			labelsMethod = r.Method
			json.NewDecoder(r.Body).Decode(&labelsBody)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number":100,"html_url":"https://codeberg.org/owner/repo/issues/100"}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	filer := fc.(forge.HostPostedIssueFiler)
	url, err := filer.PostIssue("a title", "a body", []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if url != "https://codeberg.org/owner/repo/issues/100" {
		t.Errorf("url = %q", url)
	}
	if labelsMethod != http.MethodPut {
		t.Errorf("labels method = %q, want PUT", labelsMethod)
	}
	if labelsPath != "/api/v1/repos/owner/repo/issues/100/labels" {
		t.Errorf("labels path = %q", labelsPath)
	}
	gotLabels, _ := labelsBody["labels"].([]any)
	if len(gotLabels) != 1 || gotLabels[0] != "agent-review-finding" {
		t.Errorf("labels body = %v", labelsBody)
	}
}

func TestForgejoClient_ListLabels_ReturnsRepoLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/labels" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		serveLabels(w, r, `[{"name":"ready-for-agent"},{"name":"agent-in-progress"}]`)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labels, err := fc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(labels) != 2 || labels[0] != "ready-for-agent" || labels[1] != "agent-in-progress" {
		t.Errorf("labels = %v", labels)
	}
}

// forgejoLabelsPage renders count labels as a Forgejo labels-list JSON page,
// named prefix0 through prefix<count-1>.
func forgejoLabelsPage(prefix string, count int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"%s%d"}`, prefix, i)
	}
	b.WriteByte(']')
	return b.String()
}

// serveLabels writes body as /labels page 1 and an empty page after it.
// ListLabels walks until an empty page, so a fake re-serving body for every
// page would loop forever.
func serveLabels(w http.ResponseWriter, r *http.Request, body string) {
	w.WriteHeader(http.StatusOK)
	if page := r.URL.Query().Get("page"); page != "" && page != "1" {
		w.Write([]byte(`[]`))
		return
	}
	w.Write([]byte(body))
}

// mixedLabelFixture is a two-issue /issues page: issue 5 carries label, issue
// 6 carries nothing. Shared by the server-ignored-labels-param tests, whose
// point is that the client-side re-filter (issue #3952) keeps 5 and drops 6
// regardless of what Forgejo's labels= filtering actually did server-side.
func mixedLabelFixture(label string) []byte {
	return []byte(`[` +
		`{"number":5,"title":"labelled","body":"","state":"open","labels":[{"name":"` + label + `"}]},` +
		`{"number":6,"title":"unlabelled","body":"","state":"open","labels":[]}` +
		`]`)
}

// assertOnlyIssue5 checks the mixedLabelFixture re-filter kept issue 5 and
// dropped issue 6.
func assertOnlyIssue5(t *testing.T, name string, issues []forge.Issue) {
	t.Helper()
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("%s = %+v, want only the labelled issue", name, issues)
	}
}

// forgejoLabelsServerCap mirrors Forgejo's default MAX_RESPONSE_ITEMS page cap.
const forgejoLabelsServerCap = 50

// serveCappedLabelsPage renders a fake /labels endpoint that caps page 1 at
// forgejoLabelsServerCap (named via prefix), serves page2Body on page 2, and an
// empty page after that.
func serveCappedLabelsPage(w http.ResponseWriter, r *http.Request, prefix, page2Body string) {
	switch page := r.URL.Query().Get("page"); page {
	case "1", "":
		w.Write([]byte(forgejoLabelsPage(prefix, forgejoLabelsServerCap)))
	case "2":
		w.Write([]byte(page2Body))
	default:
		w.Write([]byte(`[]`))
	}
}

// ListLabels must walk every page through rest.Client.Paginate until one
// comes back empty (issue #2265, #3953): Forgejo caps the page size at
// MAX_RESPONSE_ITEMS (default 50) whatever limit is requested, so a short
// page 1 is not the last page and a label past it must still be seen.
func TestForgejoClient_ListLabels_WalksAllPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/labels" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		serveCappedLabelsPage(w, r, "page1-", `[{"name":"page2-extra"},{"name":"agent-review-finding"}]`)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labels, err := fc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}

	wantCount := forgejoLabelsServerCap + 2
	if len(labels) != wantCount {
		t.Fatalf("ListLabels returned %d labels, want %d (all pages merged despite the server-capped page size)", len(labels), wantCount)
	}
	if !slices.Contains(labels, "agent-review-finding") {
		t.Fatalf("labels = %v, want the page-2 finding label present", labels)
	}
}

// ListLabels must not loop forever against a server or proxy that ignores
// the ?page query param and always re-serves the same non-empty page: a
// repeated first label name across pages is the cheap tell it stops on.
func TestForgejoClient_ListLabels_StopsOnRepeatedFirstLabelAcrossPages(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 5 {
			t.Errorf("ListLabels made more than 5 requests, want the repeated-page guard to stop it")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`[{"name":"ready-for-agent"},{"name":"agent-in-progress"}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labels, err := fc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	want := []string{"ready-for-agent", "agent-in-progress"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("ListLabels = %v, want %v with no duplicates from the repeated page", labels, want)
	}
}

// ListIssuesWithLabels must see a finding label that only shows up on
// /labels page 2 because the server caps the page size below
// forge.ResultPageLimit (issue #3944): if ListLabels stopped as soon as a
// page came back short, this label would look undefined and get silently
// dropped from the dedup scan instead of getting its own /issues query.
func TestForgejoClient_ListIssuesWithLabels_SeesLabelOnServerCappedLabelsPage2(t *testing.T) {
	var issuesRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveCappedLabelsPage(w, r, "page1-", `[{"name":"agent-review-finding"}]`)
			return
		}
		issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if len(issuesRequests) != 1 || issuesRequests[0] != "agent-review-finding" {
		t.Fatalf("issues requests = %+v, want exactly one for the page-2 label", issuesRequests)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("issues = %+v", issues)
	}
}

// Forgejo's label-creation endpoint wants a leading hash on the color, unlike
// the color argument's own bare-hex convention.
func TestForgejoClient_CreateLabel_PostsHexColorWithHash(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/labels" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if err := fc.CreateLabel("agent-failed", "desc", "d93f0b"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if gotBody["name"] != "agent-failed" || gotBody["description"] != "desc" || gotBody["color"] != "#d93f0b" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestForgejoClient_DefaultBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer srv.Close()

	// A unit test cannot reach the real codeberg.org, so this asserts the
	// trailing-slash stripping instead, which shares a code path with the
	// default-BaseURL assignment.
	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL + "/", Repo: "owner/repo", Token: "tok"})
	if _, err := fc.Probe(); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if gotPath != "/api/v1/repos/owner/repo" {
		t.Errorf("path = %q, want trailing slash stripped from BaseURL", gotPath)
	}
}

// The fixture repeats issue 42 because Forgejo's dependency API has no
// documented uniqueness guarantee, so dependencyIDs dedups defensively.
func TestForgejoClient_BlocksOf_ReturnsNativeBlocking(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":42},{"number":43},{"number":42}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.BlockersLister)
	blocks, err := fc.BlocksOf("7")
	if err != nil {
		t.Fatalf("BlocksOf: %v", err)
	}
	want := []forge.Dependency{{ID: "42", Source: forge.DepSourceNative}, {ID: "43", Source: forge.DepSourceNative}}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("BlocksOf = %v, want %v", blocks, want)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/7/blocks" {
		t.Errorf("path = %q, want the /blocks endpoint", gotPath)
	}
}

// BlocksOf has nothing to fall back to, so a native lookup failure must reach
// the caller rather than degrade.
func TestForgejoClient_BlocksOf_PropagatesNativeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.BlockersLister)
	_, err := fc.BlocksOf("7")
	if err == nil {
		t.Fatal("BlocksOf: want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("BlocksOf error = %q, want it to mention the status", err.Error())
	}
}

// TouchesOf fetches the full issue because Issue()'s payload carries the body,
// unlike the summary list endpoint. Forgejo has no native touch-set concept, so
// the shared forge.ParseTouchPaths grammar parses the body instead.
func TestForgejoClient_TouchesOf_ParsesBodyTouchSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues/10" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"number":10,"title":"t","body":"## Touches\n- lib/env-schema.nix","state":"open","labels":[]}`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	touches, err := fc.TouchesOf("10")
	if err != nil {
		t.Fatalf("TouchesOf: %v", err)
	}
	want := []string{"lib/env-schema.nix"}
	if !reflect.DeepEqual(touches, want) {
		t.Fatalf("TouchesOf = %v, want %v", touches, want)
	}
}

// forgejoIssuesPage renders count issues as a Forgejo issues-list JSON page,
// numbered start through start+count-1.
func forgejoIssuesPage(start, count int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"number":%d,"title":"t","body":"","state":"open","labels":[]}`, start+i)
	}
	b.WriteByte(']')
	return b.String()
}

// listIssues must walk every page through rest.Client.Paginate rather than
// fetch a single bounded page (issue #2265). Page 1 is full and numbered
// descending so the final ascending sort has to be real; the short page 2
// signals the end of the walk.
func TestForgejoClient_ListOpenIssues_WalksAllPages(t *testing.T) {
	const pageSize = forge.ResultPageLimit
	var gotPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
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
			var b strings.Builder
			b.WriteByte('[')
			for i := 0; i < pageSize; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"number":%d,"title":"t","body":"","state":"open","labels":[]}`, pageSize+5-i)
			}
			b.WriteByte(']')
			w.Write([]byte(b.String()))
		case 2:
			w.Write([]byte(forgejoIssuesPage(1, 5)))
		default:
			t.Errorf("server received request for page %d, want no request beyond the short page 2", page)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	issues, err := fc.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}

	wantCount := pageSize + 5
	if len(issues) != wantCount {
		t.Fatalf("ListOpenIssues returned %d issues, want %d (all pages merged)", len(issues), wantCount)
	}
	for i, iss := range issues {
		wantNum := strconv.Itoa(i + 1)
		if iss.Number != wantNum {
			t.Fatalf("issues[%d].Number = %q, want %q (ascending order across merged pages)", i, iss.Number, wantNum)
		}
	}
	if len(gotPages) != 2 || gotPages[0] != "1" || gotPages[1] != "2" {
		t.Fatalf("server saw page requests %v, want exactly [1 2]", gotPages)
	}
}

// A dispatch label absent from the repo's defined set gets no /issues request
// at all (issue #3952): Forgejo's ListIssues handler drops an unresolved
// labels filter entirely rather than erroring, so querying it would return
// every open issue instead of none.
func TestForgejoClient_ListIssues_SkipsLabelNotDefinedOnRepo(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"unrelated-label"}]`)
			return
		}
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":5,"title":"t","body":"b","state":"open","labels":[]}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: srv.URL, Repo: "owner/repo", Token: "tok",
		Labels: forge.DispatchLabels{Dispatchable: "ready-for-agent"},
	})
	issues, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("ListIssues(Dispatchable) = %+v, want empty", issues)
	}
	if requested {
		t.Error("ListIssues(Dispatchable): want no /issues request when the label is undefined, got one")
	}
}

// Forgejo silently drops an unresolved labels= filter and returns every
// issue in state rather than erroring or returning none (issue #3952), so
// ListIssues must re-filter client-side even when the label is defined and
// the request goes out.
func TestForgejoClient_ListIssues_FiltersServerIgnoredLabelsParam(t *testing.T) {
	var gotLabelsParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
			return
		}
		gotLabelsParam = r.URL.Query().Get("labels")
		w.WriteHeader(http.StatusOK)
		w.Write(mixedLabelFixture("ready-for-agent"))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: srv.URL, Repo: "owner/repo", Token: "tok",
		Labels: forge.DispatchLabels{Dispatchable: "ready-for-agent"},
	})
	issues, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if gotLabelsParam != "ready-for-agent" {
		t.Fatalf("labels query param = %q, want %q sent even though the fake ignores it", gotLabelsParam, "ready-for-agent")
	}
	assertOnlyIssue5(t, "ListIssues(Dispatchable)", issues)
}

// A ListLabels failure must not become an empty result: it falls back to
// querying /issues without the pre-check, the pre-existing behavior, relying
// on the client-side re-filter (issue #3952) to keep the labelled issues and
// drop the rest of whatever Forgejo actually returns.
func TestForgejoClient_ListIssues_LabelsErrorFallsBackToQueryingAndFiltering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(mixedLabelFixture("ready-for-agent"))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: srv.URL, Repo: "owner/repo", Token: "tok",
		Labels: forge.DispatchLabels{Dispatchable: "ready-for-agent"},
	})
	issues, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	assertOnlyIssue5(t, "ListIssues(Dispatchable)", issues)
}

// ListIssuesWithLabels(...) queries per-label already, but must still shed a
// mislabelled issue Forgejo returns anyway (issue #3952): same backstop,
// different caller.
func TestForgejoClient_ListIssuesWithLabels_FiltersServerIgnoredLabelsParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"agent-review-finding"}]`)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(mixedLabelFixture("agent-review-finding"))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	assertOnlyIssue5(t, "ListIssuesWithLabels", issues)
}

// ListIssuesWithLabels(IssueClosed, ...) must send state=closed, not the
// open-only state ListOpenIssues and ListIssues hardcode (issue #3873): a
// closed finding is a durable triage decision the host must not refile.
func TestForgejoClient_ListIssuesWithLabels_ClosedStateAndLabelQuery(t *testing.T) {
	var gotState, gotLabels string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"agent-review-finding"}]`)
			return
		}
		q := r.URL.Query()
		gotState = q.Get("state")
		gotLabels = q.Get("labels")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":5,"title":"t","body":"b","state":"closed","labels":[{"name":"agent-review-finding"}]}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if gotState != "closed" {
		t.Errorf("state query param = %q, want closed", gotState)
	}
	if gotLabels != "agent-review-finding" {
		t.Errorf("labels query param = %q, want agent-review-finding", gotLabels)
	}
	if len(issues) != 1 || issues[0].Number != "5" || issues[0].Body != "b" {
		t.Fatalf("issues = %+v", issues)
	}
}

// Two labels ANDed by a single "labels" filter would miss an issue carrying
// only one of them, so Forgejo's comma-separated semantics are not
// reliably "any of" (issue #3873 review) -- one query per label, merged and
// de-duplicated by number.
func TestForgejoClient_ListIssuesWithLabels_MergesAndDedupsAcrossLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"agent-review-finding"},{"name":"agent-research-finding"}]`)
			return
		}
		w.WriteHeader(http.StatusOK)
		switch r.URL.Query().Get("labels") {
		case "agent-review-finding":
			w.Write([]byte(`[{"number":5,"title":"t","body":"","state":"open","labels":[{"name":"agent-review-finding"}]},` +
				`{"number":3,"title":"t","body":"","state":"open","labels":[{"name":"agent-review-finding"}]}]`))
		case "agent-research-finding":
			w.Write([]byte(`[{"number":5,"title":"t","body":"","state":"open","labels":[{"name":"agent-review-finding"},{"name":"agent-research-finding"}]},` +
				`{"number":9,"title":"t","body":"","state":"open","labels":[{"name":"agent-research-finding"}]}]`))
		default:
			t.Errorf("unexpected labels query param: %q", r.URL.Query().Get("labels"))
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}

	byNum := make(map[string]bool)
	for _, iss := range issues {
		if byNum[iss.Number] {
			t.Fatalf("issue #%s returned more than once: %+v", iss.Number, issues)
		}
		byNum[iss.Number] = true
	}
	if len(byNum) != 3 {
		t.Fatalf("want 3 distinct issues, got %+v", issues)
	}
	// The merge is newest-first, not the per-label page order.
	if issues[0].Number != "9" || issues[1].Number != "5" || issues[2].Number != "3" {
		t.Fatalf("issues = %+v, want newest-first [9, 5, 3]", issues)
	}
}

// A per-label failure (e.g. the target repo lacks one finding label) must not
// empty the whole dedup index -- the issues the succeeding label returned
// still come back, with a nil error, mirroring the github adapter.
func TestForgejoClient_ListIssuesWithLabels_PartialFailureReturnsSucceededLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"agent-review-finding"},{"name":"agent-research-finding"}]`)
			return
		}
		switch r.URL.Query().Get("labels") {
		case "agent-review-finding":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"number":5,"title":"t","body":"","state":"open","labels":[{"name":"agent-review-finding"}]}]`))
		case "agent-research-finding":
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: want nil error on partial failure, got %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("want the succeeded label's issue [5], got %+v", issues)
	}
}

// When every label's call fails, the failure has to surface: it is the
// signal backlogDedupIndex uses to fall back to intra-run-only dedup.
func TestForgejoClient_ListIssuesWithLabels_AllFailuresReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[{"name":"agent-review-finding"},{"name":"agent-research-finding"}]`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	_, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err == nil {
		t.Fatal("ListIssuesWithLabels: want error when every label fails, got nil")
	}
}

// The same "all failed" signal must surface through the unfiltered fallback
// a /labels outage takes.
func TestForgejoClient_ListIssuesWithLabels_LabelsErrorAndAllIssuesFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	_, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err == nil {
		t.Fatal("ListIssuesWithLabels: want error when /labels and every /issues query fail, got nil")
	}
}

// A state that isn't OPEN or CLOSED must error before any request runs.
func TestForgejoClient_ListIssuesWithLabels_UnsupportedStateErrorsWithoutRequest(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	_, err := fc.ListIssuesWithLabels(forge.IssueMerged, []string{"agent-review-finding"})
	if err == nil {
		t.Fatal("ListIssuesWithLabels(IssueMerged): want error, got nil")
	}
	if requested {
		t.Error("ListIssuesWithLabels(IssueMerged): want no request, got one")
	}
}

// Empty labels is the doctor-advisory-label-missing edge folded to zero
// inputs: no request, no failure, an empty result.
func TestForgejoClient_ListIssuesWithLabels_NoLabelsReturnsEmptyWithoutRequest(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, nil)
	if err != nil {
		t.Fatalf("ListIssuesWithLabels(nil): %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("want empty result, got %+v", issues)
	}
	if requested {
		t.Error("ListIssuesWithLabels(nil): want no request, got one")
	}
}

// A label absent from the repo's defined set gets no /issues request at all:
// Forgejo's ListIssues handler drops an unresolved labels filter entirely
// rather than erroring, so querying it would scan every issue in state.
func TestForgejoClient_ListIssuesWithLabels_SkipsLabelsNotDefinedOnRepo(t *testing.T) {
	for _, state := range []forge.IssueState{forge.IssueOpen, forge.IssueClosed} {
		t.Run(string(state), func(t *testing.T) {
			var issuesRequests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
					serveLabels(w, r, `[{"name":"agent-review-finding"}]`)
					return
				}
				issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`))
			}))
			defer srv.Close()

			fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
			issues, err := fc.ListIssuesWithLabels(state, []string{"agent-review-finding", "agent-missing-finding"})
			if err != nil {
				t.Fatalf("ListIssuesWithLabels: %v", err)
			}
			if len(issuesRequests) != 1 || issuesRequests[0] != "agent-review-finding" {
				t.Fatalf("issues requests = %+v, want exactly one for the defined label", issuesRequests)
			}
			if len(issues) != 1 || issues[0].Number != "5" {
				t.Fatalf("issues = %+v", issues)
			}
		})
	}
}

// When every requested label is undefined on the repo, skipping all of them
// is lossless: the result is empty and no /issues request runs at all.
func TestForgejoClient_ListIssuesWithLabels_AllLabelsUndefinedReturnsEmptyWithoutRequest(t *testing.T) {
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			serveLabels(w, r, `[]`)
			return
		}
		requested = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: want nil error when every label is undefined, got %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("want empty result, got %+v", issues)
	}
	if requested {
		t.Error("want no /issues request when every label is undefined")
	}
}

// A ListLabels failure must not become an empty dedup index: a transient
// labels-endpoint outage falls back to querying every requested label
// unfiltered, the pre-existing behavior. 404 (not 500) keeps the rest
// client's transient-status retry/backoff out of this test.
func TestForgejoClient_ListIssuesWithLabels_LabelsErrorFallsBackToQueryingAllLabels(t *testing.T) {
	var issuesRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/owner/repo/labels" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if len(issuesRequests) != 2 {
		t.Fatalf("issues requests = %+v, want both labels queried unfiltered", issuesRequests)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("issues = %+v", issues)
	}
}

// forgejoCommentsPage renders count comments as a Forgejo comments-list JSON
// page, each body reading "comment <n>" so a test can assert order across
// merged pages.
func forgejoCommentsPage(start, count int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"user":{"login":"alice"},"created_at":"2024-01-01T00:00:00Z","body":"comment %d"}`, start+i)
	}
	b.WriteByte(']')
	return b.String()
}

// Comments must walk every page through rest.Client.Paginate, mirroring
// listIssues (issue #2265). Forgejo's API defaults to 30 items per page, so a
// longer thread would otherwise lose everything past page 1 and IssueText's
// last-10 window would render stale comments rather than the newest ones.
func TestForgejoClient_Comments_PaginatesAcrossMultipleRealPages(t *testing.T) {
	const pageSize = forge.ResultPageLimit
	var gotPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues/10/comments" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
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
			w.Write([]byte(forgejoCommentsPage(1, pageSize)))
		case 2:
			w.Write([]byte(forgejoCommentsPage(pageSize+1, 5)))
		default:
			t.Errorf("server received request for page %d, want no request beyond the short page 2", page)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	cl, ok := fc.(forge.CommentLister)
	if !ok {
		t.Fatal("forgejoClient does not satisfy forge.CommentLister")
	}
	comments, err := cl.Comments("10")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}

	wantCount := pageSize + 5
	if len(comments) != wantCount {
		t.Fatalf("Comments returned %d comments, want %d (all pages merged)", len(comments), wantCount)
	}
	for i, c := range comments {
		wantBody := fmt.Sprintf("comment %d", i+1)
		if c.Body != wantBody {
			t.Fatalf("comments[%d].Body = %q, want %q (oldest-first order across merged pages)", i, c.Body, wantBody)
		}
	}
	if len(gotPages) != 2 || gotPages[0] != "1" || gotPages[1] != "2" {
		t.Fatalf("server saw page requests %v, want exactly [1 2]", gotPages)
	}
}

// newForgejoLabelServer backs a single owner/repo Forgejo repository, answering
// Probe, ListLabels, ListIssues (always empty, because doctor.Run's
// recoverable-issue count, #2255, only needs somewhere to land) and CreateLabel
// against an in-memory label set. It records every name POSTed to the
// create-label endpoint, in call order with duplicates, for a test to assert on.
func newForgejoLabelServer(t *testing.T, initial []string) (srv *httptest.Server, created *[]string) {
	t.Helper()
	labels := make(map[string]bool, len(initial))
	for _, l := range initial {
		labels[l] = true
	}
	var createdNames []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"full_name":"owner/repo"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/labels":
			// ListLabels walks until an empty page; serve one after page 1.
			type labelOut struct {
				Name string `json:"name"`
			}
			var out []labelOut
			if page := r.URL.Query().Get("page"); page == "" || page == "1" {
				out = make([]labelOut, 0, len(labels))
				for name := range labels {
					out = append(out, labelOut{Name: name})
				}
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/labels":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			labels[name] = true
			createdNames = append(createdNames, name)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &createdNames
}

// This drives doctor.Run against the real forgejo IssueTracker adapter over an
// httptest server rather than a fake, so the four work/triage labels (AC#2) and
// the six ADR 0022 research labels (AC#3) are created through the adapter's own
// CreateLabel HTTP call and doctor's re-verify then sees them all present.
func TestDoctorRun_Forgejo_CreatesTriageAndResearchLabels(t *testing.T) {
	srv, created := newForgejoLabelServer(t, nil)
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	cfg := doctor.Config{
		IssueTracker:    "forgejo",
		Label:           "ready-for-agent",
		InProgressLabel: "agent-in-progress",
		FailedLabel:     "agent-failed",
		CompleteLabel:   "agent-complete",
		// MergePolicy "manual" keeps the branch-protection row (issue #2570)
		// Advisory rather than Required. cf does not script SetBranchProtected,
		// so an unset base branch would otherwise report a spurious Required
		// failure unrelated to label creation.
		MergePolicy: "manual",
	}

	var buf bytes.Buffer
	err := doctor.Run(fc, cf, cfg, doctor.NewReporter(&buf, true), bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err != nil {
		t.Fatalf("doctor.Run: %v", err)
	}

	wantLabels := append([]string{cfg.Label, cfg.InProgressLabel, cfg.FailedLabel, cfg.CompleteLabel}, doctor.ResearchLabelNames()...)
	createdSet := make(map[string]bool, len(*created))
	for _, name := range *created {
		createdSet[name] = true
	}
	for _, want := range wantLabels {
		if !createdSet[want] {
			t.Errorf("label %q was never POSTed to the forgejo adapter's create-label endpoint; created = %v", want, *created)
		}
	}

	if got := buf.String(); !strings.Contains(got, "ok: all triage, research, priority, ambiguous-spec, and butler labels present") {
		t.Errorf("output missing final success line, got:\n%s", got)
	}
}

// AC#3: with the four work/triage labels present but the ADR 0022 research
// labels missing, doctor.Run reports the gap as advisory and returns nil rather
// than failing the check. Runs non-interactively so no creation prompt or POST
// happens at all.
func TestDoctorRun_Forgejo_MissingResearchLabelsAdvisoryOnly(t *testing.T) {
	workLabels := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	srv, created := newForgejoLabelServer(t, workLabels)
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	cfg := doctor.Config{
		IssueTracker:    "forgejo",
		Label:           workLabels[0],
		InProgressLabel: workLabels[1],
		FailedLabel:     workLabels[2],
		CompleteLabel:   workLabels[3],
		// MergePolicy "manual" keeps the branch-protection row (issue #2570)
		// Advisory rather than Required, for the same reason as above.
		MergePolicy: "manual",
	}

	var buf bytes.Buffer
	err := doctor.Run(fc, cf, cfg, doctor.NewReporter(&buf, true), bufio.NewScanner(strings.NewReader("")), false, nil)
	if err != nil {
		t.Fatalf("doctor.Run: %v, want nil — missing research labels are advisory only (AC#3)", err)
	}

	if len(*created) != 0 {
		t.Errorf("expected no CreateLabel calls in the non-interactive advisory path, got created = %v", *created)
	}

	out := buf.String()
	if !strings.Contains(out, "advisory:") || !strings.Contains(out, "does not fail") {
		t.Errorf("output missing advisory line, got:\n%s", out)
	}
}

// forge.MergeCloser (issue #2259) is settle's deterministic post-merge close
// backstop. The adapter must not satisfy forge.IssueCloser: that interface
// belongs to the local adapter's reconcile-owned closed: axis, and a forgejo
// adapter implementing it would let an ISSUE_TRACKER=local + CODE_FORGE=forgejo
// pairing close a local issue through the wrong path.
func TestForgejoClient_ImplementsMergeCloser(t *testing.T) {
	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{})
	if _, ok := fc.(forge.MergeCloser); !ok {
		t.Error("forgejoClient does not satisfy forge.MergeCloser, want it implemented")
	}
	if _, ok := fc.(forge.IssueCloser); ok {
		t.Error("forgejoClient satisfies forge.IssueCloser, want it hidden")
	}
}

// Issue #2259: when Forgejo's own merged-PR auto-close already ran (the PR body
// carried a Closes #<N> keyword), CloseMergedIssue must be a no-op and never
// PATCH. The fake server fails the test if it ever sees one.
func TestForgejoClient_CloseMergedIssue_AlreadyClosedIsNoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues/42":
			w.Write([]byte(`{"number":42,"title":"t","body":"","state":"closed","labels":[]}`))
		case r.Method == http.MethodPatch:
			t.Error("must not PATCH an already-closed issue")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	mc, ok := fc.(forge.MergeCloser)
	if !ok {
		t.Fatalf("forgejoClient does not satisfy forge.MergeCloser")
	}
	if err := mc.CloseMergedIssue("42"); err != nil {
		t.Fatalf("CloseMergedIssue on an already-closed issue must be a no-op, got: %v", err)
	}
}

// A still-open issue is the case Forgejo's own auto-close missed.
func TestForgejoClient_CloseMergedIssue_ClosesOpenIssue(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues/42":
			w.Write([]byte(`{"number":42,"title":"t","body":"","state":"open","labels":[]}`))
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

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	mc, ok := fc.(forge.MergeCloser)
	if !ok {
		t.Fatalf("forgejoClient does not satisfy forge.MergeCloser")
	}
	if err := mc.CloseMergedIssue("42"); err != nil {
		t.Fatalf("CloseMergedIssue on an open issue: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/42" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v1/repos/owner/repo/issues/42")
	}
	if gotBody["state"] != "closed" {
		t.Fatalf("body[state] = %v, want %q", gotBody["state"], "closed")
	}
}

// A real close-PATCH failure must reach the caller, not get swallowed as if it
// were the idempotent already-closed case.
func TestForgejoClient_CloseMergedIssue_GenuineFailureSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues/42":
			w.Write([]byte(`{"number":42,"title":"t","body":"","state":"open","labels":[]}`))
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	mc, ok := fc.(forge.MergeCloser)
	if !ok {
		t.Fatalf("forgejoClient does not satisfy forge.MergeCloser")
	}
	err := mc.CloseMergedIssue("42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error must surface the unexpected status, got: %v", err)
	}
}
