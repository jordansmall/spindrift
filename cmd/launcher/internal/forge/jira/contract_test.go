package jira_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

type jiraIssueRecord struct {
	title, body string
	labels      []string
	nativeDeps  []string
	failGET     bool // simulates the native-API error the issue's GET returns
	comments    []jiraWireComment
}

// jiraWireComment mirrors Jira's native comment JSON independently of the
// adapter's own payload struct, so a tag typo in the adapter fails the
// contract instead of round-tripping through a shared type.
type jiraWireComment struct {
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string `json:"created"`
	Body    string `json:"body"`
}

// jiraHarness stands in for the Jira REST API. Jira's DepsOf and Issue share
// one underlying GET request (unlike github's separate dependencies/blocked_by
// call), so this harness implements forgetest.NativeCapable but not
// NativeFailureIsolatable: the contract's native-error-fallback scenario
// (AC2, issue #1544) covers only the Fake and github adapters.
type jiraHarness struct {
	mu     sync.Mutex
	order  []string
	issues map[string]*jiraIssueRecord
	// pageCap, when > 0, caps rows per search page below the requested
	// maxResults, as a real Jira server may.
	pageCap int

	cloud bool // serves Cloud's search/jql + approximate-count; rejects v2 /search (410)
	srv   *httptest.Server
	tr    forge.IssueTracker
}

var jqlLabelClause = regexp.MustCompile(`labels = "([^"]+)"`)

// newJiraHarness builds a Server/DC harness; newJiraCloudHarness a Cloud one,
// which the adapter selects by configuring an Email.
func newJiraHarness(t *testing.T) *jiraHarness { return newJiraHarnessMode(t, false) }

func newJiraCloudHarness(t *testing.T) *jiraHarness { return newJiraHarnessMode(t, true) }

func newJiraHarnessMode(t *testing.T, cloud bool) *jiraHarness {
	h := &jiraHarness{cloud: cloud, issues: map[string]*jiraIssueRecord{}}
	email := ""
	if cloud {
		email = "bot@example.com"
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
	h.tr = jira.NewJiraClient(jira.JiraConfig{
		BaseURL:       h.srv.URL,
		ProjectKey:    "PROJ",
		Email:         email,
		Token:         "tok",
		Labels:        testLabels,
		VerdictLabels: forge.ResearchVerdictLabels(),
	})
	return h
}

func (h *jiraHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *jiraHarness) SeedIssue(iss forge.Issue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.issues[iss.Number]; !ok {
		h.order = append(h.order, iss.Number)
	}
	h.issues[iss.Number] = &jiraIssueRecord{title: iss.Title, body: iss.Body, labels: append([]string(nil), iss.Labels...)}
}

func (h *jiraHarness) SeedNativeDeps(num string, ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].nativeDeps = ids
}

func (h *jiraHarness) FailNativeDeps(num string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.issues[num].failGET = true
}

func (h *jiraHarness) SeedComments(num string, comments []forge.Comment) {
	h.mu.Lock()
	defer h.mu.Unlock()
	wire := make([]jiraWireComment, len(comments))
	for i, c := range comments {
		wire[i].Author.DisplayName = c.Author
		wire[i].Created = c.CreatedAt
		wire[i].Body = c.Body
	}
	h.issues[num].comments = wire
}

type jiraPayloadLink struct {
	Type struct {
		Inward string `json:"inward"`
	} `json:"type"`
	InwardIssue *struct {
		Key string `json:"key"`
	} `json:"inwardIssue"`
}

func (h *jiraHarness) payload(key string, rec *jiraIssueRecord) map[string]any {
	links := make([]jiraPayloadLink, len(rec.nativeDeps))
	for i, id := range rec.nativeDeps {
		l := jiraPayloadLink{}
		l.Type.Inward = "is blocked by"
		l.InwardIssue = &struct {
			Key string `json:"key"`
		}{Key: id}
		links[i] = l
	}
	return map[string]any{
		"key": key,
		"fields": map[string]any{
			"summary":     rec.title,
			"description": rec.body,
			"status": map[string]any{
				"name": "Open",
				"statusCategory": map[string]any{
					"key": "new",
				},
			},
			"labels":     rec.labels,
			"issuelinks": links,
		},
	}
}

func (h *jiraHarness) handle(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search":
		if h.cloud {
			w.WriteHeader(http.StatusGone)
			return
		}
		out := h.matching(r.URL.Query().Get("jql"), fullPayload)

		// Genuinely paginate on startAt/maxResults (issue #2265), matching the
		// real Jira search response shape jiraSearchPayload decodes. total is
		// the full matching count; doSearch stops once rows received reach it
		// (and errors on an empty page before it).
		startAt := 0
		if s := r.URL.Query().Get("startAt"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v >= 0 {
				startAt = v
			}
		}
		// maxResults=0 is a valid zero-row page: real Jira returns no issues
		// and the full total, which the demand probe reads.
		maxResults := len(out)
		if m := r.URL.Query().Get("maxResults"); m != "" {
			if v, err := strconv.Atoi(m); err == nil && v >= 0 {
				maxResults = v
			}
		}
		if h.pageCap > 0 && maxResults > h.pageCap {
			maxResults = h.pageCap
		}
		window := pageWindow(out, startAt, maxResults)
		json.NewEncoder(w).Encode(map[string]any{
			"issues":     window,
			"startAt":    startAt,
			"maxResults": maxResults,
			"total":      len(out),
		})
		return

	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search/jql":
		if !h.cloud {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		// Like real Cloud, search/jql returns only the id unless fields names
		// what it should include, so a missing fields param fails the contract.
		shape := idOnly
		if q.Get("fields") != "" {
			shape = fullPayload
		}
		out := h.matching(q.Get("jql"), shape)

		// The token is opaque to the client; here it encodes the next offset.
		// maxResults is honoured, and no total is returned.
		start := 0
		if tok := q.Get("nextPageToken"); tok != "" {
			v, err := strconv.Atoi(strings.TrimPrefix(tok, "off-"))
			if err != nil || v < 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			start = v
		}
		size := len(out)
		if m := q.Get("maxResults"); m != "" {
			if v, err := strconv.Atoi(m); err == nil && v > 0 {
				size = v
			}
		}
		resp := map[string]any{"issues": pageWindow(out, start, size)}
		if end := start + size; end < len(out) {
			resp["nextPageToken"] = fmt.Sprintf("off-%d", end)
			resp["isLast"] = false
		} else {
			resp["isLast"] = true
		}
		json.NewEncoder(w).Encode(resp)
		return

	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/approximate-count":
		if !h.cloud {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			JQL string `json:"jql"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{"count": len(h.matching(body.JQL, fullPayload))})
		return

	case r.Method == http.MethodGet && matchIssuePath(r.URL.Path) != "":
		num := matchIssuePath(r.URL.Path)
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rec.failGET {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"errorMessages":["simulated native lookup failure"]}`)
			return
		}
		json.NewEncoder(w).Encode(h.payload(num, rec))
		return

	case r.Method == http.MethodGet && issueCommentPathRe.MatchString(r.URL.Path):
		num := issueCommentPathRe.FindStringSubmatch(r.URL.Path)[1]
		out := []jiraWireComment{}
		if rec, ok := h.issues[num]; ok {
			out = append(out, rec.comments...)
		}
		json.NewEncoder(w).Encode(map[string]any{"comments": out})
		return

	case r.Method == http.MethodPut && matchIssuePath(r.URL.Path) != "":
		num := matchIssuePath(r.URL.Path)
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Update struct {
				Labels []map[string]string `json:"labels"`
			} `json:"update"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, op := range body.Update.Labels {
			if remove, ok := op["remove"]; ok {
				rec.labels = removeString(rec.labels, remove)
			}
			if add, ok := op["add"]; ok && !contains(rec.labels, add) {
				rec.labels = append(rec.labels, add)
			}
		}
		w.WriteHeader(http.StatusOK)
		return

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

var (
	issuePathRe        = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)$`)
	issueCommentPathRe = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)/comment$`)
)

func matchIssuePath(path string) string {
	m := issuePathRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1]
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(ss []string, s string) []string {
	var out []string
	for _, v := range ss {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func TestJiraClient_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newJiraHarness(t))
}

func TestJiraClient_TrackerContract_Cloud(t *testing.T) {
	forgetest.RunTrackerContract(t, newJiraCloudHarness(t))
}

// seedAndListPaged seeds n Dispatchable issues PROJ-1..PROJ-n and asserts
// ListIssues returns every one in creation order: h.order's append sequence
// stands in for Jira's created-time ordering.
func seedAndListPaged(t *testing.T, h *jiraHarness, n int) {
	t.Helper()
	var want []string
	for i := 1; i <= n; i++ {
		num := fmt.Sprintf("PROJ-%d", i)
		want = append(want, num)
		h.SeedIssue(forge.Issue{
			Number: num,
			Title:  "paged",
			Labels: []string{testLabels.Dispatchable},
		})
	}

	issues, err := h.Tracker().ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(issues) != n {
		t.Fatalf("ListIssues(Dispatchable) returned %d issues, want %d", len(issues), n)
	}
	for i, iss := range issues {
		if iss.Number != want[i] {
			t.Fatalf("ListIssues(Dispatchable)[%d].Number = %q, want %q (creation order not preserved)", i, iss.Number, want[i])
		}
	}
}

// Seeding more than forge.ResultPageLimit issues forces doSearch (issue #2265)
// to walk at least two real pages.
func TestJiraClient_ListIssues_PaginatesAcrossMultipleRealPages(t *testing.T) {
	for name, newHarness := range map[string]func(*testing.T) *jiraHarness{
		"DC":    newJiraHarness,
		"Cloud": newJiraCloudHarness,
	} {
		t.Run(name, func(t *testing.T) {
			seedAndListPaged(t, newHarness(t), forge.ResultPageLimit+30)
		})
	}
}

// payloadShape picks how much of each issue the harness search returns.
type payloadShape int

const (
	fullPayload payloadShape = iota
	idOnly
)

// matching returns the payloads of the issues whose labels satisfy the JQL's
// labels clause, in creation order. With idOnly it returns only the id, as
// real Cloud search/jql does when no fields are requested.
func (h *jiraHarness) matching(jql string, shape payloadShape) []map[string]any {
	var wantLabel string
	if sub := jqlLabelClause.FindStringSubmatch(jql); sub != nil {
		wantLabel = sub[1]
	}
	var out []map[string]any
	for _, num := range h.order {
		rec := h.issues[num]
		if wantLabel != "" && !contains(rec.labels, wantLabel) {
			continue
		}
		if shape == fullPayload {
			out = append(out, h.payload(num, rec))
		} else {
			out = append(out, map[string]any{"id": num})
		}
	}
	return out
}

// pageWindow slices out[start:start+size], never nil so it encodes as [].
func pageWindow(out []map[string]any, start, size int) []map[string]any {
	if start >= len(out) {
		return []map[string]any{}
	}
	end := min(start+size, len(out))
	return out[start:end]
}

// A server that caps pages below the requested maxResults must not make
// doSearch skip rows: the walk advances by rows received, not a fixed stride.
func TestJiraClient_ListIssues_PaginatesAcrossServerCappedPages(t *testing.T) {
	h := newJiraHarness(t)
	h.pageCap = 30
	seedAndListPaged(t, h, 75)
}

// The server's page cap must never touch the maxResults=0 zero-row page
// CountReady reads the total from.
func TestJiraClient_CountReady_FullTotalUnderPageCap(t *testing.T) {
	h := newJiraHarness(t)
	h.pageCap = 30
	const seeded = 75
	for i := 1; i <= seeded; i++ {
		h.SeedIssue(forge.Issue{
			Number: fmt.Sprintf("PROJ-%d", i),
			Title:  "counted",
			Labels: []string{testLabels.Dispatchable},
		})
	}

	dc, ok := h.Tracker().(forge.DemandCounter)
	if !ok {
		t.Fatal("tracker does not implement forge.DemandCounter")
	}
	got, err := dc.CountReady(false)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if got != seeded {
		t.Errorf("CountReady = %d, want %d (the full total, not the page cap)", got, seeded)
	}
}
