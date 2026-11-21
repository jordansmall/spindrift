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

var tgt = tracker.Target{API: "https://api.github.com", Repo: "o/r"}

// stub replaces send with canned responses keyed by URL and records requests.
func stub(t *testing.T, resp map[string]connector.Response) *[]connector.Request {
	t.Helper()
	var got []connector.Request
	old := send
	send = func(r connector.Request) (connector.Response, error) {
		got = append(got, r)
		v, ok := resp[r.URL]
		if !ok {
			return connector.Response{Status: 404, Body: []byte("{}")}, nil
		}
		return v, nil
	}
	t.Cleanup(func() { send = old })
	return &got
}

func ok(body string) connector.Response {
	return connector.Response{Status: 200, Body: []byte(body)}
}

// items renders n REST issue objects numbered from first; pr marks them pull requests.
func items(first, n int, pr bool) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"number":%d,"title":"t","body":"b","state":"open","labels":[]`, first+i)
		if pr {
			b.WriteString(`,"pull_request":{}`)
		}
		b.WriteString("}")
	}
	b.WriteString("]")
	return b.String()
}

const listBase = "https://api.github.com/repos/o/r/issues?state=open&sort=created&direction=asc&per_page=100"

func TestListIssues(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		listBase + "&labels=ready+for%2Fagent&page=1": ok(`[
			{"number":7,"title":"a","body":null,"state":"open","labels":[{"name":"x"},{"name":"y"}]},
			{"number":8,"title":"pr","body":"b","state":"open","labels":[],"pull_request":{}},
			{"number":9,"title":"c","body":"cb","state":"open","labels":[]}]`),
	})
	issues, err := listIssues(tgt, tracker.Query{Label: "ready for/agent"}, 30)
	if err != nil {
		t.Fatal(err)
	}
	want := []tracker.Issue{
		{Number: "7", Title: "a", Body: "", State: "", Labels: []string{"x", "y"}},
		{Number: "9", Title: "c", Body: "", State: "", Labels: []string{}},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Fatalf("issues = %#v", issues)
	}
	r := (*got)[0]
	if r.Method != "GET" || r.Headers["Accept"] != "application/vnd.github+json" ||
		r.Headers["X-GitHub-Api-Version"] != "2022-11-28" {
		t.Fatalf("request = %#v", r)
	}
}

func TestListIssuesSkipsPullRequestsAcrossPages(t *testing.T) {
	// Page 1 is 40 PRs then 60 issues; a one-page read would return 60 issues
	// where 100 were asked for.
	p1 := strings.TrimSuffix(items(1, 40, true), "]") + "," + strings.TrimPrefix(items(41, 60, false), "[")
	got := stub(t, map[string]connector.Response{
		listBase + "&page=1": ok(p1),
		listBase + "&page=2": ok(items(101, 100, false)),
	})
	issues, err := listIssues(tgt, tracker.Query{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 100 || issues[0].Number != "41" || issues[99].Number != "140" {
		t.Fatalf("len = %d, first = %s, last = %s", len(issues), issues[0].Number, issues[len(issues)-1].Number)
	}
	if len(*got) != 2 {
		t.Fatalf("requests = %d", len(*got))
	}
}

func TestListIssuesAllPullRequestsFirstPage(t *testing.T) {
	stub(t, map[string]connector.Response{
		listBase + "&page=1": ok(items(1, 100, true)),
		listBase + "&page=2": ok(items(101, 3, false)),
	})
	issues, err := listIssues(tgt, tracker.Query{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != "101" || issues[1].Number != "102" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestListIssuesLimit(t *testing.T) {
	stub(t, map[string]connector.Response{listBase + "&page=1": ok(items(1, 5, false))})
	issues, err := listIssues(tgt, tracker.Query{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 3 || issues[2].Number != "3" {
		t.Fatalf("issues = %#v", issues)
	}
	for _, n := range []int{0, -1} {
		if _, err := listIssues(tgt, tracker.Query{}, n); err == nil {
			t.Fatalf("limit %d: want error", n)
		}
	}
}

func TestPageCeiling(t *testing.T) {
	old := send
	n := 0
	send = func(connector.Request) (connector.Response, error) {
		n++
		return ok(items(1, pageSize, false)), nil
	}
	t.Cleanup(func() { send = old })
	if _, err := nativeDeps(tgt, "5"); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("nativeDeps err = %v", err)
	}
	if n != maxPages {
		t.Fatalf("requests = %d, want %d", n, maxPages)
	}
	if _, err := listIssues(tgt, tracker.Query{}, maxPages*pageSize+1); err == nil {
		t.Fatal("listIssues: want error")
	}
}

func TestIssueRejectsPullRequest(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues/6": ok(`{"number":6,"title":"t","state":"open","labels":[],"pull_request":{}}`),
	})
	if _, err := issue(tgt, "6"); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Fatalf("err = %v", err)
	}
}

func TestIssue(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues/5": ok(`{"number":5,"title":"t","body":"b","state":"closed","labels":[{"name":"l"}]}`),
	})
	i, err := issue(tgt, "5")
	if err != nil {
		t.Fatal(err)
	}
	want := tracker.Issue{Number: "5", Title: "t", Body: "b", State: "closed", Labels: []string{"l"}}
	if !reflect.DeepEqual(i, want) {
		t.Fatalf("issue = %#v", i)
	}
	if len(*got) != 1 || (*got)[0].Method != "GET" {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestNativeDeps(t *testing.T) {
	const u = "https://api.github.com/repos/o/r/issues/5/dependencies/blocked_by?per_page=100&page="
	got := stub(t, map[string]connector.Response{
		u + "1": ok(items(1, 100, false)),
		u + "2": ok(`[{"number":3},{"number":2},{"number":3}]`),
	})
	ids, err := nativeDeps(tgt, "5")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 103 || ids[100] != "3" || ids[101] != "2" || ids[102] != "3" || len(*got) != 2 {
		t.Fatalf("ids = %v, requests = %d", ids, len(*got))
	}
}

func TestErrors(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues/5": {Status: 403, Body: []byte("no")},
	})
	_, err := issue(tgt, "5")
	if err == nil || !strings.Contains(err.Error(), "GET /repos/o/r/issues/5") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if _, err := issue(tgt, "6"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	boom := errors.New("boom")
	send = func(connector.Request) (connector.Response, error) { return connector.Response{}, boom }
	if _, err := listIssues(tgt, tracker.Query{}, 1); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestBadNumberRefusedWithoutRequest(t *testing.T) {
	for _, bad := range []string{"1/../x", "abc", "0", "-1", "007", "5?x=1", ""} {
		got := stub(t, nil)
		if _, err := issue(tgt, bad); err == nil {
			t.Errorf("issue(%q): no error", bad)
		}
		if _, err := nativeDeps(tgt, bad); err == nil {
			t.Errorf("nativeDeps(%q): no error", bad)
		}
		if len(*got) != 0 {
			t.Errorf("number %q: %d requests, want none", bad, len(*got))
		}
	}
}

func TestRemovedLabelsKeepsUnlabeledEventsInOrderAcrossPages(t *testing.T) {
	const u = "https://api.github.com/repos/o/r/issues/5/timeline?per_page=100&page="
	// A full first page of other events, so the walk must continue: the
	// unlabeled event on page 2 follows the one buried in page 1.
	var p1 strings.Builder
	p1.WriteString(`[{"event":"unlabeled","label":{"name":"agent-failed"}}`)
	for range 99 {
		p1.WriteString(`,{"event":"labeled","label":{"name":"agent-complete"}}`)
	}
	p1.WriteString("]")
	got := stub(t, map[string]connector.Response{
		u + "1": ok(p1.String()),
		u + "2": ok(`[{"event":"commented"},{"event":"unlabeled","label":{"name":"agent-complete"}},{"event":"unlabeled","label":null},{"event":"unlabeled","label":{"name":"ready"}}]`),
	})
	names, err := removedLabels(tgt, "5")
	if want := []string{"agent-failed", "agent-complete", "ready"}; err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("removedLabels = %v, %v; want %v", names, err, want)
	}
	if len(*got) != 2 || (*got)[0].Method != "GET" {
		t.Errorf("requests = %d, want two GETs", len(*got))
	}
}

func TestRemovedLabelsNoneIsEmptyNotNil(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues/5/timeline?per_page=100&page=1": ok(`[]`),
	})
	names, err := removedLabels(tgt, "5")
	if err != nil || names == nil || len(names) != 0 {
		t.Fatalf("removedLabels = %#v, %v; want empty, nil", names, err)
	}
}

func TestRemovedLabelsFailsOnNon2xxAndBadNumber(t *testing.T) {
	got := stub(t, nil)
	if _, err := removedLabels(tgt, "5"); err == nil {
		t.Error("404 accepted")
	}
	n := len(*got)
	if _, err := removedLabels(tgt, "../x"); err == nil || len(*got) != n {
		t.Errorf("bad number err = %v, requests %d -> %d; want refusal without a request", err, n, len(*got))
	}
}

func TestBlocks(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues/5/dependencies/blocking?per_page=100&page=1": ok(`[{"number":9},{"number":4}]`),
	})
	ids, err := blocks(tgt, "5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"9", "4"}) || len(*got) != 1 {
		t.Fatalf("ids = %v, requests = %d", ids, len(*got))
	}
	if _, err := blocks(tgt, "1/../x"); err == nil {
		t.Fatal("bad number: want error")
	}
}

// searchQuery returns the decoded q parameter of a /search/issues request.
func searchQuery(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Path != "/search/issues" {
		t.Fatalf("url = %q, err = %v", rawURL, err)
	}
	return u.Query().Get("q")
}

func TestSearch(t *testing.T) {
	var reqs []string
	old := send
	send = func(r connector.Request) (connector.Response, error) {
		reqs = append(reqs, r.URL)
		return ok(`{"items":[
			{"number":3,"title":"a","body":"b","state":"open","labels":[{"name":"l"}]},
			{"number":4,"title":"pr","state":"open","labels":[],"pull_request":{}},
			{"number":5,"title":"c","state":"open","labels":[]}]}`), nil
	}
	t.Cleanup(func() { send = old })
	issues, err := search(tgt, "  foo   bar ", "open", "a b", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []tracker.Issue{
		{Number: "3", Title: "a", State: "open", Labels: []string{"l"}},
		{Number: "5", Title: "c", State: "open", Labels: []string{}},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Fatalf("issues = %#v", issues)
	}
	if len(reqs) != 1 {
		t.Fatalf("requests = %v", reqs)
	}
	const q = `repo:o/r is:issue state:open label:"a b" "foo" "bar" in:title,body`
	if got := searchQuery(t, reqs[0]); got != q {
		t.Fatalf("q = %q, want %q", got, q)
	}
	issues, err = search(tgt, "x", "closed", "", 1)
	if err != nil || len(issues) != 1 {
		t.Fatalf("issues = %v, err = %v", issues, err)
	}
	if got := searchQuery(t, reqs[1]); got != `repo:o/r is:issue state:closed "x" in:title,body` {
		t.Fatalf("q = %q", got)
	}
}

func TestSearchPages(t *testing.T) {
	n := 0
	old := send
	send = func(connector.Request) (connector.Response, error) {
		n++
		return ok(`{"items":` + items(n*100, 100, false) + `}`), nil
	}
	t.Cleanup(func() { send = old })
	issues, err := search(tgt, "x", "open", "", 150)
	if err != nil || len(issues) != 150 || n != 2 {
		t.Fatalf("len = %d, requests = %d, err = %v", len(issues), n, err)
	}
}

func TestSearchRefusesUnsafeInputWithoutRequest(t *testing.T) {
	cases := []struct{ text, state, label string }{
		{`a" repo:x/y`, "open", ""},
		{`a\`, "open", ""},
		{"a\nb", "open", ""},
		{"a", "open", `l" is:pr`},
		{"   ", "open", ""},
		{"a", "all", ""},
	}
	for _, c := range cases {
		got := stub(t, nil)
		if _, err := search(tgt, c.text, c.state, c.label, 5); err == nil {
			t.Errorf("search(%+v): no error", c)
		}
		if len(*got) != 0 {
			t.Errorf("search(%+v): sent %d requests", c, len(*got))
		}
	}
	if _, err := search(tgt, "a", "open", "", 0); err == nil {
		t.Error("limit 0: no error")
	}
}

func gqlComments(hasNext bool, cursor string, nodes ...string) connector.Response {
	return ok(fmt.Sprintf(`{"data":{"repository":{"issue":{"comments":{"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}}`,
		hasNext, cursor, strings.Join(nodes, ",")))
}

// stubPages answers each POST /graphql with the next canned response.
func stubPages(t *testing.T, resp ...connector.Response) *[]connector.Request {
	t.Helper()
	var got []connector.Request
	old := send
	send = func(r connector.Request) (connector.Response, error) {
		got = append(got, r)
		if len(got) > len(resp) {
			return connector.Response{Status: 500, Body: []byte("{}")}, nil
		}
		return resp[len(got)-1], nil
	}
	t.Cleanup(func() { send = old })
	return &got
}

func TestComments_MapsEveryField(t *testing.T) {
	got := stubPages(t, gqlComments(false, "",
		`{"author":{"login":"alice"},"createdAt":"2026-01-02T03:04:05Z","body":"live","isMinimized":false,"minimizedReason":null,"authorAssociation":"MEMBER"}`,
		`{"author":{"login":"bob"},"createdAt":"2026-01-03T04:05:06Z","body":"noise\n<!-- spindrift-research -->","isMinimized":true,"minimizedReason":"SPAM","authorAssociation":"NONE"}`,
		`{"author":null,"createdAt":"2026-01-04T05:06:07Z","body":"orphan","isMinimized":false,"minimizedReason":null,"authorAssociation":"CONTRIBUTOR"}`))
	comments, err := commentsOf(tgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	want := []tracker.Comment{
		{Author: "alice", CreatedAt: "2026-01-02T03:04:05Z", Body: "live", Association: "MEMBER"},
		{Author: "bob", CreatedAt: "2026-01-03T04:05:06Z", Body: "noise\n<!-- spindrift-research -->", Minimized: true, MinimizedReason: "SPAM", Association: "NONE"},
		{Author: "", CreatedAt: "2026-01-04T05:06:07Z", Body: "orphan", Association: "CONTRIBUTOR"},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Fatalf("comments = %#v", comments)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.URL != "https://api.github.com/graphql" ||
		!strings.Contains(r.Body, `"owner":"o"`) || !strings.Contains(r.Body, `"repo":"r"`) || !strings.Contains(r.Body, `"number":7`) {
		t.Fatalf("request = %#v", r)
	}
}

func TestComments_PagesWithTheCursor(t *testing.T) {
	node := func(body string) string {
		return `{"author":{"login":"a"},"createdAt":"2026-01-01T00:00:00Z","body":"` + body + `","isMinimized":false,"minimizedReason":null,"authorAssociation":"OWNER"}`
	}
	got := stubPages(t, gqlComments(true, "CUR1", node("one"), node("two")), gqlComments(false, "", node("three")))
	comments, err := commentsOf(tgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range comments {
		bodies = append(bodies, c.Body)
	}
	if !reflect.DeepEqual(bodies, []string{"one", "two", "three"}) {
		t.Fatalf("bodies = %q", bodies)
	}
	if len(*got) != 2 || strings.Contains((*got)[0].Body, "CUR1") || !strings.Contains((*got)[1].Body, `"after":"CUR1"`) {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestComments_EmptyThreadIsNotNil(t *testing.T) {
	stubPages(t, gqlComments(false, ""))
	comments, err := commentsOf(tgt, "7")
	if err != nil || comments == nil || len(comments) != 0 {
		t.Fatalf("comments = %#v, err = %v", comments, err)
	}
}

func TestComments_Failures(t *testing.T) {
	for name, resp := range map[string]connector.Response{
		"no such issue":  ok(`{"data":{"repository":{"issue":null}}}`),
		"no repository":  ok(`{"data":{"repository":null}}`),
		"graphql errors": ok(`{"data":null,"errors":[{"message":"rate limited"}]}`),
		"http failure":   {Status: 502, Body: []byte("{}")},
	} {
		t.Run(name, func(t *testing.T) {
			stubPages(t, resp)
			if _, err := commentsOf(tgt, "7"); err == nil {
				t.Fatal("want an error: a failed read must not look like an empty thread")
			}
		})
	}
}

func TestComments_RefusesAnUnboundedThread(t *testing.T) {
	more := make([]connector.Response, maxPages+1)
	for i := range more {
		more[i] = gqlComments(true, "C")
	}
	stubPages(t, more...)
	if _, err := commentsOf(tgt, "7"); err == nil {
		t.Fatal("want an error past maxPages, not a truncated thread")
	}
}

func TestComments_RefusesABadNumber(t *testing.T) {
	got := stubPages(t)
	for _, n := range []string{"", "0", "-1", "07", "1/2", "x"} {
		if _, err := commentsOf(tgt, n); err == nil {
			t.Errorf("number %q accepted", n)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("a bad number reached the network: %#v", *got)
	}
}

func TestComments_RefusesABadRepo(t *testing.T) {
	stubPages(t)
	if _, err := commentsOf(tracker.Target{API: tgt.API, Repo: "norepo"}, "7"); err == nil {
		t.Fatal("want an error for a repo that is not owner/name")
	}
}

func TestLabeled(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues?state=closed&labels=a+b&sort=created&direction=desc&per_page=100&page=1": ok(`[
			{"number":9,"title":"np","body":"nb","state":"closed","state_reason":"not_planned","closed_at":"2026-01-02T03:04:05Z","labels":[{"name":"a b"}]},
			{"number":8,"title":"pr","state":"closed","labels":[],"pull_request":{}},
			{"number":7,"title":"done","body":null,"state":"closed","state_reason":"completed","closed_at":"2026-01-01T00:00:00Z","labels":[]},
			{"number":6,"title":"odd","state":"closed","closed_at":null,"labels":[]}]`),
	})
	issues, next, err := labeledPage(tgt, "a b", nil, "closed", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []tracker.Issue{
		{Number: "9", Title: "np", Body: "nb", State: "closed", Labels: []string{"a b"}, ClosedAt: "2026-01-02T03:04:05Z", ClosedNotPlanned: true},
		{Number: "7", Title: "done", State: "closed", Labels: []string{}, ClosedAt: "2026-01-01T00:00:00Z"},
		{Number: "6", Title: "odd", State: "closed", Labels: []string{}},
	}
	if !reflect.DeepEqual(issues, want) {
		t.Fatalf("issues = %#v", issues)
	}
	if len(*got) != 1 || next != "" {
		t.Fatalf("requests = %d, next = %q, want 1 and no next", len(*got), next)
	}
}

func TestLabeledOpenCarriesNoCloseFields(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues?state=open&labels=x&sort=created&direction=desc&per_page=100&page=1": ok(`[
			{"number":5,"title":"t","state":"open","state_reason":"reopened","closed_at":"2026-01-01T00:00:00Z","labels":[]}]`),
	})
	issues, _, err := labeledPage(tgt, "x", nil, "open", "", 100)
	want := []tracker.Issue{{Number: "5", Title: "t", State: "open", Labels: []string{}}}
	if err != nil || !reflect.DeepEqual(issues, want) {
		t.Fatalf("issues = %#v, err = %v", issues, err)
	}
}

func TestLabeledPageEmptyLabelUnroutedStillSendsAnEmptyLabelsParam(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues?state=open&labels=&sort=created&direction=desc&per_page=100&page=1": ok(`[]`),
	})
	if _, _, err := labeledPage(tgt, "", nil, "open", "", 100); err != nil || len(*got) != 1 {
		t.Fatalf("requests = %d, err = %v; want the one unchanged request", len(*got), err)
	}
}

func TestLabeledPageCursorIsTheRESTPageNumber(t *testing.T) {
	base := "https://api.github.com/repos/o/r/issues?state=open&labels=x&sort=created&direction=desc&per_page=100&page="
	got := stub(t, map[string]connector.Response{
		base + "1": ok(items(1000, 100, false)),
		base + "2": ok(items(2000, 3, false)),
	})
	issues, next, err := labeledPage(tgt, "x", nil, "open", "", 500)
	if err != nil || len(issues) != 100 || next != "2" {
		t.Fatalf("page 1: len = %d, next = %q, err = %v", len(issues), next, err)
	}
	issues, next, err = labeledPage(tgt, "x", nil, "open", next, 500)
	if err != nil || len(issues) != 3 || next != "" {
		t.Fatalf("page 2: len = %d, next = %q, err = %v", len(issues), next, err)
	}
	if len(*got) != 2 || (*got)[1].URL != base+"2" {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestLabeledPageBoundsPerPageByLimit(t *testing.T) {
	base := "https://api.github.com/repos/o/r/issues?state=open&labels=x&sort=created&direction=desc&per_page=10&page="
	got := stub(t, map[string]connector.Response{
		base + "1": ok(items(1, 10, false)),
		base + "2": ok(items(100, 4, false)),
	})
	issues, next, err := labeledPage(tgt, "x", nil, "open", "", 10)
	if err != nil || len(issues) != 10 || next != "2" {
		t.Fatalf("page 1: len = %d, next = %q, err = %v", len(issues), next, err)
	}
	issues, next, err = labeledPage(tgt, "x", nil, "open", next, 10)
	if err != nil || len(issues) != 4 || next != "" || len(*got) != 2 {
		t.Fatalf("page 2: len = %d, next = %q, err = %v, requests = %d", len(issues), next, err, len(*got))
	}
}

func TestLabeledPageKeepsGoingPastASkippedPRPage(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/issues?state=open&labels=x&sort=created&direction=desc&per_page=100&page=1": ok(items(1, 100, true)),
	})
	issues, next, err := labeledPage(tgt, "x", nil, "open", "", 500)
	if err != nil || len(issues) != 0 || next != "2" {
		t.Fatalf("len = %d, next = %q, err = %v", len(issues), next, err)
	}
}

func TestLabeledPageRefusesBadCursorWithoutRequest(t *testing.T) {
	got := stub(t, nil)
	for _, c := range []string{"x", "0", "-1", "1.5", " 2"} {
		if _, _, err := labeledPage(tgt, "x", nil, "open", c, 5); err == nil || !strings.Contains(err.Error(), "list labeled issues: malformed cursor") {
			t.Errorf("cursor %q: err = %v, want a malformed-cursor error", c, err)
		}
	}
	if len(*got) != 0 {
		t.Errorf("sent %d requests", len(*got))
	}
}

func TestLabeledRefusesBadInputWithoutRequest(t *testing.T) {
	got := stub(t, nil)
	for _, c := range []struct {
		state string
		limit int
	}{{"all", 5}, {"open", 0}} {
		if _, _, err := labeledPage(tgt, "x", nil, c.state, "", c.limit); err == nil {
			t.Errorf("labeledPage(%+v): no error", c)
		}
	}
	if len(*got) != 0 {
		t.Errorf("sent %d requests", len(*got))
	}
}

var routeOrders = []tracker.Selector{{Kind: "label", Value: "orders-api"}}

// A route's labels join the native labels filter, ANDed with the query label,
// on every list path; no route leaves the URL as it was.
func TestRouteLabelsAreAndedIntoEveryListRequest(t *testing.T) {
	resetCountCache(t)
	q := tracker.Query{Label: "ready", Route: routeOrders}
	got := stub(t, map[string]connector.Response{
		listBase + "&labels=ready,orders-api&page=1":  ok(items(1, 1, false)),
		countBase + "&labels=ready,orders-api&page=1": ok(items(1, 2, false)),
		"https://api.github.com/repos/o/r/issues?state=open&labels=x+y,orders-api&sort=created&direction=desc&per_page=100&page=1": ok(items(1, 1, false)),
	})
	if l, err := listIssues(tgt, q, 10); err != nil || len(l) != 1 {
		t.Fatalf("listIssues = %v, %v; want 1 routed issue", l, err)
	}
	if n, err := count(tgt, q, 10, false); err != nil || n != 2 {
		t.Fatalf("count = %d, %v; want 2", n, err)
	}
	if l, _, err := labeledPage(tgt, "x y", routeOrders, "open", "", 100); err != nil || len(l) != 1 {
		t.Fatalf("labeledPage = %v, %v; want 1 routed issue", l, err)
	}
	if len(*got) != 3 {
		t.Fatalf("requests = %d, want 3", len(*got))
	}

	// With no query label the route alone is the filter.
	got = stub(t, map[string]connector.Response{listBase + "&labels=orders-api&page=1": ok(items(1, 1, false))})
	if _, err := listIssues(tgt, tracker.Query{Route: routeOrders}, 10); err != nil || len(*got) != 1 {
		t.Fatalf("route-only listIssues: %d requests, %v", len(*got), err)
	}
}

// A routed count and an unrouted one are different requests, so they never
// share a cached validator.
func TestRoutedCountKeepsItsOwnCacheEntry(t *testing.T) {
	resetCountCache(t)
	stubETag(t, `"e"`)
	stub(t, map[string]connector.Response{
		countBase + "&labels=ready&page=1":            ok(items(1, 3, false)),
		countBase + "&labels=ready,orders-api&page=1": ok(items(1, 1, false)),
	})
	plain, routed := tracker.Query{Label: "ready"}, tracker.Query{Label: "ready", Route: routeOrders}
	for _, c := range []struct {
		q    tracker.Query
		want int
	}{{plain, 3}, {routed, 1}} {
		if n, err := count(tgt, c.q, 10, false); err != nil || n != c.want {
			t.Fatalf("count(%+v) = %d, %v; want %d", c.q, n, err, c.want)
		}
	}
	if len(countCache) != 2 {
		t.Errorf("countCache holds %d entries, want one per request", len(countCache))
	}
}

// A selector the Connector cannot express as a native filter fails the read
// before any request: ignoring it would list the whole scope.
func TestRouteThatGitHubCannotFilterByFailsClosed(t *testing.T) {
	for name, route := range map[string][]tracker.Selector{
		"non-label kind": {{Kind: "project", Value: "orders"}},
		"comma in value": {{Kind: "label", Value: "a,b"}},
		"empty value":    {{Kind: "label", Value: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			resetCountCache(t)
			got := stub(t, nil)
			q := tracker.Query{Label: "ready", Route: route}
			if _, err := listIssues(tgt, q, 10); err == nil {
				t.Error("listIssues: no error")
			}
			if _, err := count(tgt, q, 10, false); err == nil {
				t.Error("count: no error")
			}
			if _, _, err := labeledPage(tgt, "ready", route, "open", "", 100); err == nil {
				t.Error("labeledPage: no error")
			}
			if len(*got) != 0 {
				t.Errorf("sent %d requests, want none", len(*got))
			}
		})
	}
}

func TestRouteRefusesACommaInTheQueryLabel(t *testing.T) {
	if _, err := labelsFilter("a,b", routeOrders); err == nil {
		t.Fatal("want an error")
	}
	if got, err := labelsFilter("a,b", nil); err != nil || got != "&labels=a%2Cb" {
		t.Fatalf("unrouted = %q, %v; want the label passed as before", got, err)
	}
}
