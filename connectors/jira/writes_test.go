package main

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const transitionsBody = `{"transitions":[
	{"id":"11","to":{"name":"In Progress"},"fields":{}},
	{"id":"31","to":{"name":"Done"},"fields":{
		"resolution":{"required":true,"hasDefaultValue":false,"name":"Resolution"}}},
	{"id":"41","to":{"name":"Review"},"fields":{
		"customfield_1":{"required":true,"hasDefaultValue":false,"name":"Team"},
		"customfield_2":{"required":true,"hasDefaultValue":true,"name":"Sprint"},
		"customfield_3":{"required":false,"name":"Notes"}}},
	{"id":"51","to":{"name":"Blocked"},"fields":{"customfield_1":{"required":true}}}]}`

// transitionServer answers the transitions GET and 204s the POST.
func transitionServer(t *testing.T) *[]connector.Request {
	return script(t, func(r connector.Request, u *url.URL) (int, string) {
		switch {
		case r.Method == "GET" && u.Path == "/jira/rest/api/2/issue/ENG-7/transitions":
			return 200, transitionsBody
		case r.Method == "POST" && u.Path == "/jira/rest/api/2/issue/ENG-7/transitions":
			return 204, ""
		}
		return 404, "{}"
	})
}

// req builds a request for state against a mapping of state to status.
func req(state string, mapping map[string]tracker.StatusTarget) tracker.TransitionRequest {
	return tracker.TransitionRequest{State: state, Mapping: mapping}
}

// to maps one state to a status and resolution.
func to(state, status, resolution string) tracker.TransitionRequest {
	return req(state, map[string]tracker.StatusTarget{state: {Status: status, Resolution: resolution}})
}

func TestTransitionUnmappedSendsNothing(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", req("in-progress", map[string]tracker.StatusTarget{"complete": {Status: "Done"}}))
	if err != nil || res.Outcome != tracker.TransitionUnmapped {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v", *got)
	}
}

func TestTransitionMoves(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", to("in-progress", "in progress", ""))
	if err != nil || res.Outcome != tracker.TransitionMoved {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(*got) != 2 {
		t.Fatalf("requests = %v", *got)
	}
	g := (*got)[0]
	if g.Method != "GET" || g.URL != site+"/rest/api/2/issue/ENG-7/transitions?expand=transitions.fields" {
		t.Errorf("GET = %+v", g)
	}
	p := (*got)[1]
	if p.Method != "POST" || p.Headers["Content-Type"] != "application/json" || p.Body != `{"transition":{"id":"11"}}` {
		t.Errorf("POST = %+v", p)
	}
}

func TestTransitionAppliesResolution(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", to("complete", "Done", "Fixed"))
	if err != nil || res.Outcome != tracker.TransitionMoved {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte((*got)[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"transition": map[string]any{"id": "31"},
		"fields":     map[string]any{"resolution": map[string]any{"name": "Fixed"}},
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v", body)
	}
}

func TestTransitionUnavailableListsOffered(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", to("complete", "Shipped", ""))
	if err != nil {
		t.Fatal(err)
	}
	want := tracker.TransitionResult{
		Outcome: tracker.TransitionUnavailable,
		Status:  "Shipped",
		Offered: []string{"In Progress", "Done", "Review", "Blocked"},
	}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("res = %+v", res)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %v, want only the GET", *got)
	}
}

func TestTransitionRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		req  tracker.TransitionRequest
		want []string
	}{
		"resolution not on screen": {
			to("in-progress", "In Progress", "Fixed"),
			[]string{"no resolution field", `"Fixed"`},
		},
		"required field": {
			to("in-progress", "Review", ""),
			[]string{"requires field", "Team (customfield_1)"},
		},
		"required field without a name": {
			to("failed", "Blocked", ""),
			[]string{"requires field", "customfield_1"},
		},
		"required resolution not supplied": {
			to("complete", "Done", ""),
			[]string{"requires field", "Resolution (resolution)"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := transitionServer(t)
			res, err := transition(cloud, "ENG-7", tc.req)
			if err == nil {
				t.Fatalf("res = %+v, want error", res)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if len(*got) != 1 {
				t.Errorf("requests = %v, want only the GET", *got)
			}
		})
	}
}

func TestTransitionPostFailureIsError(t *testing.T) {
	script(t, func(r connector.Request, u *url.URL) (int, string) {
		if r.Method == "GET" {
			return 200, transitionsBody
		}
		return 400, "{}"
	})
	if _, err := transition(cloud, "ENG-7", to("in-progress", "In Progress", "")); err == nil {
		t.Fatal("want error")
	}
}

func TestEditLabelsOneWriteRemovesFirst(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 204, "" })
	if err := editLabels(cloud, "ENG-7", []string{"a", ""}, []string{"x", "", "y"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %v", *got)
	}
	r := (*got)[0]
	want := `{"update":{"labels":[{"remove":"x"},{"remove":"y"},{"add":"a"}]}}`
	if r.Method != "PUT" || r.URL != site+"/rest/api/2/issue/ENG-7" || r.Body != want {
		t.Fatalf("request = %+v", r)
	}
}

func TestEditLabelsNoOpsSendsNothing(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 204, "" })
	if err := editLabels(cloud, "ENG-7", []string{""}, nil); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v", *got)
	}
}

func TestEditLabelsHTTPError(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 403, "{}" })
	if err := editLabels(cloud, "ENG-7", []string{"a"}, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestListLabelsPages(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Path != "/jira/rest/api/2/label" {
			return 404, "{}"
		}
		if u.Query().Get("startAt") == "2" {
			return 200, `{"startAt":2,"total":3,"isLast":true,"values":["c"]}`
		}
		return 200, `{"startAt":0,"total":3,"isLast":false,"values":["a","b"]}`
	})
	names, err := listLabels(cloud)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v", names)
	}
	if len(*got) != 2 {
		t.Fatalf("requests = %d, want 2", len(*got))
	}
}

func TestListLabelsUnpagedBody(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/label", `{"values":["a","b"]}`)
	names, err := listLabels(server)
	if err != nil || !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("names = %v, err = %v", names, err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d, want 1", len(*got))
	}
}

func TestListLabelsEmptyIsNotNil(t *testing.T) {
	serve(t, "/jira/rest/api/2/label", `{"isLast":true,"values":[]}`)
	names, err := listLabels(cloud)
	if err != nil || names == nil || len(names) != 0 {
		t.Fatalf("names = %#v, err = %v", names, err)
	}
}

func TestListLabelsStopsOnEmptyPage(t *testing.T) {
	serve(t, "/jira/rest/api/2/label", `{"total":5,"isLast":false,"values":[]}`)
	if _, err := listLabels(cloud); err != nil {
		t.Fatal(err)
	}
}

func TestListLabelsPageCap(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"total":1000000,"isLast":false,"values":["a"]}`
	})
	if _, err := listLabels(cloud); err == nil {
		t.Fatal("want a page-cap error")
	}
}

func TestTransitionStateAbsentFromMappingIsUnmapped(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", req("failed", map[string]tracker.StatusTarget{"complete": {Status: "Done"}}))
	if err != nil || res.Outcome != tracker.TransitionUnmapped || res.Status != "" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v", *got)
	}
}

func TestTransitionEmptyMappedStatusIsUnmapped(t *testing.T) {
	got := transitionServer(t)
	res, err := transition(cloud, "ENG-7", to("complete", "", "Fixed"))
	if err != nil || res.Outcome != tracker.TransitionUnmapped {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v", *got)
	}
}

func TestTransitionAmbiguousMayShareFailedStatus(t *testing.T) {
	got := transitionServer(t)
	m := map[string]tracker.StatusTarget{"failed": {Status: "Blocked"}, "ambiguous": {Status: "blocked"}, "in-progress": {Status: "In Progress"}}
	res, err := transition(cloud, "ENG-7", req("in-progress", m))
	if err != nil || res.Outcome != tracker.TransitionMoved {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(*got) != 2 {
		t.Fatalf("requests = %v", *got)
	}
}

func TestTransitionSharedStatusBetweenOtherStatesIsError(t *testing.T) {
	for name, m := range map[string]map[string]tracker.StatusTarget{
		"complete and failed":          {"complete": {Status: "Done"}, "failed": {Status: "done"}},
		"in-progress and dispatchable": {"in-progress": {Status: "Open"}, "dispatchable": {Status: "OPEN"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := transitionServer(t)
			res, err := transition(cloud, "ENG-7", req("complete", m))
			if err == nil || !strings.Contains(err.Error(), "only failed and ambiguous may share a status") {
				t.Fatalf("res = %+v, err = %v, want a shared-status error", res, err)
			}
			if len(*got) != 0 {
				t.Fatalf("requests = %v", *got)
			}
		})
	}
}

func TestTransitionSharedStatusErrorNamesSortedPair(t *testing.T) {
	m := map[string]tracker.StatusTarget{"failed": {Status: "Done"}, "complete": {Status: "Done"}}
	_, err := transition(cloud, "ENG-7", req("failed", m))
	if want := `states "complete" and "failed" both map to status "Done"`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestTransitionMovedResultCarriesStatus(t *testing.T) {
	transitionServer(t)
	res, err := transition(cloud, "ENG-7", to("in-progress", "in progress", ""))
	if err != nil || res.Status != "in progress" {
		t.Fatalf("res = %+v, err = %v, want the mapped status named", res, err)
	}
}

// commentBody decodes a comment POST body, leaving properties absent when the
// request carried none.
func commentBody(t *testing.T, r connector.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCommentPostsWikiBody(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 201, "{}" })
	if err := comment(cloud, "ENG-7", tracker.CommentRequest{Body: "**done** see `x_y`"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %v", *got)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.URL != site+"/rest/api/2/issue/ENG-7/comment" {
		t.Fatalf("request = %+v", r)
	}
	m := commentBody(t, r)
	if m["body"] != `*done* see {{x\_y}}` {
		t.Errorf("body = %q", m["body"])
	}
	if _, ok := m["properties"]; ok {
		t.Errorf("non-research comment carries properties: %s", r.Body)
	}
}

func TestCommentResearchSetsProperty(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 201, "{}" })
	if err := comment(cloud, "ENG-7", tracker.CommentRequest{Body: "verdict", Research: true}); err != nil {
		t.Fatal(err)
	}
	want := `{"body":"verdict","properties":[{"key":"spindrift-research","value":true}]}`
	if r := (*got)[0]; r.Body != want {
		t.Errorf("body = %s, want %s", r.Body, want)
	}
}

func TestCommentMarkerLinePassesThrough(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 201, "{}" })
	in := "**a**\n<!-- spindrift-butler: `k_1` **x** -->"
	if err := comment(cloud, "ENG-7", tracker.CommentRequest{Body: in}); err != nil {
		t.Fatal(err)
	}
	if b := commentBody(t, (*got)[0])["body"]; b != "*a*\n<!-- spindrift-butler: `k_1` **x** -->" {
		t.Errorf("body = %q", b)
	}
}

func TestCommentHTTPError(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 403, "{}" })
	if err := comment(cloud, "ENG-7", tracker.CommentRequest{Body: "x"}); err == nil {
		t.Fatal("want error")
	}
}

func issueServer(t *testing.T, status int, body string) *[]connector.Request {
	return script(t, func(connector.Request, *url.URL) (int, string) { return status, body })
}

func TestFileIssueSendsFieldsAndReturnsBrowseURL(t *testing.T) {
	got := issueServer(t, 201, `{"id":"1","key":"ENG-9"}`)
	url, err := fileIssue(cloud, tracker.IssueRequest{
		Title: "t", Body: "**b**", Labels: []string{"a", "b"},
		DedupMarker: "<!-- spindrift-dedup: k -->", IssueType: "Task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if url != site+"/browse/ENG-9" {
		t.Errorf("url = %q", url)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.URL != site+"/rest/api/2/issue" {
		t.Fatalf("request = %+v", r)
	}
	want := `{"fields":{"description":"*b*","issuetype":{"name":"Task"},"labels":["a","b"],"project":{"key":"ENG"},"summary":"t"},` +
		`"properties":[{"key":"spindrift.dedup","value":{"marker":"<!-- spindrift-dedup: k -->"}}]}`
	var gotM, wantM any
	if err := json.Unmarshal([]byte(r.Body), &gotM); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantM); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotM, wantM) {
		t.Errorf("body = %s\nwant   %s", r.Body, want)
	}
}

func TestFileIssueOmitsEmptyDedupAndNeverNullLabels(t *testing.T) {
	got := issueServer(t, 201, `{"key":"ENG-9"}`)
	if _, err := fileIssue(cloud, tracker.IssueRequest{Title: "t", Body: "b", IssueType: "Bug"}); err != nil {
		t.Fatal(err)
	}
	b := (*got)[0].Body
	if strings.Contains(b, "properties") || !strings.Contains(b, `"labels":[]`) {
		t.Errorf("body = %s", b)
	}
}

func TestFileIssueTrimsTrailingSlashFromSite(t *testing.T) {
	issueServer(t, 201, `{"key":"ENG-9"}`)
	tg := cloud
	tg.API = site + "/"
	url, err := fileIssue(tg, tracker.IssueRequest{Title: "t", IssueType: "Task"})
	if err != nil || url != site+"/browse/ENG-9" {
		t.Errorf("url = %q, err = %v", url, err)
	}
}

func TestFileIssueErrors(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		typ    string
	}{
		"no key":     {201, `{}`, "Task"},
		"http error": {400, `{"errors":{}}`, "Task"},
		"no type":    {201, `{"key":"ENG-9"}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			issueServer(t, c.status, c.body)
			if _, err := fileIssue(cloud, tracker.IssueRequest{Title: "t", IssueType: c.typ}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
