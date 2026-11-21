package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

var ttgt = tracker.Target{API: "https://forge.test", Repo: "o/r"}

func issueList(page int, extra string) string {
	return repoAPI + "/issues?" + extra + "limit=100&page=" + strconv.Itoa(page) + "&state=open&type=issues"
}

func TestIssue(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/issues/7": ok(`{"number":7,"title":"T","body":"B","state":"closed","labels":[{"name":"a"},{"name":"b"}]}`),
	})
	i, err := issue(ttgt, "7")
	want := tracker.Issue{Number: "7", Title: "T", Body: "B", State: "closed", Labels: []string{"a", "b"}}
	if err != nil || !reflect.DeepEqual(i, want) {
		t.Fatalf("issue = %#v, %v", i, err)
	}
	if r := (*got)[0]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

func TestIssueNoLabelsIsEmptyList(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI + "/issues/7": ok(`{"number":7,"state":"open"}`)})
	i, err := issue(ttgt, "7")
	if err != nil || i.Labels == nil || len(i.Labels) != 0 {
		t.Fatalf("issue = %#v, %v", i, err)
	}
}

func TestIssueRefusesBadNumber(t *testing.T) {
	got := stub(t, nil)
	for _, n := range []string{"", "0", "-1", "07", "1/comments", "x"} {
		if _, err := issue(ttgt, n); err == nil {
			t.Errorf("issue(%q) accepted", n)
		}
		if _, err := comments(ttgt, n); err == nil {
			t.Errorf("comments(%q) accepted", n)
		}
		if _, err := nativeDeps(ttgt, n); err == nil {
			t.Errorf("nativeDeps(%q) accepted", n)
		}
		if _, err := blocks(ttgt, n); err == nil {
			t.Errorf("blocks(%q) accepted", n)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestIssueStatusIsClassed(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI + "/issues/7": status(500)})
	if _, err := issue(ttgt, "7"); err == nil {
		t.Fatal("a 500 must be an error")
	}
}

func TestListIssuesWalksPagesSortsAndFiltersLabel(t *testing.T) {
	stub(t, map[string]connector.Response{
		issueList(1, "labels=go&"): ok(`[
			{"number":9,"title":"nine","labels":[{"name":"go"}]},
			{"number":2,"title":"two","labels":[{"name":"go"},{"name":"x"}]}]`),
		// Forgejo returns an unlabelled issue when the label filter is unresolved.
		issueList(2, "labels=go&"): ok(`[{"number":5,"title":"five","labels":[{"name":"other"}]},{"number":4,"title":"four","labels":[{"name":"go"}]}]`),
		issueList(3, "labels=go&"): ok(`[]`),
	})
	got, err := listIssues(ttgt, tracker.Query{Label: "go"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var nums []string
	for _, i := range got {
		nums = append(nums, i.Number)
	}
	if !reflect.DeepEqual(nums, []string{"2", "4", "9"}) {
		t.Fatalf("numbers = %v", nums)
	}
	if got[0].Title != "two" || !reflect.DeepEqual(got[0].Labels, []string{"go", "x"}) {
		t.Fatalf("issue = %#v", got[0])
	}
}

func TestListIssuesNoLabelIsEveryOpenIssue(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		issueList(1, ""): ok(`[{"number":3},{"number":1}]`),
		issueList(2, ""): ok(`[]`),
	})
	// Status is ignored: Forgejo has none.
	issues, err := listIssues(ttgt, tracker.Query{Status: "In Review"}, 10)
	if err != nil || len(issues) != 2 || issues[0].Number != "1" {
		t.Fatalf("issues = %#v, %v", issues, err)
	}
	if len(*got) != 2 {
		t.Fatalf("requests = %d", len(*got))
	}
}

func TestListIssuesLimit(t *testing.T) {
	stub(t, map[string]connector.Response{
		issueList(1, ""): ok(`[{"number":3},{"number":1},{"number":2}]`),
		issueList(2, ""): ok(`[]`),
	})
	issues, err := listIssues(ttgt, tracker.Query{}, 2)
	if err != nil || len(issues) != 2 || issues[1].Number != "2" {
		t.Fatalf("issues = %#v, %v", issues, err)
	}
	if _, err := listIssues(ttgt, tracker.Query{}, 0); err == nil {
		t.Fatal("limit 0 accepted")
	}
}

func TestComments(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/issues/7/comments": ok(`[
			{"user":{"login":"alice"},"created_at":"2026-01-01T00:00:00Z","body":"first"},
			{"user":{"login":"bob"},"created_at":"2026-01-02T00:00:00Z","body":"second"}]`),
	})
	c, err := comments(ttgt, "7")
	want := []tracker.Comment{
		{Author: "alice", CreatedAt: "2026-01-01T00:00:00Z", Body: "first"},
		{Author: "bob", CreatedAt: "2026-01-02T00:00:00Z", Body: "second"},
	}
	if err != nil || !reflect.DeepEqual(c, want) {
		t.Fatalf("comments = %#v, %v", c, err)
	}
	if len(*got) != 1 {
		t.Fatalf("the endpoint is unpaginated: %d requests", len(*got))
	}
}

func TestNativeDepsAndBlocks(t *testing.T) {
	link := func(kind string, page int) string {
		return repoAPI + "/issues/7/" + kind + "?limit=100&page=" + strconv.Itoa(page)
	}
	stub(t, map[string]connector.Response{
		link("dependencies", 1): ok(`[{"number":3},{"number":1}]`),
		link("dependencies", 2): ok(`[{"number":8},{"number":3}]`),
		link("dependencies", 3): ok(`[]`),
		link("blocks", 1):       ok(`[]`),
	})
	deps, err := nativeDeps(ttgt, "7")
	if err != nil || !reflect.DeepEqual(deps, []string{"3", "1", "8"}) {
		t.Fatalf("deps = %v, %v", deps, err)
	}
	b, err := blocks(ttgt, "7")
	if err != nil || b == nil || len(b) != 0 {
		t.Fatalf("blocks = %#v, %v", b, err)
	}
}

var orders = []tracker.Selector{{Kind: "label", Value: "orders-api"}}

// A route label joins the query label in the native labels filter, and an
// issue missing one is dropped even when Forgejo answers it, as it does when
// it cannot resolve a name.
func TestListIssuesRouteIsAndedAndRechecked(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		issueList(1, "labels=go%2Corders-api&"): ok(`[
			{"number":1,"labels":[{"name":"go"},{"name":"orders-api"}]},
			{"number":2,"labels":[{"name":"go"}]},
			{"number":3,"labels":[{"name":"orders-api"}]}]`),
		issueList(2, "labels=go%2Corders-api&"): ok(`[]`),
	})
	issues, err := listIssues(ttgt, tracker.Query{Label: "go", Route: orders}, 10)
	if err != nil || len(issues) != 1 || issues[0].Number != "1" {
		t.Fatalf("issues = %#v, %v; want only issue 1", issues, err)
	}
	var pages int
	for _, r := range *got {
		if strings.Contains(r.URL, "/issues?") {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("issue requests = %d, want 2", pages)
	}
}

func TestListIssuesRouteAloneFiltersByRoute(t *testing.T) {
	stub(t, map[string]connector.Response{
		issueList(1, "labels=orders-api&"): ok(`[{"number":1,"labels":[{"name":"orders-api"}]},{"number":2}]`),
		issueList(2, "labels=orders-api&"): ok(`[]`),
	})
	issues, err := listIssues(ttgt, tracker.Query{Route: orders}, 10)
	if err != nil || len(issues) != 1 || issues[0].Number != "1" {
		t.Fatalf("issues = %#v, %v; want only issue 1", issues, err)
	}
}

func TestListIssuesRefusesAnUnexpressibleRoute(t *testing.T) {
	for name, route := range map[string][]tracker.Selector{
		"non-label kind": {{Kind: "project", Value: "orders"}},
		"comma label":    {{Kind: "label", Value: "a,b"}},
		"empty label":    {{Kind: "label"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := stub(t, nil)
			if _, err := listIssues(ttgt, tracker.Query{Label: "go", Route: route}, 10); err == nil {
				t.Fatal("want an error, not the whole scope")
			}
			if len(*got) != 0 {
				t.Fatalf("requests = %v, want none", *got)
			}
		})
	}
}

func TestListIssuesRefusesACommaInTheQueryLabel(t *testing.T) {
	got := stub(t, nil)
	if _, err := listIssues(ttgt, tracker.Query{Label: "a,b", Route: orders}, 10); err == nil {
		t.Fatal("want an error")
	}
	if len(*got) != 0 {
		t.Fatalf("requests = %v, want none", *got)
	}
}

func TestListIssuesUndefinedRouteLabelAsksNoIssues(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"go"}]`, ok(`[]`), nil)
	issues, err := listIssues(ttgt, tracker.Query{Label: "go", Route: orders}, 10)
	if err != nil || issues == nil || len(issues) != 0 {
		t.Fatalf("issues = %#v, %v; want empty", issues, err)
	}
	for _, r := range *got {
		if strings.Contains(r.URL, "/issues?") {
			t.Fatalf("issue query sent for an undefined route label: %v", r.URL)
		}
	}
}
