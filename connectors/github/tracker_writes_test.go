package main

import (
	"encoding/json"
	"errors"
	"reflect"
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

// labelFake answers the label add over REST and the two GraphQL documents
// editLabels sends, telling them apart by content; lookup is the reply to the
// id query.
func labelFake(t *testing.T, lookup string) *[]connector.Request {
	t.Helper()
	var got []connector.Request
	old := send
	t.Cleanup(func() { send = old })
	send = func(r connector.Request) (connector.Response, error) {
		got = append(got, r)
		switch {
		case r.URL == issuesURL+"/7/labels":
			return ok(`[]`), nil
		case r.URL == graphqlURL && strings.Contains(r.Body, "removeLabelsFromLabelable"):
			return ok(`{"data":{"removeLabelsFromLabelable":{"clientMutationId":null}}}`), nil
		case r.URL == graphqlURL:
			return ok(lookup), nil
		}
		return connector.Response{Status: 404, Body: []byte("{}")}, nil
	}
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
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"l0":{"id":"L_fix"},"l1":{"id":"L_ab"}}}}`)
	if err := editLabels(tgt, "7", []string{"x", "y"}, []string{"needs fix", "a/b"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 3 {
		t.Fatalf("calls = %d, want 3: %+v", len(*got), *got)
	}
	add := (*got)[0]
	var body struct{ Labels []string }
	if add.Method != "POST" || json.Unmarshal([]byte(add.Body), &body) != nil || strings.Join(body.Labels, ",") != "x,y" {
		t.Errorf("add call = %+v", add)
	}
	q := (*got)[1]
	if q.Method != "POST" || q.URL != graphqlURL {
		t.Errorf("lookup call = %s %s", q.Method, q.URL)
	}
	wantVars := map[string]any{"owner": "o", "repo": "r", "number": float64(7), "n0": "needs fix", "n1": "a/b"}
	g := gqlOf(t, q)
	if !reflect.DeepEqual(g.Variables, wantVars) {
		t.Errorf("lookup variables = %#v, want %#v", g.Variables, wantVars)
	}
	if strings.Contains(g.Query, "needs fix") || strings.Contains(g.Query, "a/b") {
		t.Error("label name interpolated into the query text")
	}
	m := (*got)[2]
	if m.Method != "POST" || m.URL != graphqlURL {
		t.Errorf("mutation call = %s %s", m.Method, m.URL)
	}
	wantVars = map[string]any{"id": "I_7", "ids": []any{"L_fix", "L_ab"}}
	if g := gqlOf(t, m); !strings.Contains(g.Query, "removeLabelsFromLabelable") || !reflect.DeepEqual(g.Variables, wantVars) {
		t.Errorf("mutation = %+v", g)
	}
}

func TestEditLabelsRemoveOnlySkipsAdd(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"l0":{"id":"L_x"}}}}`)
	if err := editLabels(tgt, "7", []string{""}, []string{"", "x"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 || (*got)[0].URL != graphqlURL || (*got)[1].URL != graphqlURL {
		t.Fatalf("calls = %+v, want the two GraphQL calls only", *got)
	}
	if v := gqlOf(t, (*got)[0]).Variables; v["n0"] != "x" || v["n1"] != nil {
		t.Errorf("variables = %#v, want only n0=x", v)
	}
}

func TestEditLabelsEmptyEditMakesNoCalls(t *testing.T) {
	got := stub(t, nil)
	if err := editLabels(tgt, "7", nil, []string{""}); err != nil || len(*got) != 0 {
		t.Errorf("empty edit: err = %v, calls = %d", err, len(*got))
	}
}

func TestEditLabelsAbsentLabelIsSkipped(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"l0":null,"l1":{"id":"L_y"}}}}`)
	if err := editLabels(tgt, "7", nil, []string{"gone", "y"}); err != nil {
		t.Fatal(err)
	}
	ids := gqlOf(t, (*got)[len(*got)-1]).Variables["ids"]
	if want := []any{"L_y"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("mutation ids = %#v, want %#v", ids, want)
	}
}

func TestEditLabelsAllAbsentSendsNoMutation(t *testing.T) {
	got := labelFake(t, `{"data":{"repository":{"issue":{"id":"I_7"},"l0":null}}}`)
	if err := editLabels(tgt, "7", nil, []string{"gone"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Errorf("calls = %d, want only the lookup", len(*got))
	}
}

func TestEditLabelsMissingIssueOrRepoFails(t *testing.T) {
	for name, lookup := range map[string]string{
		"null issue": `{"data":{"repository":{"issue":null,"l0":{"id":"L"}}}}`,
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
	old := send
	t.Cleanup(func() { send = old })
	send = func(r connector.Request) (connector.Response, error) {
		if r.URL == issuesURL+"/7/labels" {
			return connector.Response{Status: 500}, nil
		}
		return ok(`{}`), nil
	}
	if err := editLabels(tgt, "7", []string{"a"}, nil); err == nil {
		t.Error("add 500: want error")
	}
	send = func(r connector.Request) (connector.Response, error) {
		if strings.Contains(r.Body, "removeLabelsFromLabelable") {
			return ok(`{"errors":[{"message":"denied"}]}`), nil
		}
		return ok(`{"data":{"repository":{"issue":{"id":"I"},"l0":{"id":"L"}}}}`), nil
	}
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
	old := send
	t.Cleanup(func() { send = old })
	send = func(connector.Request) (connector.Response, error) {
		return ok("[" + strings.Join(full, ",") + "]"), nil
	}
	if _, err := listLabels(tgt); err == nil {
		t.Error("want error past maxPages")
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
