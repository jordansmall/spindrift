package main

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const site = "https://example.atlassian.net/jira"

var (
	cloud  = tracker.Target{API: site, Repo: "ENG", Dialect: "cloud"}
	server = tracker.Target{API: site, Repo: "ENG", Dialect: "server"}
)

// script answers every request through the fake host with fn and records them.
func script(t *testing.T, fn func(r connector.Request, u *url.URL) (int, string)) *[]connector.Request {
	t.Helper()
	return host.Serve(t, func(r connector.Request) (connector.Response, error) {
		u, err := url.Parse(r.URL)
		if err != nil {
			t.Fatal(err)
		}
		status, body := fn(r, u)
		return connector.Response{Status: status, Body: []byte(body)}, nil
	})
}

// serve answers path with a fixed 200 body and 404s everything else.
func serve(t *testing.T, path, body string) *[]connector.Request {
	return script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Path == path {
			return 200, body
		}
		return 404, "{}"
	})
}

func classOf(t *testing.T, err error) connector.Class {
	t.Helper()
	var ce *connector.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *connector.Error", err)
	}
	return ce.Class
}

// statusClasses pins the literal class each status maps to, so a change to
// ClassForStatus cannot pass unnoticed.
var statusClasses = []struct {
	status int
	want   connector.Class
}{
	{500, connector.Transient}, {502, connector.Transient}, {429, connector.RateLimited},
	{404, connector.NotFound}, {401, connector.Auth}, {403, connector.Auth}, {400, connector.Permanent},
}

func TestIssueMapsRawFields(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/issue/ENG-7", `{
		"key":"ENG-7",
		"properties":{"spindrift.dedup":{"marker":"<!-- spindrift:dedup abc -->"}},
		"fields":{
			"summary":"s","description":"d\nmore",
			"status":{"name":"Done","statusCategory":{"key":"done"}},
			"labels":["x","y"],
			"resolution":{"name":"WON'T do"},
			"resolutiondate":"2025-03-04T05:06:07.123+0200"}}`)
	i, err := issue(cloud, "ENG-7")
	if err != nil {
		t.Fatal(err)
	}
	want := tracker.Issue{
		Number: "ENG-7", Title: "s", Body: "d\nmore", State: "closed",
		Status:           "Done",
		Labels:           []string{"x", "y"},
		ClosedAt:         "2025-03-04T05:06:07.123+02:00",
		ClosedNotPlanned: true,
		DedupMarker:      "<!-- spindrift:dedup abc -->",
	}
	if !reflect.DeepEqual(i, want) {
		t.Fatalf("issue = %#v", i)
	}
	r := (*got)[0]
	if r.Method != "GET" || r.Headers["Accept"] != "application/json" ||
		r.URL != site+"/rest/api/2/issue/ENG-7?properties=spindrift.dedup" {
		t.Fatalf("request = %#v", r)
	}
}

func TestIssueOpenIgnoresResolution(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-8", `{"key":"ENG-8","fields":{
		"summary":"s","description":"",
		"status":{"statusCategory":{"key":"indeterminate"}},
		"resolution":{"name":"Declined"},"resolutiondate":"2025-03-04T05:06:07.123+0200"}}`)
	i, err := issue(cloud, "ENG-8")
	if err != nil {
		t.Fatal(err)
	}
	if i.State != "open" || i.ClosedAt != "" || i.ClosedNotPlanned || i.DedupMarker != "" {
		t.Fatalf("issue = %#v", i)
	}
	if i.Labels == nil {
		t.Fatal("labels must be non-nil")
	}
}

func TestIssueClosedWithBadDate(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-9", `{"key":"ENG-9","fields":{
		"status":{"statusCategory":{"key":"done"}},
		"resolution":{"name":"Fixed"},"resolutiondate":"yesterday"}}`)
	i, err := issue(cloud, "ENG-9")
	if err != nil {
		t.Fatal(err)
	}
	if i.State != "closed" || i.ClosedAt != "" || i.ClosedNotPlanned {
		t.Fatalf("issue = %#v", i)
	}
}

// A key's "?" and "#" are escaped into the path.
func TestIssueKeyIsPathEscaped(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{}` })
	if _, err := issue(cloud, "ENG-1?y#z"); err != nil {
		t.Fatal(err)
	}
	if u := (*got)[0].URL; strings.Contains(u, "?y") {
		t.Fatalf("key reached the URL raw: %s", u)
	}
}

// A key that cannot stay one path segment is refused by the Connector before
// any request is sent (the host would refuse "." and ".." and close the
// Connector), and the Connector stays usable for the next call.
func TestIssueKeyRefusedBeforeAnyRequest(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/issue/ENG-1", `{"key":"ENG-1"}`)
	for _, key := range []string{"", ".", "..", "ENG-1/../../x", "a;b"} {
		if _, err := issue(cloud, key); err == nil {
			t.Errorf("issue(%q) succeeded, want a refusal", key)
		}
		if err := comment(cloud, key, tracker.CommentRequest{Body: "x"}); err == nil {
			t.Errorf("comment(%q) succeeded, want a refusal", key)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("requests sent for refused keys: %v", *got)
	}
	if _, err := issue(cloud, "ENG-1"); err != nil {
		t.Fatalf("issue after refusals: %v", err)
	}
}

func TestIssueHTTPError(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 500, "{}" })
	if _, err := issue(cloud, "ENG-1"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

// Jira says why it refused (an unknown Component in JQL, say) in the body;
// the error carries that reason, and a body that is not Jira's shape adds none.
func TestHTTPErrorCarriesJirasReason(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"errorMessages", `{"errorMessages":["The value 'orders-apii' does not exist for the field 'component'."],"errors":{}}`,
			"GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400: The value 'orders-apii' does not exist for the field 'component'."},
		{"field errors", `{"errorMessages":[],"errors":{"jql":"bad clause"}}`,
			"GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400: jql: bad clause"},
		{"not json", `<html>nope</html>`, "GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400"},
		{"empty", ``, "GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400"},
		{"multi-line", `{"errorMessages":["line one\r\nline two\u0007\tend"],"errors":{}}`,
			"GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400: line one  line two  end"},
		{"separators and bidi overrides", `{"errorMessages":["a\u2028b\u2029c\u202ed"],"errors":{}}`,
			"GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400: a b c d"},
		{"oversized", `{"errorMessages":["x` + strings.Repeat("é", maxReason) + `"],"errors":{}}`,
			"GET https://example.atlassian.net/jira/rest/api/2/issue/ENG-1?properties=spindrift.dedup: status 400: x" + strings.Repeat("é", maxReason/2-1) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script(t, func(connector.Request, *url.URL) (int, string) { return 400, tc.body })
			_, err := issue(cloud, "ENG-1")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCommentsReportResearchMarkerAsField(t *testing.T) {
	got := serve(t, "/jira/rest/api/2/issue/ENG-3/comment", `{"comments":[
		{"author":{"displayName":"Ann"},"created":"2025-01-02T03:04:05.000+0000","body":"verdict",
		 "properties":[{"key":"other","value":1},{"key":"spindrift-research","value":true}]},
		{"author":{"displayName":"Bob"},"created":"2025-01-03T03:04:05.000+0000","body":"hi"}]}`)
	cs, err := comments(cloud, "ENG-3")
	if err != nil {
		t.Fatal(err)
	}
	want := []tracker.Comment{
		{Author: "Ann", CreatedAt: "2025-01-02T03:04:05Z", Body: "verdict", ResearchMarker: true},
		{Author: "Bob", CreatedAt: "2025-01-03T03:04:05Z", Body: "hi"},
	}
	if !reflect.DeepEqual(cs, want) {
		t.Fatalf("comments = %#v", cs)
	}
	if u := (*got)[0].URL; u != site+"/rest/api/2/issue/ENG-3/comment?expand=properties" {
		t.Fatalf("url = %s", u)
	}
}

func TestCommentsReportAnonymousAuthor(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-3/comment", `{"comments":[
		{"created":"2025-01-02T03:04:05.000+0000","body":"anon"}]}`)
	cs, err := comments(cloud, "ENG-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Author != "Anonymous" {
		t.Fatalf("comments = %#v", cs)
	}
}

const links = `{"key":"ENG-4","fields":{"issuelinks":[
	{"type":{"inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"ENG-1"}},
	{"type":{"inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"ENG-2"}},
	{"type":{"inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"ENG-1"}},
	{"type":{"inward":"is cloned by","outward":"clones"},"inwardIssue":{"key":"ENG-50"}},
	{"type":{"inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"ENG-9"}},
	{"type":{"inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"ENG-8"}},
	{"type":{"inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"ENG-9"}},
	{"type":{"inward":"relates to","outward":"relates to"},"outwardIssue":{"key":"ENG-60"}}]}}`

func TestNativeDepsAndBlocks(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-4", links)
	deps, err := nativeDeps(cloud, "ENG-4")
	if err != nil || !reflect.DeepEqual(deps, []string{"ENG-1", "ENG-2"}) {
		t.Fatalf("deps = %v, %v", deps, err)
	}
	blocks, err := blocks(cloud, "ENG-4")
	if err != nil || !reflect.DeepEqual(blocks, []string{"ENG-9", "ENG-8"}) {
		t.Fatalf("blocks = %v, %v", blocks, err)
	}
}

func TestNoLinksIsEmptyNotNil(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-5", `{"key":"ENG-5","fields":{}}`)
	deps, err := nativeDeps(cloud, "ENG-5")
	if err != nil || deps == nil || len(deps) != 0 {
		t.Fatalf("deps = %#v, %v", deps, err)
	}
}

// issueJSON renders one search-result issue.
func issueJSON(key string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"t","description":"b","status":{"statusCategory":{"key":"new"}},"labels":["l"]}}`, key)
}

func TestCloudWalksTokens(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Path != "/jira/rest/api/2/search/jql" {
			return 404, "{}"
		}
		switch u.Query().Get("nextPageToken") {
		case "":
			return 200, `{"issues":[` + issueJSON("ENG-1") + `],"nextPageToken":"p2","isLast":false}`
		case "p2":
			return 200, `{"issues":[` + issueJSON("ENG-2") + `],"isLast":true}`
		}
		return 404, "{}"
	})
	issues, err := listIssues(cloud, tracker.Query{Label: "ready"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != "ENG-1" || issues[1].Number != "ENG-2" ||
		issues[0].Title != "t" || issues[0].Body != "b" || !reflect.DeepEqual(issues[0].Labels, []string{"l"}) {
		t.Fatalf("issues = %#v", issues)
	}
	q := mustQuery(t, (*got)[0])
	if q.Get("fields") != "summary,description,status,labels,resolution,resolutiondate" ||
		q.Get("properties") != "spindrift.dedup" || q.Get("maxResults") != "50" {
		t.Fatalf("query = %v", q)
	}
	if (*got)[0].Headers["Accept"] != "application/json" {
		t.Fatalf("headers = %v", (*got)[0].Headers)
	}
}

func TestCloudRefusesRepeatedToken(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"issues":[` + issueJSON("ENG-1") + `],"nextPageToken":"same","isLast":false}`
	})
	// First page returns "same"; the second request carries it and gets it back.
	if _, err := listIssues(cloud, tracker.Query{Label: "ready"}, 50); err == nil ||
		!strings.Contains(err.Error(), "nextPageToken") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudStopsAtLimit(t *testing.T) {
	calls := 0
	script(t, func(connector.Request, *url.URL) (int, string) {
		calls++
		return 200, `{"issues":[` + issueJSON("ENG-1") + `,` + issueJSON("ENG-2") + `],"nextPageToken":"more","isLast":false}`
	})
	issues, err := listIssues(cloud, tracker.Query{Label: "ready"}, 1)
	if err != nil || len(issues) != 1 || calls != 1 {
		t.Fatalf("issues = %v, err = %v, calls = %d", issues, err, calls)
	}
}

func TestServerWalksStartAt(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Path != "/jira/rest/api/2/search" {
			return 404, "{}"
		}
		switch u.Query().Get("startAt") {
		case "0":
			return 200, `{"total":3,"issues":[` + issueJSON("ENG-1") + `,` + issueJSON("ENG-2") + `]}`
		case "2":
			return 200, `{"total":3,"issues":[` + issueJSON("ENG-3") + `]}`
		}
		return 404, "{}"
	})
	issues, err := listIssues(server, tracker.Query{Status: "Ready"}, 50)
	if err != nil || len(issues) != 3 {
		t.Fatalf("issues = %v, err = %v", issues, err)
	}
	q := mustQuery(t, (*got)[0])
	if q.Get("properties") != "spindrift.dedup" || q.Get("fields") != "" {
		t.Fatalf("query = %v", q)
	}
}

func TestServerEmptyPageBeforeTotalErrors(t *testing.T) {
	script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Query().Get("startAt") == "0" {
			return 200, `{"total":5,"issues":[` + issueJSON("ENG-1") + `]}`
		}
		return 200, `{"total":5,"issues":[]}`
	})
	if _, err := listIssues(server, tracker.Query{Status: "Ready"}, 50); err == nil ||
		!strings.Contains(err.Error(), "empty page") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerRepeatedPageErrors(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"total":4,"issues":[` + issueJSON("ENG-1") + `,` + issueJSON("ENG-2") + `]}`
	})
	if _, err := listIssues(server, tracker.Query{Status: "Ready"}, 50); err == nil ||
		!strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownDialectErrors(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{}` })
	tgt := tracker.Target{API: site, Repo: "ENG", Dialect: "datacenter"}
	if _, err := listIssues(tgt, tracker.Query{Label: "x"}, 5); err == nil || !strings.Contains(err.Error(), `"datacenter"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestListLimitMustBePositive(t *testing.T) {
	if _, err := listIssues(cloud, tracker.Query{}, 0); err == nil {
		t.Fatal("limit 0 must error")
	}
}

func TestJQLSelectors(t *testing.T) {
	const tail = ` AND statusCategory != Done order by created asc`
	// escape would break out of project = "ENG" if the value were spliced raw.
	const escape = `orders" OR project = "OTHER`
	for _, c := range []struct {
		q    tracker.Query
		want string
	}{
		{tracker.Query{Label: "ready"}, `project = "ENG" AND (labels = "ready")` + tail},
		{tracker.Query{Status: "To Do"}, `project = "ENG" AND (status = "To Do")` + tail},
		{tracker.Query{Label: "ready", Status: "To Do"}, `project = "ENG" AND (status = "To Do" OR labels = "ready")` + tail},
		{tracker.Query{}, `project = "ENG"` + tail},
		{tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "label", Value: "team-a"}}},
			`project = "ENG" AND (labels = "ready") AND (labels = "team-a")` + tail},
		{tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "component", Value: "Orders"}}},
			`project = "ENG" AND (labels = "ready") AND (component = "Orders")` + tail},
		{tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "component", Value: "Orders"}, {Kind: "label", Value: "team-a"}}},
			`project = "ENG" AND (labels = "ready") AND (component = "Orders") AND (labels = "team-a")` + tail},
		{tracker.Query{Route: []tracker.Selector{{Kind: "component", Value: "Orders"}}},
			`project = "ENG" AND (component = "Orders")` + tail},
		{tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "component", Value: escape}}},
			`project = "ENG" AND (labels = "ready") AND (component = "orders\" OR project = \"OTHER")` + tail},
		{tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "label", Value: escape}}},
			`project = "ENG" AND (labels = "ready") AND (labels = "orders\" OR project = \"OTHER")` + tail},
	} {
		got, err := jql("ENG", c.q)
		if err != nil || got != c.want {
			t.Errorf("jql(%+v) = %s, %v, want %s", c.q, got, err, c.want)
		}
	}
}

// mustOpenJQL is the count query for q against project ENG.
func mustOpenJQL(t *testing.T, q tracker.Query) string {
	t.Helper()
	s, err := openJQL("ENG", q)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var badRoute = []tracker.Selector{{Kind: "team", Value: "x"}}

func TestUnknownRouteKindErrorsWithoutRequest(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, "{}" })
	q := tracker.Query{Label: "ready", Route: badRoute}
	if _, err := listIssues(cloud, q, 5); err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Errorf("listIssues err = %v", err)
	}
	if _, err := count(cloud, q, 0, false); err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Errorf("count err = %v", err)
	}
	if _, _, err := labeledPage(cloud, "x", badRoute, "open", "", 5); err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Errorf("labeledPage err = %v", err)
	}
	if _, err := fileIssue(cloud, tracker.IssueRequest{Title: "t", IssueType: "Bug", Route: badRoute}); err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Errorf("fileIssue err = %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("requests = %v, want none", *got)
	}
}

func TestListIssuesSendsRouteClauses(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"isLast":true,"issues":[]}` })
	q := tracker.Query{Label: "ready", Route: []tracker.Selector{{Kind: "component", Value: `a" OR project = "B`}}}
	if _, err := listIssues(cloud, q, 5); err != nil {
		t.Fatal(err)
	}
	const want = `project = "ENG" AND (labels = "ready") AND (component = "a\" OR project = \"B") AND statusCategory != Done order by created asc`
	if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
		t.Fatalf("requests = %v, want one with jql %s", *got, want)
	}
}

func mustQuery(t *testing.T, r connector.Request) url.Values {
	t.Helper()
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestReadWithEmptyBodyErrors(t *testing.T) {
	serve(t, "/jira/rest/api/2/issue/ENG-1", "")
	_, err := issue(cloud, "ENG-1")
	var ce *connector.Error
	if err == nil || !strings.Contains(err.Error(), "decode") || !errors.As(err, &ce) || ce.Class != connector.Permanent {
		t.Fatalf("err = %v, want a Permanent decode error", err)
	}
}

func TestLabeledClosedCarriesCloseFields(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Path != "/jira/rest/api/2/search/jql" {
			return 404, "{}"
		}
		return 200, `{"isLast":true,"issues":[
			{"key":"ENG-9","fields":{"summary":"np","description":"nb","status":{"statusCategory":{"key":"done"}},"labels":["agent-review-finding"],"resolution":{"name":"Won't Do"},"resolutiondate":"2026-01-02T03:04:05.000+0000"}},
			{"key":"ENG-7","fields":{"summary":"done","status":{"statusCategory":{"key":"done"}},"labels":[],"resolution":{"name":"Done"},"resolutiondate":"2026-01-01T00:00:00.000+0000"}}]}`
	})
	issues, _, err := labeledPage(cloud, "agent-review-finding", nil, "closed", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != "ENG-9" || issues[0].State != "closed" || !issues[0].ClosedNotPlanned ||
		issues[0].ClosedAt != "2026-01-02T03:04:05Z" || issues[1].ClosedNotPlanned {
		t.Fatalf("issues = %#v", issues)
	}
	const want = `project = "ENG" AND labels = "agent-review-finding" AND statusCategory = Done order by created desc`
	if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
		t.Fatalf("requests = %v, want one with jql %s", *got, want)
	}
}

func TestLabeledOpenQueriesNotDone(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"total":1,"issues":[` + issueJSON("ENG-5") + `]}`
	})
	issues, _, err := labeledPage(server, "x", nil, "open", "", 5)
	if err != nil || len(issues) != 1 || issues[0].State != "open" || issues[0].ClosedAt != "" || issues[0].ClosedNotPlanned {
		t.Fatalf("issues = %#v, err = %v", issues, err)
	}
	const want = `project = "ENG" AND labels = "x" AND statusCategory != Done order by created desc`
	if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
		t.Fatalf("requests = %v, want one with jql %s", *got, want)
	}
}

func TestLabeledBoundsPageRequestByLimit(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"isLast":true,"issues":[` + issueJSON("ENG-1") + `]}`
	})
	if _, _, err := labeledPage(cloud, "x", nil, "open", "", 1); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("maxResults") != "1" {
		t.Fatalf("requests = %v, want one with maxResults=1", *got)
	}
}

func TestLabeledCloudChainsByNextPageToken(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Query().Get("nextPageToken") == "tok1" {
			return 200, `{"isLast":true,"issues":[` + issueJSON("ENG-2") + `]}`
		}
		return 200, `{"isLast":false,"nextPageToken":"tok1","issues":[` + issueJSON("ENG-1") + `,` + issueJSON("ENG-3") + `]}`
	})
	first, next, err := labeledPage(cloud, "x", nil, "open", "", 2)
	if err != nil || next != "tok1" || len(first) != 2 {
		t.Fatalf("first = %v, next = %q, err = %v; want the HTTP page's two rows and tok1", first, next, err)
	}
	last, next, err := labeledPage(cloud, "x", nil, "open", next, 2)
	if err != nil || next != "" || len(last) != 1 || last[0].Number != "ENG-2" {
		t.Fatalf("last = %v, next = %q, err = %v", last, next, err)
	}
	if mustQuery(t, (*got)[0]).Has("nextPageToken") || mustQuery(t, (*got)[1]).Get("nextPageToken") != "tok1" {
		t.Fatalf("requests = %v, want the cursor sent as nextPageToken on the second only", *got)
	}
}

func TestLabeledCloudIsLastEndsDespiteToken(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"isLast":true,"nextPageToken":"stale","issues":[` + issueJSON("ENG-1") + `]}`
	})
	if _, next, err := labeledPage(cloud, "x", nil, "open", "", 5); err != nil || next != "" {
		t.Fatalf("next = %q, err = %v, want end", next, err)
	}
}

func TestLabeledServerAdvancesStartAtByRowsReceived(t *testing.T) {
	got := script(t, func(_ connector.Request, u *url.URL) (int, string) {
		if u.Query().Get("startAt") == "2" {
			return 200, `{"total":3,"issues":[` + issueJSON("ENG-3") + `]}`
		}
		return 200, `{"total":3,"issues":[` + issueJSON("ENG-1") + `,` + issueJSON("ENG-2") + `]}`
	})
	_, next, err := labeledPage(server, "x", nil, "open", "", 5)
	if err != nil || next != "2:ENG-1" {
		t.Fatalf("next = %q, err = %v, want 2:ENG-1", next, err)
	}
	issues, next, err := labeledPage(server, "x", nil, "open", next, 5)
	if err != nil || next != "" || len(issues) != 1 {
		t.Fatalf("issues = %v, next = %q, err = %v", issues, next, err)
	}
	if mustQuery(t, (*got)[0]).Get("startAt") != "0" || mustQuery(t, (*got)[1]).Get("startAt") != "2" {
		t.Fatalf("requests = %v, want startAt 0 then 2", *got)
	}
}

func TestLabeledServerEmptyPageBeforeTotalErrors(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"total":4,"issues":[]}` })
	if _, _, err := labeledPage(server, "x", nil, "open", "2:ENG-1", 5); err == nil || !strings.Contains(err.Error(), "list labeled issues: search returned an empty page") {
		t.Fatalf("err = %v, want an empty-page error", err)
	}
}

func TestLabeledServerMalformedCursorErrorsWithoutRequest(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{}` })
	for _, cursor := range []string{"abc", "-1", "1.5", "x:ENG-1", ":ENG-1"} {
		if _, _, err := labeledPage(server, "x", nil, "open", cursor, 5); err == nil || !strings.Contains(err.Error(), "list labeled issues: malformed cursor") {
			t.Errorf("cursor %q: err = %v, want a malformed-cursor error", cursor, err)
		}
	}
	if len(*got) != 0 {
		t.Errorf("sent %d requests", len(*got))
	}
}

func TestLabeledServerRepeatedPageErrors(t *testing.T) {
	// A server that ignores startAt answers the same rows under every cursor.
	var rows []string
	for i := range 100 {
		rows = append(rows, issueJSON(fmt.Sprintf("ENG-%d", i+1)))
	}
	script(t, func(connector.Request, *url.URL) (int, string) {
		return 200, `{"total":600,"issues":[` + strings.Join(rows, ",") + `]}`
	})
	_, next, err := labeledPage(server, "x", nil, "open", "", 100)
	if err != nil || next != "100:ENG-1" {
		t.Fatalf("first: next = %q, err = %v", next, err)
	}
	if _, _, err := labeledPage(server, "x", nil, "open", next, 100); err == nil || !strings.Contains(err.Error(), "repeated the page starting at ENG-1") {
		t.Fatalf("second: err = %v, want a repeated-page error", err)
	}
}

func TestLabeledRefusesBadInputWithoutRequest(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{}` })
	for _, c := range []struct {
		state string
		limit int
	}{{"all", 5}, {"open", 0}} {
		if _, _, err := labeledPage(cloud, "x", nil, c.state, "", c.limit); err == nil {
			t.Errorf("labeledPage(%+v): no error", c)
		}
	}
	if len(*got) != 0 {
		t.Errorf("sent %d requests", len(*got))
	}
}

func TestLabeledUnknownDialectErrors(t *testing.T) {
	script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{}` })
	tgt := tracker.Target{API: site, Repo: "ENG", Dialect: "datacenter"}
	if _, _, err := labeledPage(tgt, "x", nil, "open", "", 5); err == nil || !strings.Contains(err.Error(), `list labeled issues: unknown Jira dialect "datacenter"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLabeledCarriesRouteClauses(t *testing.T) {
	route := []tracker.Selector{{Kind: "component", Value: `a" OR project = "B`}, {Kind: "label", Value: "team-a"}}
	const clauses = ` AND (component = "a\" OR project = \"B") AND (labels = "team-a")`
	for _, c := range []struct{ state, category string }{{"open", "!="}, {"closed", "="}} {
		got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"isLast":true,"issues":[]}` })
		if _, _, err := labeledPage(cloud, "x", route, c.state, "", 5); err != nil {
			t.Fatal(err)
		}
		want := `project = "ENG" AND labels = "x"` + clauses + ` AND statusCategory ` + c.category + ` Done order by created desc`
		if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
			t.Errorf("%s: requests = %v, want one with jql %s", c.state, *got, want)
		}
	}
}

func TestMatchesRouteSendsKeyAndRouteClauses(t *testing.T) {
	hit := `{"isLast":true,"issues":[{"key":"ENG-7","fields":{"labels":["orders"]}}]}`
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"hit":    {hit, true},
		"no hit": {`{"isLast":true,"issues":[]}`, false},
	} {
		got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, tc.body })
		route := []tracker.Selector{{Kind: "label", Value: "orders"}, {Kind: "component", Value: "web"}}
		m, err := matchesRoute(cloud, "ENG-7", route)
		if err != nil || m != tc.want {
			t.Errorf("%s: matchesRoute = %v, %v; want %v", name, m, err, tc.want)
		}
		const want = `project = "ENG" AND key = "ENG-7" AND (labels = "orders") AND (component = "web")`
		if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
			t.Errorf("%s: requests = %v, want one with jql %s", name, *got, want)
		}
	}
}

func TestMatchesRouteQuotesTheKey(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"isLast":true,"issues":[]}` })
	if _, err := matchesRoute(cloud, `ENG-7" OR project = "B`, nil); err != nil {
		t.Fatal(err)
	}
	const want = `project = "ENG" AND key = "ENG-7\" OR project = \"B"`
	if len(*got) != 1 || mustQuery(t, (*got)[0]).Get("jql") != want {
		t.Fatalf("requests = %v, want one with jql %s", *got, want)
	}
}

func TestMatchesRouteRefusesAnUnsupportedKindWithoutARequest(t *testing.T) {
	got := script(t, func(connector.Request, *url.URL) (int, string) { return 200, `{"isLast":true,"issues":[]}` })
	if _, err := matchesRoute(cloud, "ENG-7", []tracker.Selector{{Kind: "project", Value: "x"}}); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v, want none", *got)
	}
}

func TestIssueClassesNon2xx(t *testing.T) {
	for _, c := range statusClasses {
		script(t, func(connector.Request, *url.URL) (int, string) { return c.status, "{}" })
		_, err := issue(cloud, "ENG-7")
		if got := classOf(t, err); got != c.want {
			t.Errorf("status %d: class %q, want %q", c.status, got, c.want)
		}
	}
}

func TestTransportFailureIsTransient(t *testing.T) {
	dialFails(t)
	t.Run("do", func(t *testing.T) {
		_, err := issue(cloud, "ENG-7")
		if got := classOf(t, err); got != connector.Transient {
			t.Errorf("class %q, want %q", got, connector.Transient)
		}
	})
	t.Run("probe", func(t *testing.T) {
		_, err := probe(cloud)
		if got := classOf(t, err); got != connector.Transient {
			t.Errorf("class %q, want %q", got, connector.Transient)
		}
	})
}

// A search whose pages never end fails with a Permanent error naming the
// endpoint, not a truncated list.
func TestSearchCloudFailsPastPageBound(t *testing.T) {
	n := 0
	script(t, func(connector.Request, *url.URL) (int, string) {
		n++
		return 200, fmt.Sprintf(`{"issues":[],"isLast":false,"nextPageToken":"t%d"}`, n)
	})
	_, err := searchCloud(cloud, "project = ENG", 1000000)
	if err == nil {
		t.Fatal("searchCloud succeeded past the page bound")
	}
	if c := classOf(t, err); c != connector.Permanent {
		t.Errorf("class = %v, want Permanent", c)
	}
	if !strings.HasPrefix(err.Error(), "GET /rest/api/2/search/jql: ") {
		t.Errorf("err = %v, want the endpoint as prefix", err)
	}
}
