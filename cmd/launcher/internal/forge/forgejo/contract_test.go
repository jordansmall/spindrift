package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix (issue #460).
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// forgejoIssueRecord is one scripted issue in forgejoHarness's in-memory
// backend.
type forgejoIssueRecord struct {
	number     int
	title      string
	body       string
	labels     []string
	nativeDeps []string
	failDeps   bool // simulates a native dependencies-endpoint error
	comments   []forgejoWireComment
}

// forgejoWireComment mirrors Forgejo's native comment JSON independently of
// the adapter's own payload struct, so a tag typo in the adapter fails the
// contract instead of round-tripping through a shared type.
type forgejoWireComment struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

// forgejoHarness is a forgetest.Harness backed by an httptest server standing
// in for the Forgejo REST API. Forgejo's dependencies endpoint is separate from
// the issue GET, unlike jira where both share one request, so this harness
// implements both forgetest.NativeCapable and forgetest.NativeFailureIsolatable,
// mirroring the github/Fake harnesses.
type forgejoHarness struct {
	mu            sync.Mutex
	order         []string
	issues        map[string]*forgejoIssueRecord
	definedLabels map[string]bool // sticky registry: stays defined after an issue drops it, like real Forgejo

	srv *httptest.Server
	tr  forge.IssueTracker
}

func newForgejoHarness(t *testing.T) *forgejoHarness {
	h := &forgejoHarness{issues: map[string]*forgejoIssueRecord{}, definedLabels: map[string]bool{}}
	for _, l := range []string{testLabels.Dispatchable, testLabels.InProgress, testLabels.Complete, testLabels.Failed} {
		if l != "" {
			h.definedLabels[l] = true
		}
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
	h.tr = forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL:       h.srv.URL,
		Repo:          "owner/repo",
		Token:         "tok",
		Labels:        testLabels,
		VerdictLabels: forge.ResearchVerdictLabels(),
	})
	return h
}

func (h *forgejoHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *forgejoHarness) SeedIssue(iss forge.Issue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.issues[iss.Number]; !ok {
		h.order = append(h.order, iss.Number)
	}
	n, _ := strconv.Atoi(iss.Number)
	h.issues[iss.Number] = &forgejoIssueRecord{
		number: n,
		title:  iss.Title,
		body:   iss.Body,
		labels: append([]string(nil), iss.Labels...),
	}
	for _, l := range iss.Labels {
		h.definedLabels[l] = true
	}
}

func (h *forgejoHarness) SeedNativeDeps(num string, ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].nativeDeps = ids
}

func (h *forgejoHarness) FailNativeDeps(num string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].failDeps = true
}

func (h *forgejoHarness) SeedComments(num string, comments []forge.Comment) {
	h.mu.Lock()
	defer h.mu.Unlock()
	wire := make([]forgejoWireComment, len(comments))
	for i, c := range comments {
		wire[i].User.Login = c.Author
		wire[i].CreatedAt = c.CreatedAt
		wire[i].Body = c.Body
	}
	h.issues[num].comments = wire
}

func (h *forgejoHarness) IsolatesNativeFailure() {}

func (h *forgejoHarness) IsPriorityCapable() {}

func (h *forgejoHarness) issuePayload(rec *forgejoIssueRecord) map[string]any {
	labels := make([]map[string]any, len(rec.labels))
	for i, l := range rec.labels {
		labels[i] = map[string]any{"name": l}
	}
	return map[string]any{
		"number": rec.number,
		"title":  rec.title,
		"body":   rec.body,
		"state":  "open",
		"labels": labels,
	}
}

var (
	issuesListRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues$`)
	issueDepsRe    = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/dependencies$`)
	issueBlocksRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/blocks$`)
	issueLabelsRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/labels$`)
	issueCommentRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/comments$`)
	issueRe        = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)$`)
	labelsListRe   = regexp.MustCompile(`^/api/v1/repos/owner/repo/labels$`)
	repoRootRe     = regexp.MustCompile(`^/api/v1/repos/owner/repo$`)
)

func (h *forgejoHarness) handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && issuesListRe.MatchString(r.URL.Path):
		label := r.URL.Query().Get("labels")
		var out []map[string]any
		for _, num := range h.order {
			rec := h.issues[num]
			if label != "" && !contains(rec.labels, label) {
				continue
			}
			out = append(out, h.issuePayload(rec))
		}

		// Genuinely paginate on page/limit (issue #2265). Production code always
		// sends both, 1-indexed, so an out-of-range page must come back as an
		// empty page rather than an error, matching the real Forgejo API: an
		// empty page is what stops walkPages' walk.
		json.NewEncoder(w).Encode(windowPage(r, out))
		return

	case r.Method == http.MethodGet && issueDepsRe.MatchString(r.URL.Path):
		num := issueDepsRe.FindStringSubmatch(r.URL.Path)[1]
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rec.failDeps {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, len(rec.nativeDeps))
		for i, id := range rec.nativeDeps {
			n, _ := strconv.Atoi(id)
			out[i] = map[string]any{"number": n}
		}
		json.NewEncoder(w).Encode(windowPage(r, out))
		return

	case r.Method == http.MethodGet && issueBlocksRe.MatchString(r.URL.Path):
		num := issueBlocksRe.FindStringSubmatch(r.URL.Path)[1]
		if _, ok := h.issues[num]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{})
		return

	case r.Method == http.MethodPut && issueLabelsRe.MatchString(r.URL.Path):
		num := issueLabelsRe.FindStringSubmatch(r.URL.Path)[1]
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Labels []string `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		rec.labels = body.Labels
		for _, l := range body.Labels {
			h.definedLabels[l] = true
		}
		w.WriteHeader(http.StatusOK)
		return

	case r.Method == http.MethodPost && issueCommentRe.MatchString(r.URL.Path):
		num := issueCommentRe.FindStringSubmatch(r.URL.Path)[1]
		if _, ok := h.issues[num]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		return

	case r.Method == http.MethodGet && issueCommentRe.MatchString(r.URL.Path):
		// Like real Forgejo (issue #3978), serve the whole thread and ignore
		// page/limit.
		num := issueCommentRe.FindStringSubmatch(r.URL.Path)[1]
		out := []forgejoWireComment{}
		if rec, ok := h.issues[num]; ok {
			out = append(out, rec.comments...)
		}
		json.NewEncoder(w).Encode(out)
		return

	case r.Method == http.MethodGet && issueRe.MatchString(r.URL.Path):
		num := issueRe.FindStringSubmatch(r.URL.Path)[1]
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(h.issuePayload(rec))
		return

	case r.Method == http.MethodGet && labelsListRe.MatchString(r.URL.Path):
		// Sticky, like real Forgejo: a label seeded or ever attached to an
		// issue stays defined even after every issue drops it. Every label
		// fits on page 1; later pages come back empty to end ListLabels' walk.
		if p := r.URL.Query().Get("page"); p != "" && p != "1" {
			json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		names := make([]string, 0, len(h.definedLabels))
		for l := range h.definedLabels {
			names = append(names, l)
		}
		sort.Strings(names)
		out := make([]map[string]any, len(names))
		for i, n := range names {
			out[i] = map[string]any{"name": n}
		}
		json.NewEncoder(w).Encode(out)
		return

	case r.Method == http.MethodGet && repoRootRe.MatchString(r.URL.Path):
		json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/repo"})
		return

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestForgejoClient_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newForgejoHarness(t))
}

// TestForgejoClient_ListIssues_PaginatesAcrossMultipleRealPages seeds more than
// forge.ResultPageLimit issues so listIssues (issue #2265) must walk at least two
// real pages, then checks every seeded issue comes back in ascending number order.
func TestForgejoClient_ListIssues_PaginatesAcrossMultipleRealPages(t *testing.T) {
	h := newForgejoHarness(t)
	const seeded = forge.ResultPageLimit + 30

	for i := 1; i <= seeded; i++ {
		h.SeedIssue(forge.Issue{
			Number: strconv.Itoa(i),
			Title:  "paged",
			Labels: []string{testLabels.Dispatchable},
		})
	}

	issues, err := h.Tracker().ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(issues) != seeded {
		t.Fatalf("ListIssues(Dispatchable) returned %d issues, want %d", len(issues), seeded)
	}
	for i, iss := range issues {
		want := strconv.Itoa(i + 1)
		if iss.Number != want {
			t.Fatalf("ListIssues(Dispatchable)[%d].Number = %q, want %q (order not ascending)", i, iss.Number, want)
		}
	}
}
