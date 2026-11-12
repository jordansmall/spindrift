package main

import (
	"errors"
	"fmt"
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
		{Number: "7", Title: "a", Body: "", State: "open", Labels: []string{"x", "y"}},
		{Number: "9", Title: "c", Body: "cb", State: "open", Labels: []string{}},
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
