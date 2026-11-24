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
	resolution string   // resolution the last transition posted
	done       bool     // the issue sits in statusCategory done, closed under resolution
	posts      int      // transition POSTs received
	// labelsAtPost are the labels the issue carried when the last transition POST
	// arrived, so a test can tell a strip made before it from one made after.
	labelsAtPost []string
	comments     []jiraWireComment
}

// jiraWireTransitionField is one screen field of a transition, as Jira reports
// it under expand=transitions.fields.
type jiraWireTransitionField struct {
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
	Name            string `json:"name"`
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
// one GET request, so no jira harness is forgetest.NativeFailureIsolatable
// (issue #1544 AC2).
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
	// transitionFields are the screen fields a transition to the named status
	// reports; every workflow status is a transition target from any status.
	transitionFields map[string]map[string]jiraWireTransitionField
	statusCalls      int
	statusesFail     bool
	// transitionsFail makes GET .../transitions answer 500.
	transitionsFail bool

	// commentStatus is the status POST .../comment answers with; 0 means 201.
	commentStatus int
	// myselfStatus is the status GET /myself answers with; 0 means 200.
	myselfStatus int
	// searchReject, when set, is the message every search and count answers 400
	// with, in Jira's errorMessages shape (an unknown Component in JQL).
	searchReject string

	cloud bool // serves Cloud's search/jql + approximate-count; rejects v2 /search (410)
	srv   *httptest.Server
}

var jqlLabelClause = regexp.MustCompile(`labels = "([^"]+)"`)
var jqlStatusClause = regexp.MustCompile(`status = "([^"]+)"`)
var jqlDoneClause = regexp.MustCompile(`statusCategory (!?=) Done`)

// newJiraBackend serves the scripted Jira API from start's server; the
// Connector harness needs TLS, which the Connector host requires of every
// route.
func newJiraBackend(t *testing.T, cloud bool, start func(http.Handler) *httptest.Server) *jiraHarness {
	h := &jiraHarness{cloud: cloud, issues: map[string]*jiraIssueRecord{}}
	h.srv = start(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)
	return h
}

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
					"key": categoryOf(rec),
				},
			},
			"labels":     rec.labels,
			"issuelinks": links,
		},
	}
	if rec.done {
		fields := out["fields"].(map[string]any)
		fields["resolution"] = map[string]any{"name": rec.resolution}
		fields["resolutiondate"] = "2026-01-02T03:04:05.000+0000"
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

	if h.searchReject != "" && strings.Contains(r.URL.Path, "/search") {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"errorMessages": []string{h.searchReject}, "errors": map[string]string{}})
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search":
		if h.cloud {
			w.WriteHeader(http.StatusGone)
			return
		}
		out := h.matching(r.URL.Query().Get("jql"), fullPayload, r.URL.Query().Get("properties"))

		// Genuinely paginate on startAt/maxResults (issue #2265) in real Jira's
		// response shape; total is the full matching count, since the
		// Connector's searchServer stops once rows reach it and errors on an
		// empty page before it.
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
		if h.commentStatus != 0 {
			w.WriteHeader(h.commentStatus)
			return
		}
		var c jiraWireComment
		json.NewDecoder(r.Body).Decode(&c)
		// Jira stamps the authenticated user and the creation time on a
		// posted comment; the request body carries neither.
		c.Author.DisplayName = "spindrift-bot"
		c.Created = "2024-01-02T03:04:05.000+0000"
		rec.comments = append(rec.comments, c)
		w.WriteHeader(http.StatusCreated)
		return

	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/label":
		// The site-wide label list: every label on any issue, one page.
		values := []string{}
		for _, num := range h.order {
			for _, l := range h.issues[num].labels {
				if !contains(values, l) {
					values = append(values, l)
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"values": values, "total": len(values), "isLast": true})
		return

	case r.Method == http.MethodGet && issueTransitionsPathRe.MatchString(r.URL.Path):
		h.listTransitions(w, issueTransitionsPathRe.FindStringSubmatch(r.URL.Path)[1])
		return

	case r.Method == http.MethodPost && issueTransitionsPathRe.MatchString(r.URL.Path):
		h.doTransition(w, r, issueTransitionsPathRe.FindStringSubmatch(r.URL.Path)[1])
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

// listTransitions serves GET /issue/{key}/transitions: one transition per
// workflow status, its id the status's 1-based position.
func (h *jiraHarness) listTransitions(w http.ResponseWriter, num string) {
	if _, ok := h.issues[num]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if h.transitionsFail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type transition struct {
		ID string `json:"id"`
		To struct {
			Name string `json:"name"`
		} `json:"to"`
		Fields map[string]jiraWireTransitionField `json:"fields"`
	}
	out := []transition{}
	for i, name := range h.workflowStatuses {
		tr := transition{ID: strconv.Itoa(i + 1), Fields: h.transitionFields[name]}
		tr.To.Name = name
		out = append(out, tr)
	}
	json.NewEncoder(w).Encode(map[string]any{"transitions": out})
}

// doTransition serves POST /issue/{key}/transitions: the workflow status moves
// to the posted transition's target and the posted resolution is recorded.
func (h *jiraHarness) doTransition(w http.ResponseWriter, r *http.Request, num string) {
	rec, ok := h.issues[num]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var body struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
		Fields struct {
			Resolution struct {
				Name string `json:"name"`
			} `json:"resolution"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	i, err := strconv.Atoi(body.Transition.ID)
	if err != nil || i < 1 || i > len(h.workflowStatuses) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rec.posts++
	rec.labelsAtPost = append([]string(nil), rec.labels...)
	rec.status = h.workflowStatuses[i-1]
	rec.resolution = body.Fields.Resolution.Name
	w.WriteHeader(http.StatusNoContent)
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
	issuePathRe            = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)$`)
	issueCommentPathRe     = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)/comment$`)
	issueTransitionsPathRe = regexp.MustCompile(`^/rest/api/2/issue/([^/]+)/transitions$`)
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

// payloadShape picks how much of each issue the harness search returns.
type payloadShape int

const (
	fullPayload payloadShape = iota
	idOnly
)

// categoryOf is the status category a record reports.
func categoryOf(rec *jiraIssueRecord) string {
	if rec.done {
		return "done"
	}
	return "new"
}

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
	category := jqlDoneClause.FindStringSubmatch(jql)
	if sub := jqlLabelClause.FindStringSubmatch(jql); sub != nil {
		wantLabel = sub[1]
	}
	if sub := jqlStatusClause.FindStringSubmatch(jql); sub != nil {
		wantStatus = sub[1]
	}
	var out []map[string]any
	for _, num := range h.order {
		rec := h.issues[num]
		if category != nil && rec.done != (category[1] == "=") {
			continue
		}
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

// testLabels mirrors the lifecycle-label set in lib/env-schema.nix (issue
// #460).
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// metaField is one create-screen field as Jira's createmeta reports it.
type metaField struct {
	FieldID         string `json:"fieldId"`
	Name            string `json:"name"`
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
}

type metaType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	createMetaTypesRe  = regexp.MustCompile(`^/rest/api/2/issue/createmeta/([^/]+)/issuetypes$`)
	createMetaFieldsRe = regexp.MustCompile(`^/rest/api/2/issue/createmeta/([^/]+)/issuetypes/([^/]+)$`)
)

// createMeta serves the per-project create-metadata endpoints in the two wire
// shapes Jira ships: Cloud nests rows under "issueTypes"/"fields" and counts
// with "total"; Server/DC nests them under "values" and ends with "isLast".
func (h *jiraHarness) createMeta(w http.ResponseWriter, r *http.Request) {
	var rows []any
	if m := createMetaTypesRe.FindStringSubmatch(r.URL.Path); m != nil && m[1] == "PROJ" {
		for i, name := range h.issueTypes() {
			rows = append(rows, metaType{ID: strconv.Itoa(10001 + i), Name: name})
		}
		h.writeMetaPage(w, r, "issueTypes", rows)
		return
	}
	if m := createMetaFieldsRe.FindStringSubmatch(r.URL.Path); m != nil && m[1] == "PROJ" {
		for _, f := range h.metaFields {
			rows = append(rows, f)
		}
		h.writeMetaPage(w, r, "fields", rows)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *jiraHarness) writeMetaPage(w http.ResponseWriter, r *http.Request, cloudKey string, rows []any) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
	size, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if size <= 0 || (h.metaPageSize > 0 && size > h.metaPageSize) {
		size = h.metaPageSize
	}
	if size <= 0 {
		size = len(rows)
	}
	end := min(start+size, len(rows))
	page := []any{}
	if start < len(rows) {
		page = rows[start:end]
	}
	body := map[string]any{"startAt": start, "maxResults": size, "total": len(rows)}
	if h.cloud {
		body[cloudKey] = page
	} else {
		body["values"] = page
		body["isLast"] = end >= len(rows)
	}
	json.NewEncoder(w).Encode(body)
}

var projectStatusesPathRe = regexp.MustCompile(`^/rest/api/2/project/([^/]+)/statuses$`)

// projectStatuses serves the per-issue-type status list Jira's project
// statuses endpoint returns: the same workflow statuses repeat under each type.
func (h *jiraHarness) projectStatuses(w http.ResponseWriter, r *http.Request) {
	h.statusCalls++ // handle holds h.mu
	if h.statusesFail {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if projectStatusesPathRe.FindStringSubmatch(r.URL.Path)[1] != "PROJ" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	type status struct {
		Name string `json:"name"`
	}
	var statuses []status
	for _, n := range h.workflowStatuses {
		statuses = append(statuses, status{Name: n})
	}
	out := []map[string]any{}
	for _, ty := range h.issueTypes() {
		out = append(out, map[string]any{"name": ty, "statuses": statuses})
	}
	json.NewEncoder(w).Encode(out)
}
