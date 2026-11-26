package main

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const issuesURL = "https://api.github.com/repos/o/r/issues"

func created(body string) connector.Response {
	return connector.Response{Status: 201, Body: []byte(body)}
}

func TestTrackerTransitionIsAlwaysUnmapped(t *testing.T) {
	got := stub(t, nil)
	res, err := trackerTransition(tgt, "7", tracker.TransitionRequest{
		State:   "complete",
		Mapping: map[string]tracker.StatusTarget{"complete": {Status: "Done"}},
	})
	if err != nil || res.Outcome != tracker.TransitionUnmapped {
		t.Fatalf("res = %+v, err = %v, want unmapped", res, err)
	}
	if len(*got) != 0 {
		t.Errorf("made %d HTTP calls, want none", len(*got))
	}
}

// labelFake answers the label add over REST and the GraphQL documents
// editLabels sends, telling them apart by content. Each label lookup consumes
// the next of lookups (the last repeats). Every GraphQL query must be one of
// the declared documents.
func labelFake(t *testing.T, lookups ...string) *[]connector.Request {
	t.Helper()
	var got []connector.Request
	n := 0
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		got = append(got, r)
		if r.URL == graphqlURL {
			if q := gqlOf(t, r).Query; !slices.Contains(slices.Collect(maps.Values(documents)), q) {
				t.Errorf("query is not a declared document: %q", q)
			}
		}
		switch {
		case r.URL == issuesURL+"/7/labels":
			return ok(`[]`), nil
		case r.URL == graphqlURL && strings.Contains(r.Body, "removeLabelsFromLabelable"):
			return ok(`{"data":{"removeLabelsFromLabelable":{"clientMutationId":null}}}`), nil
		case r.URL == graphqlURL:
			reply := lookups[min(n, len(lookups)-1)]
			n++
			return ok(reply), nil
		}
		return connector.Response{Status: 404, Body: []byte("{}")}, nil
	})
	return &got
}

type gqlBody struct {
	Query     string
	Variables map[string]any
}

func gqlOf(t *testing.T, r connector.Request) gqlBody {
	t.Helper()
	var b gqlBody
	if err := json.Unmarshal([]byte(r.Body), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEditLabelsAddsThenRemoves(t *testing.T) {
	got := labelFake(t,
		`{"data":{"repository":{"issue":{"id":"I_7"},"label":{"id":"L_fix"}}}}`,
		`{"data":{"repository":{"issue":{"id":"I_7"},"label":{"id":"L_ab"}}}}`)
	if err := editLabels(tgt, "7", []string{"x", "y"}, []string{"needs fix", "a/b"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 4 {
		t.Fatalf("calls = %d, want 4: %+v", len(*got), *got)
	}
	add := (*got)[0]
	var body struct{ Labels []string }
	if add.Method != "POST" || json.Unmarshal([]byte(add.Body), &body) != nil || strings.Join(body.Labels, ",") != "x,y" {
		t.Errorf("add call = %+v", add)
	}
	for i, name := range []string{"needs fix", "a/b"} {
		q := (*got)[1+i]
		if q.Method != "POST" || q.URL != graphqlURL {
			t.Errorf("lookup call = %s %s", q.Method, q.URL)
		}
		wantVars := map[string]any{"owner": "o", "repo": "r", "number": float64(7), "name": name}
		if g := gqlOf(t, q); g.Query != labelIDQuery || !reflect.DeepEqual(g.Variables, wantVars) {
			t.Errorf("lookup %d = %+v, want the fixed document with vars %#v", i, g, wantVars)
		}
	}
	m := (*got)[3]
	if m.Method != "POST" || m.URL != graphqlURL {
		t.Errorf("mutation call = %s %s", m.Method, m.URL)
	}
	wantVars := map[string]any{"id": "I_7", "ids": []any{"L_fix", "L_ab"}}
	if g := gqlOf(t, m); g.Query != removeLabelsMutation || !reflect.DeepEqual(g.Variables, wantVars) {
		t.Errorf("mutation = %+v", g)
	}
}

func TestEditLabelsRemovesOneLabel(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"label":{"id":"L_x"}}}}`)
	if err := editLabels(tgt, "7", nil, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("calls = %d, want lookup and mutation: %+v", len(*got), *got)
	}
	if g := gqlOf(t, (*got)[0]); g.Query != labelIDQuery || g.Variables["name"] != "x" {
		t.Errorf("lookup = %+v", g)
	}
	if g := gqlOf(t, (*got)[1]); g.Query != removeLabelsMutation || !reflect.DeepEqual(g.Variables["ids"], []any{"L_x"}) {
		t.Errorf("mutation = %+v", g)
	}
}

func TestEditLabelsRemoveOnlySkipsAdd(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"label":{"id":"L_x"}}}}`)
	if err := editLabels(tgt, "7", []string{""}, []string{"", "x"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 || (*got)[0].URL != graphqlURL || (*got)[1].URL != graphqlURL {
		t.Fatalf("calls = %+v, want the two GraphQL calls only", *got)
	}
	if v := gqlOf(t, (*got)[0]).Variables; v["name"] != "x" {
		t.Errorf("variables = %#v, want name=x", v)
	}
}

func TestEditLabelsEmptyEditMakesNoCalls(t *testing.T) {
	got := stub(t, nil)
	if err := editLabels(tgt, "7", nil, []string{""}); err != nil || len(*got) != 0 {
		t.Errorf("empty edit: err = %v, calls = %d", err, len(*got))
	}
}

func TestEditLabelsAbsentLabelIsSkipped(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"label":null}}}`,
		`{"data":{"repository":{"issue":{"id":"I_7"},"label":{"id":"L_y"}}}}`)
	if err := editLabels(tgt, "7", nil, []string{"gone", "y"}); err != nil {
		t.Fatal(err)
	}
	ids := gqlOf(t, (*got)[len(*got)-1]).Variables["ids"]
	if want := []any{"L_y"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("mutation ids = %#v, want %#v", ids, want)
	}
}

func TestEditLabelsAllAbsentSendsNoMutation(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"label":null}}}`)
	if err := editLabels(tgt, "7", nil, []string{"gone"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Errorf("calls = %d, want only the lookup", len(*got))
	}
}

func TestEditLabelsMissingIssueOrRepoFails(t *testing.T) {
	for name, lookup := range map[string]string{
		"null issue": `{"data":{"repository":{"issue":null,"label":{"id":"L"}}}}`,
		"null repo":  `{"data":{"repository":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := labelFake(t, lookup)
			err := editLabels(tgt, "7", nil, []string{"x"})
			var ce *connector.Error
			if !errors.As(err, &ce) || ce.Class != connector.NotFound {
				t.Errorf("err = %v, want a not-found error", err)
			}
			if len(*got) != 1 {
				t.Errorf("calls = %d, want only the lookup", len(*got))
			}
		})
	}
}

func TestEditLabelsFailsOnErrors(t *testing.T) {
	labelFake(t, `{"errors":[{"message":"boom"}]}`)
	if err := editLabels(tgt, "7", nil, []string{"x"}); err == nil {
		t.Error("graphql errors on lookup: want error")
	}
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		if r.URL == issuesURL+"/7/labels" {
			return connector.Response{Status: 500}, nil
		}
		return ok(`{}`), nil
	})
	if err := editLabels(tgt, "7", []string{"a"}, nil); err == nil {
		t.Error("add 500: want error")
	}
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		if strings.Contains(r.Body, "removeLabelsFromLabelable") {
			return ok(`{"errors":[{"message":"denied"}]}`), nil
		}
		return ok(`{"data":{"repository":{"issue":{"id":"I"},"label":{"id":"L"}}}}`), nil
	})
	if err := editLabels(tgt, "7", nil, []string{"x"}); err == nil {
		t.Error("graphql errors on mutation: want error")
	}
}

func TestEditLabelsRefusesBadNumber(t *testing.T) {
	got := stub(t, nil)
	if err := editLabels(tgt, "7/../8", []string{"a"}, nil); err == nil || len(*got) != 0 {
		t.Errorf("err = %v, calls = %d", err, len(*got))
	}
}

func TestRemoveLabelsRefusesNonCanonicalNumber(t *testing.T) {
	for _, number := range []string{"-1", "0", "01", "x"} {
		got := stub(t, nil)
		if err := removeLabels(tgt, number, []string{"x"}); err == nil || len(*got) != 0 {
			t.Errorf("number %q: err = %v, calls = %d", number, err, len(*got))
		}
	}
}

func TestIssueCommentPostsBody(t *testing.T) {
	got := stub(t, map[string]connector.Response{issuesURL + "/7/comments": created(`{}`)})
	if err := issueComment(tgt, "7", tracker.CommentRequest{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.Body != `{"body":"hello"}` {
		t.Errorf("request = %s %s", r.Method, r.Body)
	}
}

func TestIssueCommentRestoresResearchMarker(t *testing.T) {
	got := stub(t, map[string]connector.Response{issuesURL + "/7/comments": created(`{}`)})
	if err := issueComment(tgt, "7", tracker.CommentRequest{Body: "verdict", Research: true}); err != nil {
		t.Fatal(err)
	}
	var body struct{ Body string }
	if err := json.Unmarshal([]byte((*got)[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if want := "<!-- spindrift-research -->\nverdict"; body.Body != want {
		t.Errorf("body = %q, want %q", body.Body, want)
	}
}

func TestFileIssueWritesDedupMarkerBack(t *testing.T) {
	got := stub(t, map[string]connector.Response{issuesURL: created(`{"html_url":"https://github.com/o/r/issues/9"}`)})
	url, err := fileIssue(tgt, tracker.IssueRequest{
		Title: "t", Body: "desc", Labels: []string{"a", "b"},
		DedupMarker: "<!-- spindrift-dedup: k1 -->", IssueType: "Bug",
	})
	if err != nil || url != "https://github.com/o/r/issues/9" {
		t.Fatalf("url = %q, err = %v", url, err)
	}
	var body struct {
		Title, Body string
		Labels      []string
	}
	if err := json.Unmarshal([]byte((*got)[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Title != "t" || strings.Join(body.Labels, ",") != "a,b" {
		t.Errorf("body = %+v", body)
	}
	if want := "desc\n\n<!-- spindrift-dedup: k1 -->"; body.Body != want {
		t.Errorf("body text = %q, want %q", body.Body, want)
	}
}

func TestFileIssueWithoutMarkerKeepsBodyAndEmptyLabels(t *testing.T) {
	got := stub(t, map[string]connector.Response{issuesURL: created(`{"html_url":"u"}`)})
	if _, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", Body: "desc"}); err != nil {
		t.Fatal(err)
	}
	if want := `{"title":"t","body":"desc","labels":[]}`; (*got)[0].Body != want {
		t.Errorf("body = %s, want %s", (*got)[0].Body, want)
	}
}

func TestFileIssueNeedsURL(t *testing.T) {
	stub(t, map[string]connector.Response{issuesURL: created(`{}`)})
	if _, err := fileIssue(tgt, tracker.IssueRequest{Title: "t"}); err == nil {
		t.Error("no html_url: want error")
	}
}

func TestListLabelsPages(t *testing.T) {
	page1 := make([]string, pageSize)
	for i := range page1 {
		page1[i] = `{"name":"l` + string(rune('a'+i%26)) + `"}`
	}
	base := "https://api.github.com/repos/o/r/labels?per_page=100&page="
	got := stub(t, map[string]connector.Response{
		base + "1": ok("[" + strings.Join(page1, ",") + "]"),
		base + "2": ok(`[{"name":"last"}]`),
	})
	names, err := listLabels(tgt)
	if err != nil || len(names) != pageSize+1 || names[pageSize] != "last" || len(*got) != 2 {
		t.Fatalf("names = %d, err = %v, calls = %d", len(names), err, len(*got))
	}
}

func TestListLabelsEmptyIsNonNil(t *testing.T) {
	stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r/labels?per_page=100&page=1": ok(`[]`)})
	names, err := listLabels(tgt)
	if err != nil || names == nil || len(names) != 0 {
		t.Errorf("names = %#v, err = %v", names, err)
	}
}

func TestListLabelsFailsPastMaxPages(t *testing.T) {
	full := make([]string, pageSize)
	for i := range full {
		full[i] = `{"name":"l"}`
	}
	var sent int
	host.Serve(t, func(connector.Request) (connector.Response, error) {
		sent++
		return ok("[" + strings.Join(full, ",") + "]"), nil
	})
	_, err := listLabels(tgt)
	if err == nil || !strings.Contains(err.Error(), "/labels") || !strings.Contains(err.Error(), strconv.Itoa(maxPages)) {
		t.Errorf("err = %v, want one naming the endpoint and the %d-page bound", err, maxPages)
	}
	if sent != maxPages {
		t.Errorf("sent %d requests, want exactly %d", sent, maxPages)
	}
}

func TestCreateLabelPostsFields(t *testing.T) {
	got := stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r/labels": created(`{}`)})
	if err := createLabel(tgt, "bug", "a bug", "ff0000"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.Body != `{"color":"ff0000","description":"a bug","name":"bug"}` {
		t.Errorf("request = %s %s", r.Method, r.Body)
	}
}

func TestCreateLabelFailsOnExisting(t *testing.T) {
	stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r/labels": {Status: 422}})
	if err := createLabel(tgt, "bug", "", "ff0000"); err == nil {
		t.Error("422: want error, as gh label create without --force")
	}
}

// closeFake answers the issue-id query with lookup and the close mutation
// with an empty success.
func closeFake(t *testing.T, lookup string) *[]connector.Request {
	t.Helper()
	var got []connector.Request
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		got = append(got, r)
		if r.URL != graphqlURL {
			return connector.Response{Status: 404, Body: []byte("{}")}, nil
		}
		if strings.Contains(r.Body, "closeIssue") {
			return ok(`{"data":{"closeIssue":{"clientMutationId":null}}}`), nil
		}
		return ok(lookup), nil
	})
	return &got
}

func TestCloseIssueClosesAsCompleted(t *testing.T) {
	got := closeFake(t, `{"data":{"repository":{"issue":{"id":"I_7"}}}}`)
	if err := closeIssue(tgt, "7"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(*got), *got)
	}
	q := gqlOf(t, (*got)[0])
	if want := map[string]any{"owner": "o", "repo": "r", "number": float64(7)}; !reflect.DeepEqual(q.Variables, want) {
		t.Errorf("lookup variables = %#v, want %#v", q.Variables, want)
	}
	m := (*got)[1]
	if m.Method != "POST" || m.URL != graphqlURL {
		t.Errorf("mutation call = %s %s", m.Method, m.URL)
	}
	g := gqlOf(t, m)
	if !strings.Contains(g.Query, "closeIssue") || !strings.Contains(g.Query, "stateReason:COMPLETED") {
		t.Errorf("mutation does not close as completed: %s", g.Query)
	}
	if want := map[string]any{"id": "I_7"}; !reflect.DeepEqual(g.Variables, want) {
		t.Errorf("mutation variables = %#v, want %#v", g.Variables, want)
	}
}

func TestCloseIssueMissingIssueIsNotFound(t *testing.T) {
	for _, lookup := range []string{
		`{"data":{"repository":{"issue":null}}}`,
		`{"data":{"repository":null}}`,
	} {
		got := closeFake(t, lookup)
		err := closeIssue(tgt, "7")
		var ce *connector.Error
		if !errors.As(err, &ce) || ce.Class != connector.NotFound {
			t.Errorf("lookup %s: err = %v, want NotFound", lookup, err)
		}
		if len(*got) != 1 {
			t.Errorf("lookup %s: calls = %d, want only the lookup", lookup, len(*got))
		}
	}
}

func TestCloseIssueRejectsBadNumber(t *testing.T) {
	got := stub(t, nil)
	if err := closeIssue(tgt, "x"); err == nil || len(*got) != 0 {
		t.Errorf("err = %v, calls = %d, want an error and no calls", err, len(*got))
	}
}

func TestFileIssueStampsTheRouteLabels(t *testing.T) {
	got := stub(t, map[string]connector.Response{issuesURL: created(`{"html_url":"u"}`)})
	route := []tracker.Selector{{Kind: "label", Value: "orders-api"}, {Kind: "label", Value: "a"}}
	if _, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", Labels: []string{"a"}, Route: route}); err != nil {
		t.Fatal(err)
	}
	if want := `{"title":"t","body":"","labels":["a","orders-api"]}`; (*got)[0].Body != want {
		t.Errorf("body = %s, want %s", (*got)[0].Body, want)
	}
}
