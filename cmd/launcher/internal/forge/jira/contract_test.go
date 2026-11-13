package jira_test

import (
	"encoding/json"
	"fmt"
	"io"
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
	// props are the issue entity properties, as raw JSON values keyed by
	// property key; served back only where a request names the key.
	props      map[string]json.RawMessage
	nativeDeps []string
	blocks     []string // issues this one blocks: outward "blocks" links
	wireKey    string   // when set, the key the payload reports instead of the requested one
	failGET    bool     // simulates the native-API error the issue's GET returns
	status     string   // workflow status the payload reports and a status clause matches; "" is "Open"
	comments   []jiraWireComment
}

// jiraWireComment mirrors Jira's native comment JSON independently of the
// adapter's own payload struct, so a tag typo in the adapter fails the
// contract instead of round-tripping through a shared type.
type jiraWireComment struct {
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created    string             `json:"created"`
	Body       string             `json:"body"`
	Properties []jiraWireProperty `json:"properties,omitempty"`
}

type jiraWireProperty struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
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

	// metaFields are the create-screen fields the fake project reports for every
	// issue type; metaPageSize, when > 0, caps rows per createmeta page.
	metaFields   []metaField
	metaPageSize int

	// workflowStatuses are the status names the fake project's workflow has,
	// served per issue type by the project statuses endpoint; statusCalls counts
	// the reads and statusesFail makes the endpoint answer 403.
	workflowStatuses []string
	statusCalls      int
	statusesFail     bool

	// myselfStatus is the status GET /myself answers with; 0 means 200.
	myselfStatus int

	cloud bool // serves Cloud's search/jql + approximate-count; rejects v2 /search (410)
	srv   *httptest.Server
	tr    forge.IssueTracker
}

var jqlLabelClause = regexp.MustCompile(`labels = "([^"]+)"`)
var jqlStatusClause = regexp.MustCompile(`status = "([^"]+)"`)

// newJiraHarness builds a Server/DC harness; newJiraCloudHarness a Cloud one,
// which the adapter selects by configuring an Email.
func newJiraHarness(t *testing.T) *jiraHarness { return newJiraHarnessMode(t, false) }

func newJiraCloudHarness(t *testing.T) *jiraHarness { return newJiraHarnessMode(t, true) }

func newJiraHarnessMode(t *testing.T, cloud bool) *jiraHarness {
	h := newJiraBackend(t, cloud, httptest.NewServer)
	email := ""
	if cloud {
		email = "bot@example.com"
	}
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

// newJiraBackend serves the scripted Jira API from start's server, with no
// native tracker: the Connector harness needs TLS, which the Connector host
// requires of every route.
func newJiraBackend(t *testing.T, cloud bool, start func(http.Handler) *httptest.Server) *jiraHarness {
	h := &jiraHarness{cloud: cloud, issues: map[string]*jiraIssueRecord{}}
	h.srv = start(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
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
		Inward  string `json:"inward,omitempty"`
		Outward string `json:"outward,omitempty"`
	} `json:"type"`
	InwardIssue *struct {
		Key string `json:"key"`
	} `json:"inwardIssue,omitempty"`
	OutwardIssue *struct {
		Key string `json:"key"`
	} `json:"outwardIssue,omitempty"`
}

func (h *jiraHarness) payload(key string, rec *jiraIssueRecord, wantProps string) map[string]any {
	links := make([]jiraPayloadLink, len(rec.nativeDeps), len(rec.nativeDeps)+len(rec.blocks))
	for i, id := range rec.nativeDeps {
		l := jiraPayloadLink{}
		l.Type.Inward = "is blocked by"
		l.InwardIssue = &struct {
			Key string `json:"key"`
		}{Key: id}
		links[i] = l
	}
	for _, id := range rec.blocks {
		l := jiraPayloadLink{}
		l.Type.Outward = "blocks"
		l.OutwardIssue = &struct {
			Key string `json:"key"`
		}{Key: id}
		links = append(links, l)
	}
	if rec.wireKey != "" {
		key = rec.wireKey
	}
	out := map[string]any{
		"key": key,
		"fields": map[string]any{
			"summary":     rec.title,
			"description": rec.body,
			"status": map[string]any{
				"name": statusOf(rec),
				"statusCategory": map[string]any{
					"key": "new",
				},
			},
			"labels":     rec.labels,
			"issuelinks": links,
		},
	}
	// Like real Jira, properties come back only when the request names them.
	requested := map[string]json.RawMessage{}
	for _, k := range strings.Split(wantProps, ",") {
		if v, ok := rec.props[k]; ok && k != "" {
			requested[k] = v
		}
	}
	if wantProps != "" {
		out["properties"] = requested
	}
	return out
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
		out := h.matching(r.URL.Query().Get("jql"), fullPayload, r.URL.Query().Get("properties"))

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
		out := h.matching(q.Get("jql"), shape, q.Get("properties"))

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
		json.NewEncoder(w).Encode(map[string]any{"count": len(h.matching(body.JQL, fullPayload, ""))})
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
		json.NewEncoder(w).Encode(h.payload(num, rec, r.URL.Query().Get("properties")))
		return

	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue":
		h.createIssue(w, r)
		return

	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/myself":
		if h.myselfStatus != 0 {
			w.WriteHeader(h.myselfStatus)
			return
		}
		fmt.Fprint(w, `{"accountId":"abc123"}`)
		return

	case r.Method == http.MethodGet && projectStatusesPathRe.MatchString(r.URL.Path):
		h.projectStatuses(w, r)
		return

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/createmeta/"):
		h.createMeta(w, r)
		return

	case r.Method == http.MethodGet && issueCommentPathRe.MatchString(r.URL.Path):
		num := issueCommentPathRe.FindStringSubmatch(r.URL.Path)[1]
		out := []jiraWireComment{}
		if rec, ok := h.issues[num]; ok {
			out = append(out, rec.comments...)
		}
		// Jira omits comment properties unless the caller expands them.
		if r.URL.Query().Get("expand") != "properties" {
			for i := range out {
				out[i].Properties = nil
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"comments": out})
		return

	case r.Method == http.MethodPost && issueCommentPathRe.MatchString(r.URL.Path):
		num := issueCommentPathRe.FindStringSubmatch(r.URL.Path)[1]
		rec, ok := h.issues[num]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var c jiraWireComment
		json.NewDecoder(r.Body).Decode(&c)
		rec.comments = append(rec.comments, c)
		w.WriteHeader(http.StatusCreated)
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

// failingIssueTitle is the title the fake's create rejects as Jira rejects a
// missing required field, the contract's failing-filing seed.
const failingIssueTitle = "fail-issue"

// createIssue serves POST /rest/api/2/issue the way Jira's create-issue API
// does: fields.project/issuetype are validated, entity properties ride as a
// top-level [{"key","value"}] array, and a rejection is a 400 with an
// "errors" map.
func (h *jiraHarness) createIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
			Summary     string   `json:"summary"`
			Description string   `json:"description"`
			Labels      []string `json:"labels"`
			IssueType   struct {
				Name string `json:"name"`
			} `json:"issuetype"`
		} `json:"fields"`
		Properties []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	reject := func(field, msg string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"errorMessages": []string{}, "errors": map[string]string{field: msg}})
	}
	f := req.Fields
	switch {
	case f.Project.Key != "PROJ":
		reject("project", "project is required")
	case !contains(h.issueTypes(), f.IssueType.Name):
		reject("issuetype", "issue type is invalid")
	case f.Summary == "" || f.Summary == failingIssueTitle:
		reject("summary", "You must specify a summary of the issue.")
	default:
		n := len(h.order) + 1
		key := fmt.Sprintf("PROJ-%d", n)
		for h.issues[key] != nil {
			n++
			key = fmt.Sprintf("PROJ-%d", n)
		}
		rec := &jiraIssueRecord{title: f.Summary, body: f.Description, labels: f.Labels, props: map[string]json.RawMessage{}}
		for _, p := range req.Properties {
			rec.props[p.Key] = p.Value
		}
		h.order = append(h.order, key)
		h.issues[key] = rec
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": strconv.Itoa(10000 + n), "key": key, "self": h.srv.URL + "/rest/api/2/issue/" + key})
	}
}

// issueTypes are the types the fake project defines.
func (h *jiraHarness) issueTypes() []string { return []string{"Task", "Bug"} }

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

// statusOf is the workflow status a record reports.
func statusOf(rec *jiraIssueRecord) string {
	if rec.status == "" {
		return "Open"
	}
	return rec.status
}

// matching returns the payloads of the issues satisfying the JQL's selector,
// in creation order: with both a status and a labels clause an issue matches
// either (as the Connector's query spells them), with neither every issue
// does. With idOnly it returns only the id, as real Cloud search/jql does
// when no fields are requested.
func (h *jiraHarness) matching(jql string, shape payloadShape, wantProps string) []map[string]any {
	var wantLabel, wantStatus string
	if sub := jqlLabelClause.FindStringSubmatch(jql); sub != nil {
		wantLabel = sub[1]
	}
	if sub := jqlStatusClause.FindStringSubmatch(jql); sub != nil {
		wantStatus = sub[1]
	}
	var out []map[string]any
	for _, num := range h.order {
		rec := h.issues[num]
		if (wantLabel != "" || wantStatus != "") &&
			!(wantLabel != "" && contains(rec.labels, wantLabel)) &&
			!(wantStatus != "" && statusOf(rec) == wantStatus) {
			continue
		}
		if shape == fullPayload {
			out = append(out, h.payload(num, rec, wantProps))
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

func TestJiraClient_ResearchMarker_RoundTripsThroughProperty(t *testing.T) {
	h := newJiraHarness(t)
	h.SeedIssue(forge.Issue{Number: "PROJ-9", Title: "t"})
	body := forge.ResearchMarker + "\n## Verdict\n\nstatus=recommend"
	if err := h.tr.Comment("PROJ-9", body); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	stored := h.issues["PROJ-9"].comments
	if len(stored) != 1 || strings.Contains(stored[0].Body, forge.ResearchMarker) {
		t.Fatalf("stored visible text must not carry the marker: %+v", stored)
	}
	got, err := h.tr.(forge.CommentLister).Comments("PROJ-9")
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0].Body, forge.ResearchMarker+"\n") {
		t.Fatalf("Comments = %+v, want marker restored on top", got)
	}

	text, err := forge.IssueText(h.tr, "PROJ-9", io.Discard)
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(text, forge.ResearchMarker) {
		t.Errorf("issue text lacks the research marker:\n%s", text)
	}
}
