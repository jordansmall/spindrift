package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const (
	orgAPI    = "https://forge.test/api/v1/orgs/o"
	labelsURL = repoAPI + "/labels?limit=100&page="
	orgLabels = orgAPI + "/labels?limit=100&page="
)

// labelStub serves the repo label set and the org label response.
func labelStub(t *testing.T, repo string, org connector.Response, extra map[string]connector.Response) *[]connector.Request {
	m := map[string]connector.Response{
		labelsURL + "1": ok(repo), labelsURL + "2": ok(`[]`),
		orgLabels + "1": org, orgLabels + "2": ok(`[]`),
	}
	for k, v := range extra {
		m[k] = v
	}
	return stub(t, m)
}

func lastBody(got *[]connector.Request) string { return (*got)[len(*got)-1].Body }

func TestTransitionIsUnmapped(t *testing.T) {
	r, err := transition(ttgt, "7", tracker.TransitionRequest{})
	if err != nil || r.Outcome != tracker.TransitionUnmapped {
		t.Fatalf("transition = %#v, %v", r, err)
	}
}

func TestEditLabelsReplacesTheSetByName(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/issues/7":        ok(`{"number":7,"labels":[{"name":"agent/ready"},{"name":"keep"}]}`),
		repoAPI + "/issues/7/labels": ok(`[]`),
	})
	if err := editLabels(ttgt, "7", []string{"agent/wip", "keep", ""}, []string{"agent/ready", "absent", ""}); err != nil {
		t.Fatal(err)
	}
	r := (*got)[len(*got)-1]
	if r.Method != "PUT" || r.URL != repoAPI+"/issues/7/labels" || r.Body != `{"labels":["keep","agent/wip"]}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestEditLabelsEmptiedSetIsEmptyList(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/issues/7":        ok(`{"number":7,"labels":[{"name":"a"}]}`),
		repoAPI + "/issues/7/labels": ok(`[]`),
	})
	if err := editLabels(ttgt, "7", nil, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if b := lastBody(got); b != `{"labels":[]}` {
		t.Fatalf("body = %q", b)
	}
}

func TestEditLabelsEmptyEditMakesNoCalls(t *testing.T) {
	got := stub(t, nil)
	if err := editLabels(ttgt, "7", nil, nil); err != nil || len(*got) != 0 {
		t.Errorf("empty edit: err = %v, requests = %d", err, len(*got))
	}
}

func TestEditLabelsRefusesBadNumberAndClassesFailure(t *testing.T) {
	got := stub(t, nil)
	if err := editLabels(ttgt, "1/x", []string{"a"}, nil); err == nil || len(*got) != 0 {
		t.Fatalf("bad number: err = %v, requests = %d", err, len(*got))
	}
	stub(t, map[string]connector.Response{
		repoAPI + "/issues/7":        ok(`{"number":7}`),
		repoAPI + "/issues/7/labels": status(403),
	})
	var ce *connector.Error
	if err := editLabels(ttgt, "7", []string{"a"}, nil); !errors.As(err, &ce) || ce.Class != connector.Auth {
		t.Fatalf("err = %v", err)
	}
}

func TestIssueComment(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/issues/7/comments": ok(`{}`)})
	if err := issueComment(ttgt, "7", tracker.CommentRequest{Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "POST" || r.Body != `{"body":"hi"}` {
		t.Fatalf("request = %#v", r)
	}
	if err := issueComment(ttgt, "7", tracker.CommentRequest{Body: "hi", Research: true}); err != nil {
		t.Fatal(err)
	}
	if b := lastBody(got); b != `{"body":"\u003c!-- spindrift-research --\u003e\nhi"}` {
		t.Fatalf("body = %q", b)
	}
	if err := issueComment(ttgt, "x", tracker.CommentRequest{}); err == nil {
		t.Fatal("bad number accepted")
	}
}

func TestCloseIssue(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/issues/7": ok(`{}`)})
	if err := closeIssue(ttgt, "7"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "PATCH" || r.Body != `{"state":"closed"}` {
		t.Fatalf("request = %#v", r)
	}
	if err := closeIssue(ttgt, "0"); err == nil {
		t.Fatal("bad number accepted")
	}
}

func TestListLabelsIsRepoThenOrgOnlyDeduped(t *testing.T) {
	labelStub(t, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`, ok(`[{"id":9,"name":"b"},{"id":10,"name":"org"}]`), nil)
	got, err := listLabels(ttgt)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "org"}) {
		t.Fatalf("labels = %v, %v", got, err)
	}
}

func TestListLabelsOrgOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		org     connector.Response
		want    []string
		wantErr bool
	}{
		{"user-owned repo", status(404), []string{"a"}, false},
		{"token lacks read:organization", status(403), []string{"a"}, false},
		{"unauthenticated", status(401), []string{"a"}, false},
		{"other failure", status(500), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labelStub(t, `[{"id":1,"name":"a"}]`, tc.org, nil)
			got, err := listLabels(ttgt)
			if (err != nil) != tc.wantErr || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("labels = %v, %v", got, err)
			}
		})
	}
}

func TestCreateLabelAddsHashToColor(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/labels": ok(`{}`)})
	if err := createLabel(ttgt, "n", "d", "ff00aa"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "POST" || r.Body != `{"color":"#ff00aa","description":"d","name":"n"}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestFileIssueSendsLabelIDsAndAnswersURL(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"a"}]`, ok(`[{"id":10,"name":"org"}]`),
		map[string]connector.Response{repoAPI + "/issues": ok(`{"number":3,"html_url":"https://forge.test/o/r/issues/3"}`)})
	u, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T", Body: "B", Labels: []string{"org", "a"}, DedupMarker: "<!-- m -->", IssueType: "Bug"})
	if err != nil || u != "https://forge.test/o/r/issues/3" {
		t.Fatalf("url = %q, %v", u, err)
	}
	r := (*got)[len(*got)-1]
	if r.Method != "POST" || r.URL != repoAPI+"/issues" ||
		r.Body != `{"body":"B\n\n\u003c!-- m --\u003e","labels":[10,1],"title":"T"}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestFileIssueWithoutLabelsSkipsLabelLookup(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/issues": ok(`{"number":3,"html_url":"u"}`)})
	if _, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0].Body, `"labels":[]`) {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestFileIssueUndefinedLabelFilesNothing(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"a"}]`, status(404), nil)
	_, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T", Labels: []string{"a", "nope"}})
	if err == nil || !strings.Contains(err.Error(), "label(s) not defined on o/r or its owner: nope") {
		t.Fatalf("err = %v", err)
	}
	for _, r := range *got {
		if r.Method != "GET" {
			t.Fatalf("filed anyway: %#v", r)
		}
	}
}

func TestFileIssueUnreadableOrgAppliesLabelsByName(t *testing.T) {
	got := labelStub(t, `[{"id":1,"name":"a"}]`, status(403), map[string]connector.Response{
		repoAPI + "/issues":          ok(`{"number":3,"html_url":"https://forge.test/o/r/issues/3"}`),
		repoAPI + "/issues/3/labels": ok(`[]`),
	})
	u, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T", Labels: []string{"a", "org"}})
	if err != nil || u != "https://forge.test/o/r/issues/3" {
		t.Fatalf("url = %q, %v", u, err)
	}
	var create, put connector.Request
	for _, r := range *got {
		switch {
		case r.Method == "POST":
			create = r
		case r.Method == "PUT":
			put = r
		}
	}
	if !strings.Contains(create.Body, `"labels":[1]`) || put.URL != repoAPI+"/issues/3/labels" || put.Body != `{"labels":["a","org"]}` {
		t.Fatalf("create = %#v, put = %#v", create, put)
	}
}

func TestFileIssueStillAnswersURLWhenNameApplyFails(t *testing.T) {
	labelStub(t, `[]`, status(403), map[string]connector.Response{
		repoAPI + "/issues":          ok(`{"number":3,"html_url":"https://forge.test/o/r/issues/3"}`),
		repoAPI + "/issues/3/labels": status(500),
	})
	u, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T", Labels: []string{"org"}})
	if err != nil || u != "https://forge.test/o/r/issues/3" {
		t.Fatalf("url = %q, %v", u, err)
	}
}

func TestFileIssueEmptyURLIsAnError(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI + "/issues": ok(`{"number":3}`)})
	if u, err := fileIssue(ttgt, tracker.IssueRequest{Title: "T"}); err == nil {
		t.Fatalf("url = %q", u)
	}
}
