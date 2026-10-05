package forgejo_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// postIssueFake serves repo labels, org labels (orgBody, or 404 when empty,
// or orgStatus when set), and POST /issues, recording the create bodies and
// every label PUT. putStatus overrides the PUT's 200.
type postIssueFake struct {
	t         *testing.T
	creates   []map[string]any
	putPaths  []string
	putLabels [][]any
	orgStatus int
	putStatus int
}

func (f *postIssueFake) handler(repoLabels, orgBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			f.putPaths = append(f.putPaths, r.URL.Path)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Errorf("decode label PUT body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			names, _ := body["labels"].([]any)
			f.putLabels = append(f.putLabels, names)
			if f.putStatus != 0 {
				w.WriteHeader(f.putStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == forgejoOrgLabelsPath:
			if f.orgStatus != 0 {
				w.WriteHeader(f.orgStatus)
				return
			}
			if orgBody == "" && serveNoOrgLabels(w, r) {
				return
			}
			serveLabels(w, r, orgBody)
		case r.URL.Path == "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, repoLabels)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/issues":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.creates = append(f.creates, body)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"number":100,"html_url":"https://codeberg.org/owner/repo/issues/100"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newPostIssueFiler(srvURL string) forge.HostPostedIssueFiler {
	return forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srvURL, Repo: "owner/repo", Token: "tok"}).(forge.HostPostedIssueFiler)
}

// Forgejo's create endpoint wants label IDs, so PostIssue resolves names to
// IDs and sends them on the single create; a follow-up label call could fail
// after the issue exists (issue #4459).
func TestForgejoClient_PostIssue_SendsLabelIDsOnCreate(t *testing.T) {
	fake := &postIssueFake{t: t}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"agent-review-finding"},{"id":8,"name":"other"}]`, ""))
	defer srv.Close()

	url, err := newPostIssueFiler(srv.URL).PostIssue("a title", "a body", []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if url != "https://codeberg.org/owner/repo/issues/100" {
		t.Errorf("url = %q", url)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(fake.creates))
	}
	gotLabels, _ := fake.creates[0]["labels"].([]any)
	if len(gotLabels) != 1 || gotLabels[0] != float64(7) {
		t.Errorf("labels body = %v, want [7]", fake.creates[0]["labels"])
	}
	if len(fake.putPaths) != 0 {
		t.Errorf("PUT calls = %d, want 0", len(fake.putPaths))
	}
}

func TestForgejoClient_PostIssue_ResolvesOrgOnlyLabel(t *testing.T) {
	fake := &postIssueFake{t: t}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"shared"}]`, `[{"id":70,"name":"shared"},{"id":71,"name":"org-only"}]`))
	defer srv.Close()

	if _, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"shared", "org-only"}); err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(fake.creates))
	}
	gotLabels, _ := fake.creates[0]["labels"].([]any)
	if len(gotLabels) != 2 || gotLabels[0] != float64(7) || gotLabels[1] != float64(71) {
		t.Errorf("labels body = %v, want [7 71] (repo wins a clash, org-only resolves)", fake.creates[0]["labels"])
	}
}

// An unresolvable label must fail before the create, so ("", err) keeps
// meaning nothing was filed (issue #4459).
func TestForgejoClient_PostIssue_UnknownLabelErrorsBeforeCreate(t *testing.T) {
	fake := &postIssueFake{t: t}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"known"}]`, ""))
	defer srv.Close()

	url, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"known", "missing-one"})
	if err == nil {
		t.Fatal("PostIssue succeeded, want error for an undefined label")
	}
	if url != "" {
		t.Errorf("url = %q, want empty", url)
	}
	if !strings.Contains(err.Error(), "missing-one") {
		t.Errorf("err = %v, want it to name missing-one", err)
	}
	if len(fake.creates) != 0 {
		t.Errorf("creates = %d, want 0", len(fake.creates))
	}
}

// A token without read:organization cannot see org labels, so an org-only
// name has no knowable ID; PostIssue creates with the repo IDs it has, then
// lets Forgejo resolve the full name list server-side.
func TestForgejoClient_PostIssue_AuthDegradedOrgAppliesLabelsByName(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake := &postIssueFake{t: t, orgStatus: status}
			srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"repo-label"}]`, ""))
			defer srv.Close()

			url, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"repo-label", "org-only"})
			if err != nil {
				t.Fatalf("PostIssue: %v", err)
			}
			if url != "https://codeberg.org/owner/repo/issues/100" {
				t.Errorf("url = %q", url)
			}
			if len(fake.creates) != 1 {
				t.Fatalf("creates = %d, want 1", len(fake.creates))
			}
			gotIDs, _ := fake.creates[0]["labels"].([]any)
			if len(gotIDs) != 1 || gotIDs[0] != float64(7) {
				t.Errorf("create labels = %v, want [7]", fake.creates[0]["labels"])
			}
			if len(fake.putPaths) != 1 || fake.putPaths[0] != "/api/v1/repos/owner/repo/issues/100/labels" {
				t.Fatalf("PUT paths = %v, want one to issue 100's labels", fake.putPaths)
			}
			if got := fake.putLabels[0]; len(got) != 2 || got[0] != "repo-label" || got[1] != "org-only" {
				t.Errorf("PUT labels = %v, want the full name list", got)
			}
		})
	}
}

// The create already landed, so a failed label call must not read as
// "nothing filed": the URL comes back with a nil error.
func TestForgejoClient_PostIssue_AuthDegradedLabelPutFailureStillReturnsURL(t *testing.T) {
	fake := &postIssueFake{t: t, orgStatus: http.StatusForbidden, putStatus: http.StatusUnprocessableEntity}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"repo-label"}]`, ""))
	defer srv.Close()

	url, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"repo-label", "org-only"})
	if err != nil {
		t.Fatalf("PostIssue: %v, want nil once the issue exists", err)
	}
	if url != "https://codeberg.org/owner/repo/issues/100" {
		t.Errorf("url = %q", url)
	}
	if len(fake.creates) != 1 || len(fake.putPaths) != 1 {
		t.Errorf("creates = %d, puts = %d, want 1 and 1", len(fake.creates), len(fake.putPaths))
	}
}

// With every label resolvable the degraded token needs no label call.
func TestForgejoClient_PostIssue_AuthDegradedResolvedLabelsNeedNoPut(t *testing.T) {
	fake := &postIssueFake{t: t, orgStatus: http.StatusForbidden}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"repo-label"}]`, ""))
	defer srv.Close()

	if _, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"repo-label"}); err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if len(fake.putPaths) != 0 {
		t.Errorf("PUT calls = %d, want 0", len(fake.putPaths))
	}
}

// Org labels read fine here, so a missing name is definitively undefined and
// the degraded by-name fallback must not apply.
func TestForgejoClient_PostIssue_UnknownLabelWithReadableOrgErrorsBeforeCreate(t *testing.T) {
	fake := &postIssueFake{t: t}
	srv := httptest.NewServer(fake.handler(`[{"id":7,"name":"known"}]`, `[{"id":71,"name":"org-label"}]`))
	defer srv.Close()

	if _, err := newPostIssueFiler(srv.URL).PostIssue("t", "b", []string{"known", "missing-one"}); err == nil {
		t.Fatal("PostIssue succeeded, want error for an undefined label")
	}
	if len(fake.creates) != 0 || len(fake.putPaths) != 0 {
		t.Errorf("creates = %d, puts = %d, want 0 and 0", len(fake.creates), len(fake.putPaths))
	}
}

func TestForgejoClient_ListLabels_ReturnsRepoLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveNoOrgLabels(w, r) {
			return
		}
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

// forgejoOrgLabelsPath is the org labels endpoint ListLabels queries
// alongside the repo one (issue #3997), for the "owner/repo" fixture repo
// every fake in this file uses. A fake that doesn't route it explicitly
// falls through to whatever its own catch-all handles, most often an
// /issues handler, so most fakes below route it via serveNoOrgLabels before
// that catch-all; a few instead serve real org labels or a specific status
// on this path directly.
const forgejoOrgLabelsPath = "/api/v1/orgs/owner/labels"

// serveNoOrgLabels 404s forgejoOrgLabelsPath — the no-owning-org case, which
// must not add anything to ListLabels' union — and reports whether it
// handled the request, so a fake can route it with "if serveNoOrgLabels(w,
// r) { return }" ahead of its own catch-all.
func serveNoOrgLabels(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != forgejoOrgLabelsPath {
		return false
	}
	w.WriteHeader(http.StatusNotFound)
	return true
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

// serveIssues writes body as /issues page 1 and an empty page after it, the
// same shape as serveLabels: listIssues now walks until an empty page
// (issue #3978), so a fake asserting an exact request count per label needs
// page 2 to end the walk rather than repeat page 1 and rely on the
// repeated-first-item guard's extra round trip.
func serveIssues(w http.ResponseWriter, r *http.Request, body string) {
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

// serveCappedLabelsPage renders a fake /labels endpoint that caps page 1 at
// forgejoServerPageCap, serves page2Body on page 2, and an empty page after
// that.
func serveCappedLabelsPage(w http.ResponseWriter, r *http.Request, prefix, page2Body string) {
	switch page := r.URL.Query().Get("page"); page {
	case "1", "":
		w.Write([]byte(forgejoLabelsPage(prefix, forgejoServerPageCap)))
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
		if serveNoOrgLabels(w, r) {
			return
		}
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

	wantCount := forgejoServerPageCap + 2
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
		if serveNoOrgLabels(w, r) {
			return
		}
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

// ListLabels resolves the union of repo and org labels (issue #3997):
// Forgejo's /issues label filter and PUT issue-labels replace both resolve
// against that same union, so definedLabels' pre-check must see it too.
func TestForgejoClient_ListLabels_UnionsRepoAndOrgLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"ready-for-agent"},{"name":"shared-label"}]`)
		case forgejoOrgLabelsPath:
			serveLabels(w, r, `[{"name":"shared-label"},{"name":"org-only-label"}]`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labels, err := fc.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	want := []string{"ready-for-agent", "shared-label", "org-only-label"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("ListLabels = %v, want %v (repo order first, then org-only names, deduplicated)", labels, want)
	}
}

// captureStderr returns everything fn writes to os.Stderr, via a temp file
// like main_test.go's captureStderrFile. Swapping the package-global
// os.Stderr makes this unsafe under t.Parallel with any other
// stderr-sensitive test.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()
	fn()
	f.Close()
	captured, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(captured)
}

// A 404 on the org labels endpoint is the ordinary case for a user-owned
// repo (no owning org): it must degrade to repo labels only, not fail
// ListLabels, and not warn — the warning is reserved for the auth-failure
// case below.
func TestForgejoClient_ListLabels_OrgNotFoundYieldsRepoLabelsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
		case forgejoOrgLabelsPath:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	var labels []string
	var err error
	stderr := captureStderr(t, func() {
		labels, err = fc.ListLabels()
	})
	if err != nil {
		t.Fatalf("ListLabels: %v, want nil — a user-owned repo's org lookup 404s and must degrade to repo labels only", err)
	}
	want := []string{"ready-for-agent"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("ListLabels = %v, want %v", labels, want)
	}
	if strings.Contains(stderr, "WARNING") {
		t.Errorf("stderr = %q, want no WARNING — 404 is the ordinary no-owning-org case", stderr)
	}
}

// A 403 on the org labels endpoint means the token lacks read:organization
// even though the repo fetch with the same token just succeeded. Failing
// ListLabels here would newly break doctor (it maps any ListLabels error to
// forge.ErrConnectivity) for existing fine-grained-token deployments, so
// this degrades to repo labels only instead, the same as the 404 case, with
// a warning — gated by sync.Once so a second ListLabels call on the same
// client stays quiet.
func TestForgejoClient_ListLabels_OrgAuthFailureYieldsRepoLabelsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
		case forgejoOrgLabelsPath:
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	var labels []string
	var err error
	stderr := captureStderr(t, func() {
		labels, err = fc.ListLabels()
		if err != nil {
			return
		}
		// A second call on the same client must not warn again: the
		// sync.Once gate is per-client, not per-call.
		_, err = fc.ListLabels()
	})
	if err != nil {
		t.Fatalf("ListLabels: %v, want nil — a token missing read:organization must degrade to repo labels only", err)
	}
	want := []string{"ready-for-agent"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("ListLabels = %v, want %v", labels, want)
	}
	warnings := strings.Count(stderr, "WARNING")
	if warnings != 1 {
		t.Fatalf("stderr had %d WARNING lines across two ListLabels calls, want exactly 1 (sync.Once-gated): %q", warnings, stderr)
	}
	if !strings.Contains(stderr, "read:organization") {
		t.Errorf("stderr = %q, want it to mention read:organization", stderr)
	}
}

// Any other org error (a transient 5xx in real life) is not one of the two
// expected degrade cases and must fail ListLabels: definedLabels already
// falls back to an unfiltered query+client-side refilter on that error. 400
// (not 500) keeps the rest client's transient-status retry/backoff out of
// this test, the same reason TestForgejoClient_Probe_ServerError uses it.
func TestForgejoClient_ListLabels_OrgOtherErrorFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
		case forgejoOrgLabelsPath:
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	if _, err := fc.ListLabels(); err == nil {
		t.Fatal("ListLabels: want error when the org lookup fails with neither 404 nor 403, got nil")
	}
}

// ListLabels' org-lookup verdict is cached per client except on success or
// on any error other than 404/403 (issue #4034): a success is never cached
// because org labels can be added mid-run, and a non-404/403 error means
// the verdict itself isn't settled yet, so a later poll must retry it
// rather than pinning it to today's transient failure.
func TestForgejoClient_ListLabels_OrgVerdictCachingPerClient(t *testing.T) {
	tests := []struct {
		name         string
		orgHandler   func(w http.ResponseWriter, r *http.Request)
		wantLabels   []string // nil when wantErr
		wantErr      bool
		cached       bool
		wantWarnings int
	}{
		{
			name:       "404 not found is cached",
			orgHandler: func(w http.ResponseWriter, r *http.Request) { serveNoOrgLabels(w, r) },
			wantLabels: []string{"ready-for-agent"},
			cached:     true,
		},
		{
			name:         "403 auth failure is cached",
			orgHandler:   func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
			wantLabels:   []string{"ready-for-agent"},
			cached:       true,
			wantWarnings: 1,
		},
		{
			name:       "200 success is not cached",
			orgHandler: func(w http.ResponseWriter, r *http.Request) { serveLabels(w, r, `[{"name":"org-only-label"}]`) },
			wantLabels: []string{"ready-for-agent", "org-only-label"},
			cached:     false,
		},
		{
			// 400, not 500: a 500 would hit the rest client's own
			// transient-status retry/backoff, muddying the request count
			// this test asserts on (see the comment above
			// TestForgejoClient_ListLabels_OrgOtherErrorFails).
			name:       "other error is not cached",
			orgHandler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			wantErr:    true,
			cached:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orgRequests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/repos/owner/repo/labels":
					serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
				case forgejoOrgLabelsPath:
					orgRequests++
					tt.orgHandler(w, r)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
			var countAfterCall1, countAfterCall2 int
			stderr := captureStderr(t, func() {
				for i := 0; i < 2; i++ {
					labels, err := fc.ListLabels()
					if tt.wantErr {
						if err == nil {
							t.Fatalf("ListLabels call %d: want error, got nil", i+1)
						}
					} else {
						if err != nil {
							t.Fatalf("ListLabels call %d: %v", i+1, err)
						}
						if !reflect.DeepEqual(labels, tt.wantLabels) {
							t.Fatalf("ListLabels call %d = %v, want %v", i+1, labels, tt.wantLabels)
						}
					}
					if i == 0 {
						countAfterCall1 = orgRequests
					} else {
						countAfterCall2 = orgRequests
					}
				}
			})

			if tt.cached {
				if countAfterCall1 < 1 {
					t.Fatalf("org labels endpoint got %d requests after call 1, want at least 1", countAfterCall1)
				}
				if countAfterCall2 != countAfterCall1 {
					t.Fatalf("org labels endpoint got %d requests after call 2, want %d (verdict cached)", countAfterCall2, countAfterCall1)
				}
			} else if countAfterCall2 <= countAfterCall1 {
				t.Fatalf("org labels endpoint got %d requests after call 1 and %d after call 2, want call 2 > call 1 (verdict not cached)", countAfterCall1, countAfterCall2)
			}

			if warnings := strings.Count(stderr, "WARNING"); warnings != tt.wantWarnings {
				t.Fatalf("stderr had %d WARNING lines across two ListLabels calls, want %d: %q", warnings, tt.wantWarnings, stderr)
			}
		})
	}
}

// ListLabels called concurrently on one client, so that go test -race
// actually sees shared access to orgLabelsState.
func TestForgejoClient_ListLabels_ConcurrentCallsDoNotRace(t *testing.T) {
	var orgRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
		case forgejoOrgLabelsPath:
			orgRequests.Add(1)
			serveNoOrgLabels(w, r)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	want := []string{"ready-for-agent"}

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			labels, err := fc.ListLabels()
			if err != nil {
				errs <- fmt.Errorf("ListLabels: %v", err)
				return
			}
			if !reflect.DeepEqual(labels, want) {
				errs <- fmt.Errorf("ListLabels = %v, want %v", labels, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
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
		if serveNoOrgLabels(w, r) {
			return
		}
		issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
		serveIssues(w, r, `[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	// listIssues walks until an empty page (issue #3978), so a single-label
	// query is 2 requests here: the non-empty page 1 and the empty page 2
	// that ends the walk. Both must carry the page-2 finding label, never a
	// second, different label.
	if len(issuesRequests) != 2 || issuesRequests[0] != "agent-review-finding" || issuesRequests[1] != "agent-review-finding" {
		t.Fatalf("issues requests = %+v, want exactly two, both for the page-2 label", issuesRequests)
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
// descending so the final ascending sort has to be real; page 2 is short but
// non-empty (a server-capped page, issue #3978), so only the empty page 3
// ends the walk.
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
		case 3:
			// A short page 2 is not proof of the last page on a server that
			// caps MAX_RESPONSE_ITEMS below forge.ResultPageLimit (issue
			// #3978): the walk must request one more page and stop only on
			// the empty one.
			w.Write([]byte(`[]`))
		default:
			t.Errorf("server received request for page %d, want no request beyond the empty page 3", page)
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
	if len(gotPages) != 3 || gotPages[0] != "1" || gotPages[1] != "2" || gotPages[2] != "3" {
		t.Fatalf("server saw page requests %v, want exactly [1 2 3]", gotPages)
	}
}

// Stock Forgejo caps limit at [api] MAX_RESPONSE_ITEMS (default 50) on the
// issues listing endpoint no matter what limit the client requests (issue
// #3978), so a walk that stopped on len(payload) < forge.ResultPageLimit
// would give up after page 1 and silently drop every issue past the server's
// own cap.
func TestForgejoClient_ListOpenIssues_WalksPastServerCappedPageSize(t *testing.T) {
	const total = 120
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if limit := r.URL.Query().Get("limit"); limit != strconv.Itoa(forge.ResultPageLimit) {
			t.Errorf("limit query param = %q, want %q", limit, strconv.Itoa(forge.ResultPageLimit))
		}
		serveCappedNumbered(w, r, total, forgejoIssuesPage)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	issues, err := fc.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != total {
		t.Fatalf("ListOpenIssues returned %d issues, want %d (every server-capped page merged)", len(issues), total)
	}
}

// listIssues must not loop forever against a server or proxy that ignores the
// ?page query param and always re-serves the same non-empty page: a repeated
// first issue number across pages is the cheap tell it stops on, mirroring
// ListLabels' guard (#3953).
func TestForgejoClient_ListOpenIssues_StopsOnRepeatedFirstIssueAcrossPages(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 5 {
			t.Errorf("ListOpenIssues made more than 5 requests, want the repeated-page guard to stop it")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(forgejoIssuesPage(1, 2)))
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	issues, err := fc.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("ListOpenIssues = %+v, want 2 issues with no duplicates from the repeated page", issues)
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
		if serveNoOrgLabels(w, r) {
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

// Counterpart of the above: a label the repo doesn't define but the owning
// org does (issue #3997) must still be treated as defined, since Forgejo's
// /issues label filter and PUT issue-labels both resolve org labels too.
func TestForgejoClient_ListIssues_SeesLabelDefinedOnlyOnOrg(t *testing.T) {
	var gotLabelsParam string
	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[{"name":"unrelated-label"}]`)
		case forgejoOrgLabelsPath:
			serveLabels(w, r, `[{"name":"ready-for-agent"}]`)
		default:
			requested = true
			gotLabelsParam = r.URL.Query().Get("labels")
			serveIssues(w, r, `[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"ready-for-agent"}]}]`)
		}
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
	if !requested || gotLabelsParam != "ready-for-agent" {
		t.Fatalf("want the /issues query sent with labels=ready-for-agent, got requested=%v labels=%q", requested, gotLabelsParam)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("issues = %+v, want the org-only-labelled issue", issues)
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
		if serveNoOrgLabels(w, r) {
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
		if serveNoOrgLabels(w, r) {
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
		if serveNoOrgLabels(w, r) {
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
		if serveNoOrgLabels(w, r) {
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
		if serveNoOrgLabels(w, r) {
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
		if serveNoOrgLabels(w, r) {
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
				if serveNoOrgLabels(w, r) {
					return
				}
				issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
				serveIssues(w, r, `[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`)
			}))
			defer srv.Close()

			fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
			issues, err := fc.ListIssuesWithLabels(state, []string{"agent-review-finding", "agent-missing-finding"})
			if err != nil {
				t.Fatalf("ListIssuesWithLabels: %v", err)
			}
			// listIssues walks until an empty page (issue #3978): the
			// defined label's query is the non-empty page 1 plus the empty
			// page 2 that ends the walk, both for the same label.
			if len(issuesRequests) != 2 || issuesRequests[0] != "agent-review-finding" || issuesRequests[1] != "agent-review-finding" {
				t.Fatalf("issues requests = %+v, want exactly two, both for the defined label", issuesRequests)
			}
			if len(issues) != 1 || issues[0].Number != "5" {
				t.Fatalf("issues = %+v", issues)
			}
		})
	}
}

// Counterpart of the above: a label the repo doesn't define but the owning
// org does (issue #3997) must still be queried, not skipped as undefined.
func TestForgejoClient_ListIssuesWithLabels_SeesLabelDefinedOnlyOnOrg(t *testing.T) {
	var issuesRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/labels":
			serveLabels(w, r, `[]`)
		case forgejoOrgLabelsPath:
			serveLabels(w, r, `[{"name":"agent-review-finding"}]`)
		default:
			issuesRequests = append(issuesRequests, r.URL.Query().Get("labels"))
			serveIssues(w, r, `[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if len(issuesRequests) != 2 || issuesRequests[0] != "agent-review-finding" || issuesRequests[1] != "agent-review-finding" {
		t.Fatalf("issues requests = %+v, want exactly two, both for the org-only-defined label", issuesRequests)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("issues = %+v", issues)
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
		if serveNoOrgLabels(w, r) {
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
		serveIssues(w, r, `[{"number":5,"title":"t","body":"b","state":"open","labels":[{"name":"agent-review-finding"}]}]`)
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"}).(forge.LabeledBacklogLister)
	issues, err := fc.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	// listIssues walks until an empty page (issue #3978), so each label's
	// query is 2 requests (a non-empty page 1, an empty page 2), 4 total
	// across the two labels queried unfiltered.
	seen := map[string]int{}
	for _, l := range issuesRequests {
		seen[l]++
	}
	if len(issuesRequests) != 4 || seen["agent-review-finding"] != 2 || seen["agent-research-finding"] != 2 {
		t.Fatalf("issues requests = %+v, want both labels queried unfiltered, 2 requests each", issuesRequests)
	}
	if len(issues) != 1 || issues[0].Number != "5" {
		t.Fatalf("issues = %+v", issues)
	}
}

// forgejoCommentsPage renders count comments as a Forgejo comments-list JSON
// page, each body reading "comment <n>" so a test can assert order.
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

// Real Forgejo's comments endpoint ignores page/limit and returns every
// comment on every request (issue #3978) — unlike every other list endpoint
// this adapter walks. This fake mirrors that: it serves the full set
// regardless of query params. Comments must make exactly one request and
// return each comment exactly once, in order, rather than looping forever
// treating a full-looking response as "another page to fetch".
func TestForgejoClient_Comments_UnpaginatedEndpointServesEveryCommentOnce(t *testing.T) {
	const total = forge.ResultPageLimit + 20
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/owner/repo/issues/10/comments" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Has("page") || r.URL.Query().Has("limit") {
			t.Errorf("comments request query = %q, want no page or limit param (Comments must not paginate)", r.URL.RawQuery)
		}
		requests++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(forgejoCommentsPage(1, total)))
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

	if len(comments) != total {
		t.Fatalf("Comments returned %d comments, want %d (every comment, exactly once)", len(comments), total)
	}
	for i, c := range comments {
		wantBody := fmt.Sprintf("comment %d", i+1)
		if c.Body != wantBody {
			t.Fatalf("comments[%d].Body = %q, want %q (oldest-first order)", i, c.Body, wantBody)
		}
	}
	if requests != 1 {
		t.Fatalf("server saw %d requests, want exactly 1 — Comments must not paginate an endpoint that ignores page/limit", requests)
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
		case r.Method == http.MethodGet && r.URL.Path == forgejoOrgLabelsPath:
			// This fixture repo has no owning org; 404 degrades ListLabels
			// to repo labels only (issue #3997), same as real Forgejo.
			w.WriteHeader(http.StatusNotFound)
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

// AddLabels appends only the labels num does not already carry, replacing the
// full set through setLabels (issue #4074).
func TestForgejoClient_AddLabels_AppendsMissingOnly(t *testing.T) {
	h := newForgejoHarness(t)
	h.SeedIssue(forge.Issue{Number: "42", Title: "t", Labels: []string{"agent-butler-finding"}})

	labeler, ok := h.Tracker().(forge.IssueLabeler)
	if !ok {
		t.Fatal("forgejoClient does not satisfy forge.IssueLabeler")
	}
	if err := labeler.AddLabels("42", []string{"agent-butler-finding", "ready-for-agent"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}

	iss, err := h.Tracker().Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(iss.Labels) != 2 || !slices.Contains(iss.Labels, "agent-butler-finding") || !slices.Contains(iss.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want [agent-butler-finding ready-for-agent], no duplicate", iss.Labels)
	}
}

// An empty label name is a caller bug, not a legitimate no-op label the way
// TransitionState's unconfigured to-label is; it must be rejected before any
// network call.
func TestForgejoClient_AddLabels_EmptyLabelErrorsWithoutRequest(t *testing.T) {
	h := newForgejoHarness(t)
	h.SeedIssue(forge.Issue{Number: "42", Title: "t", Labels: []string{"agent-butler-finding"}})

	labeler, ok := h.Tracker().(forge.IssueLabeler)
	if !ok {
		t.Fatal("forgejoClient does not satisfy forge.IssueLabeler")
	}
	err := labeler.AddLabels("42", []string{"ready-for-agent", ""})
	if err == nil {
		t.Fatal("want error, got nil")
	}

	iss, err := h.Tracker().Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "agent-butler-finding" {
		t.Errorf("labels = %v, want unchanged [agent-butler-finding]", iss.Labels)
	}
}

// A real labels-PUT failure must reach the caller.
func TestForgejoClient_AddLabels_GenuineFailureSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/owner/repo/issues/42":
			w.Write([]byte(`{"number":42,"title":"t","body":"","state":"open","labels":[]}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := forgejo.NewForgejoClient(forgejo.ForgejoConfig{BaseURL: srv.URL, Repo: "owner/repo", Token: "tok"})
	labeler, ok := fc.(forge.IssueLabeler)
	if !ok {
		t.Fatalf("forgejoClient does not satisfy forge.IssueLabeler")
	}
	err := labeler.AddLabels("42", []string{"ready-for-agent"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error must surface the unexpected status, got: %v", err)
	}
}
