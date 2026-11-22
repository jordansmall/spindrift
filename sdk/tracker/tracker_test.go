package tracker

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var wantTarget = Target{API: "https://api.github.com", Repo: "o/r", Dialect: "cloud"}

const target = `"target":{"api":"https://api.github.com","repo":"o/r","dialect":"cloud"}`

func fakeReader(t *testing.T) Reader {
	t.Helper()
	return Reader{
		ListIssues: func(tg Target, q Query, limit int) ([]Issue, error) {
			if tg != wantTarget || !reflect.DeepEqual(q, Query{Label: "ready", Status: "In Review"}) || limit != 7 {
				t.Errorf("ListIssues(%v, %+v, %d)", tg, q, limit)
			}
			return []Issue{{Number: "3", Title: "t", Body: "b", State: "open", Labels: []string{"ready"}}}, nil
		},
		Issue: func(tg Target, number string) (Issue, error) {
			if tg != wantTarget || number != "3" {
				t.Errorf("Issue(%v, %q)", tg, number)
			}
			return Issue{Number: "3", State: "closed"}, nil
		},
		Comments: func(tg Target, number string) ([]Comment, error) {
			if tg != wantTarget || number != "3" {
				t.Errorf("Comments(%v, %q)", tg, number)
			}
			return []Comment{{Author: "a", CreatedAt: "2026-01-02T03:04:05Z", Body: "hi", Minimized: true, MinimizedReason: "SPAM", Association: "MEMBER"}}, nil
		},
	}
}

func call(t *testing.T, m map[string]func(json.RawMessage) (any, error), method, params string) string {
	t.Helper()
	res, err := m[method](json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCapabilities(t *testing.T) {
	r := fakeReader(t)
	r.Comments = nil
	caps, m := handlers(r)
	if want := []string{"tracker"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"tracker.comments", "tracker.nativeDeps", "tracker.blocks"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s handler present without its Reader field", name)
		}
	}
	r = fakeReader(t)
	caps, m = handlers(r)
	if want := []string{"tracker", "tracker.comments"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if _, ok := m["tracker.comments"]; !ok {
		t.Error("comments handler missing")
	}
	r.NativeDeps = func(Target, string) ([]string, error) { return nil, nil }
	caps, m = handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.nativeDeps"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if _, ok := m["tracker.nativeDeps"]; !ok {
		t.Error("nativeDeps handler missing")
	}
	r.Blocks = func(Target, string) ([]string, error) { return nil, nil }
	caps, m = handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.nativeDeps", "tracker.blocks"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if _, ok := m["tracker.blocks"]; !ok {
		t.Error("blocks handler missing")
	}
	r.NativeDeps = nil
	caps, _ = handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.blocks"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
}

func TestWireShapes(t *testing.T) {
	r := fakeReader(t)
	r.NativeDeps = func(tg Target, number string) ([]string, error) {
		if tg != wantTarget || number != "3" {
			t.Errorf("NativeDeps(%v, %q)", tg, number)
		}
		return []string{"4", "5"}, nil
	}
	r.Blocks = func(tg Target, number string) ([]string, error) {
		if tg != wantTarget || number != "3" {
			t.Errorf("Blocks(%v, %q)", tg, number)
		}
		return []string{"6"}, nil
	}
	_, m := handlers(r)
	cases := []struct{ method, params, want string }{
		{"tracker.listIssues", `{` + target + `,"label":"ready","status":"In Review","limit":7}`,
			`{"issues":[{"number":"3","title":"t","body":"b","state":"open","labels":["ready"],"closedAt":"","closedNotPlanned":false,"dedupMarker":"","status":""}]}`},
		{"tracker.issue", `{` + target + `,"number":"3"}`,
			`{"number":"3","title":"","body":"","state":"closed","labels":[],"closedAt":"","closedNotPlanned":false,"dedupMarker":"","status":""}`},
		{"tracker.comments", `{` + target + `,"number":"3"}`,
			`{"comments":[{"author":"a","createdAt":"2026-01-02T03:04:05Z","body":"hi","minimized":true,"minimizedReason":"SPAM","association":"MEMBER","researchMarker":false}]}`},
		{"tracker.nativeDeps", `{` + target + `,"number":"3"}`, `{"ids":["4","5"]}`},
		{"tracker.blocks", `{` + target + `,"number":"3"}`, `{"ids":["6"]}`},
	}
	for _, c := range cases {
		if got := call(t, m, c.method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
}

func TestSearchCapabilityAndWireShape(t *testing.T) {
	r := fakeReader(t)
	if _, m := handlers(r); m["tracker.searchIssues"] != nil {
		t.Error("searchIssues handler present without Reader.Search")
	}
	r.Search = func(tg Target, text, state, label string, limit int) ([]Issue, error) {
		if tg != wantTarget || text != "flaky test" || state != "closed" || label != "bug" || limit != 5 {
			t.Errorf("Search(%v, %q, %q, %q, %d)", tg, text, state, label, limit)
		}
		return []Issue{{Number: "8", Title: "t", State: "closed"}}, nil
	}
	caps, m := handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.searchIssues"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	got := call(t, m, "tracker.searchIssues", `{`+target+`,"text":"flaky test","state":"closed","label":"bug","limit":5}`)
	if want := `{"issues":[{"number":"8","title":"t","body":"","state":"closed","labels":[],"closedAt":"","closedNotPlanned":false,"dedupMarker":"","status":""}]}`; got != want {
		t.Errorf("tracker.searchIssues = %s, want %s", got, want)
	}

	r.Search = func(Target, string, string, string, int) ([]Issue, error) { return nil, nil }
	_, m = handlers(r)
	if got := call(t, m, "tracker.searchIssues", `{`+target+`}`); got != `{"issues":[]}` {
		t.Errorf("empty search = %s, want an empty array", got)
	}
}

func TestLabeledCapabilityAndWireShape(t *testing.T) {
	r := fakeReader(t)
	if _, m := handlers(r); m["tracker.labeled"] != nil {
		t.Error("labeled handler present without Reader.Labeled")
	}
	r.Labeled = func(tg Target, label, state string, limit int) ([]Issue, error) {
		if tg != wantTarget || label != "bug" || state != "closed" || limit != 5 {
			t.Errorf("Labeled(%v, %q, %q, %d)", tg, label, state, limit)
		}
		return []Issue{{Number: "8", Title: "t", State: "closed", ClosedAt: "2026-01-02T03:04:05Z", ClosedNotPlanned: true}}, nil
	}
	caps, m := handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.labeled"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	got := call(t, m, "tracker.labeled", `{`+target+`,"label":"bug","state":"closed","limit":5}`)
	if want := `{"issues":[{"number":"8","title":"t","body":"","state":"closed","labels":[],"closedAt":"2026-01-02T03:04:05Z","closedNotPlanned":true,"dedupMarker":"","status":""}]}`; got != want {
		t.Errorf("tracker.labeled = %s, want %s", got, want)
	}

	r.Labeled = func(Target, string, string, int) ([]Issue, error) { return nil, nil }
	_, m = handlers(r)
	if got := call(t, m, "tracker.labeled", `{`+target+`}`); got != `{"issues":[]}` {
		t.Errorf("empty labeled = %s, want an empty array", got)
	}
}

func TestLabeledPageCursorAndPrecedence(t *testing.T) {
	r := fakeReader(t)
	r.LabeledPage = func(tg Target, label string, route []Selector, state, cursor string, limit int) ([]Issue, string, error) {
		if tg != wantTarget || label != "bug" || route != nil || state != "open" || limit != 2 {
			t.Errorf("LabeledPage(%v, %q, %v, %q, %q, %d)", tg, label, route, state, cursor, limit)
		}
		if cursor == "" {
			return []Issue{{Number: "1"}}, "c2", nil
		}
		return []Issue{{Number: "2"}}, "", nil
	}
	r.Labeled = func(Target, string, string, int) ([]Issue, error) {
		t.Error("Labeled called although LabeledPage is set")
		return nil, nil
	}
	caps, m := handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.labeled"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	const issue = `"title":"","body":"","state":"","labels":[],"closedAt":"","closedNotPlanned":false,"dedupMarker":"","status":""`
	base := `{` + target + `,"label":"bug","state":"open","limit":2`
	if got, want := call(t, m, "tracker.labeled", base+`}`), `{"issues":[{"number":"1",`+issue+`}],"next":"c2"}`; got != want {
		t.Errorf("first page = %s, want %s", got, want)
	}
	if got, want := call(t, m, "tracker.labeled", base+`,"cursor":"c2"}`), `{"issues":[{"number":"2",`+issue+`}]}`; got != want {
		t.Errorf("last page = %s, want %s", got, want)
	}
}

func TestLabeledRoute(t *testing.T) {
	route := []Selector{{Kind: "component", Value: "api"}, {Kind: "label", Value: "repo-a"}}
	const params = `{` + target + `,"label":"bug","state":"open","limit":2,"route":[{"kind":"component","value":"api"},{"kind":"label","value":"repo-a"}]}`

	r := fakeReader(t)
	r.LabeledPage = func(_ Target, _ string, got []Selector, _, _ string, _ int) ([]Issue, string, error) {
		if !reflect.DeepEqual(got, route) {
			t.Errorf("LabeledPage route = %v, want %v", got, route)
		}
		return nil, "", nil
	}
	_, m := handlers(r)
	call(t, m, "tracker.labeled", params)

	// Labeled cannot narrow, so a routed scan would list the whole scope.
	r = fakeReader(t)
	r.Labeled = func(Target, string, string, int) ([]Issue, error) {
		t.Error("Labeled called with a route")
		return nil, nil
	}
	_, m = handlers(r)
	if _, err := m["tracker.labeled"](json.RawMessage(params)); err == nil {
		t.Error("Labeled accepted a non-empty route")
	}
}

func TestLabeledOnlyOmitsNext(t *testing.T) {
	r := fakeReader(t)
	r.Labeled = func(Target, string, string, int) ([]Issue, error) { return nil, nil }
	_, m := handlers(r)
	if got := call(t, m, "tracker.labeled", `{`+target+`,"cursor":"x"}`); got != `{"issues":[]}` {
		t.Errorf("labeled-only = %s, want no next key", got)
	}
}

func TestEmptyListsEncodeAsArrays(t *testing.T) {
	r := Reader{
		ListIssues: func(Target, Query, int) ([]Issue, error) { return nil, nil },
		Issue:      func(Target, string) (Issue, error) { return Issue{}, nil },
		Comments:   func(Target, string) ([]Comment, error) { return nil, nil },
		NativeDeps: func(Target, string) ([]string, error) { return nil, nil },
		Blocks:     func(Target, string) ([]string, error) { return nil, nil },
	}
	_, m := handlers(r)
	p := `{` + target + `,"number":"1"}`
	for method, want := range map[string]string{
		"tracker.listIssues": `{"issues":[]}`,
		"tracker.comments":   `{"comments":[]}`,
		"tracker.nativeDeps": `{"ids":[]}`,
		"tracker.blocks":     `{"ids":[]}`,
	} {
		if got := call(t, m, method, p); got != want {
			t.Errorf("%s = %s, want %s", method, got, want)
		}
	}
}

func TestBadParams(t *testing.T) {
	r := fakeReader(t)
	r.NativeDeps = func(Target, string) ([]string, error) { return nil, nil }
	r.Search = func(Target, string, string, string, int) ([]Issue, error) { return nil, nil }
	_, m := handlers(r)
	for name, fn := range m {
		if _, err := fn(json.RawMessage(`{not json`)); err == nil {
			t.Errorf("%s: bad params accepted", name)
		}
	}
}

func TestReaderErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	r := fakeReader(t)
	r.Issue = func(Target, string) (Issue, error) { return Issue{}, boom }
	_, m := handlers(r)
	if _, err := m["tracker.issue"](json.RawMessage(`{` + target + `,"number":"3"}`)); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestMissingRequiredPanics(t *testing.T) {
	full := fakeReader(t)
	for name, mut := range map[string]func(*Reader){
		"ListIssues": func(r *Reader) { r.ListIssues = nil },
		"Issue":      func(r *Reader) { r.Issue = nil },
	} {
		r := full
		mut(&r)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s unset: no panic", name)
				}
			}()
			handlers(r)
		}()
	}
}

func TestRoutesCapability(t *testing.T) {
	r := fakeReader(t)
	r.Routes = true
	r.MatchesRoute = func(Target, string, []Selector) (bool, error) { return true, nil }
	caps, _ := handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.route"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	r.Routes = false
	caps, m := handlers(r)
	if want := []string{"tracker", "tracker.comments"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("unrouted caps = %v, want %v", caps, want)
	}
	if m["tracker.matchesRoute"] != nil {
		t.Error("matchesRoute handler present without Reader.Routes")
	}
}

func TestMatchesRouteWireShape(t *testing.T) {
	r := fakeReader(t)
	r.Routes = true
	r.MatchesRoute = func(tg Target, number string, route []Selector) (bool, error) {
		if tg != wantTarget || number != "7" || !reflect.DeepEqual(route, []Selector{{Kind: "label", Value: "orders"}}) {
			t.Errorf("MatchesRoute(%v, %q, %v)", tg, number, route)
		}
		return true, nil
	}
	_, m := handlers(r)
	got := call(t, m, "tracker.matchesRoute", `{`+target+`,"number":"7","route":[{"kind":"label","value":"orders"}]}`)
	if want := `{"matches":true}`; got != want {
		t.Errorf("tracker.matchesRoute = %s, want %s", got, want)
	}
	r.MatchesRoute = func(Target, string, []Selector) (bool, error) { return false, nil }
	_, m = handlers(r)
	if got := call(t, m, "tracker.matchesRoute", `{`+target+`,"number":"7"}`); got != `{"matches":false}` {
		t.Errorf("miss = %s, want matches false", got)
	}
	r.MatchesRoute = func(Target, string, []Selector) (bool, error) { return false, errors.New("boom") }
	_, m = handlers(r)
	if _, err := m["tracker.matchesRoute"](json.RawMessage(`{` + target + `,"number":"7"}`)); err == nil {
		t.Error("MatchesRoute error not propagated")
	}
}

func TestRoutesWithoutMatchesRoutePanics(t *testing.T) {
	r := fakeReader(t)
	r.Routes = true
	defer func() {
		if recover() == nil {
			t.Error("Routes without MatchesRoute: no panic")
		}
	}()
	handlers(r)
}

func TestProjectKeyIDsCapability(t *testing.T) {
	r := fakeReader(t)
	r.IDs = ProjectKeyIDs
	caps, _ := handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.ids.projectKey"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	r.IDs = DecimalIDs
	caps, _ = handlers(r)
	if want := []string{"tracker", "tracker.comments"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("decimal caps = %v, want %v", caps, want)
	}
}

func TestMarkerFieldsEncode(t *testing.T) {
	r := fakeReader(t)
	r.Issue = func(Target, string) (Issue, error) {
		return Issue{Number: "3", State: "closed", ClosedAt: "2026-01-02T03:04:05Z", ClosedNotPlanned: true, DedupMarker: "marker-line"}, nil
	}
	r.Comments = func(Target, string) ([]Comment, error) { return []Comment{{Body: "x", ResearchMarker: true}}, nil }
	_, m := handlers(r)
	if got, want := call(t, m, "tracker.issue", `{`+target+`,"number":"3"}`),
		`{"number":"3","title":"","body":"","state":"closed","labels":[],"closedAt":"2026-01-02T03:04:05Z","closedNotPlanned":true,"dedupMarker":"marker-line","status":""}`; got != want {
		t.Errorf("issue = %s, want %s", got, want)
	}
	if got := call(t, m, "tracker.comments", `{`+target+`,"number":"3"}`); !strings.Contains(got, `"researchMarker":true`) {
		t.Errorf("comments = %s, want researchMarker", got)
	}
}

func jiraReader(t *testing.T) Reader {
	t.Helper()
	r := fakeReader(t)
	r.Probe = func(tg Target) (int, error) {
		if tg != wantTarget {
			t.Errorf("Probe(%v)", tg)
		}
		return 401, nil
	}
	r.Statuses = func(tg Target) ([]Status, error) {
		if tg != wantTarget {
			t.Errorf("Statuses(%v)", tg)
		}
		return []Status{{Name: "To Do"}, {Name: "Done"}}, nil
	}
	r.CreateMeta = func(tg Target, issueType string) (CreateMeta, error) {
		if tg != wantTarget || issueType != "Task" {
			t.Errorf("CreateMeta(%v, %q)", tg, issueType)
		}
		return CreateMeta{
			IssueTypes: []string{"Task", "Bug"},
			Fields:     []CreateField{{ID: "summary", Name: "Summary", Required: true, HasDefaultValue: false}},
		}, nil
	}
	r.Count = func(tg Target, q Query, _ int, _ bool) (int, error) {
		if tg != wantTarget || !reflect.DeepEqual(q, Query{Label: "ready", Status: "In Review"}) {
			t.Errorf("Count(%v, %+v)", tg, q)
		}
		return 12, nil
	}
	r.CountInterval = 90*time.Second + 900*time.Millisecond
	return r
}

func TestDoctorCapabilities(t *testing.T) {
	caps, _ := handlers(fakeReader(t))
	if want := []string{"tracker", "tracker.comments"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	caps, m := handlers(jiraReader(t))
	want := []string{"tracker", "tracker.comments", "tracker.probe", "tracker.statuses", "tracker.createMeta", "tracker.count"}
	if !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"tracker.probe", "tracker.statuses", "tracker.createMeta", "tracker.count", "tracker.countInterval"} {
		if _, ok := m[name]; !ok {
			t.Errorf("%s handler missing", name)
		}
	}
	r := jiraReader(t)
	r.Probe, r.Statuses, r.CreateMeta, r.Count, r.CountInterval = nil, nil, nil, nil, 0
	_, m = handlers(r)
	for _, name := range []string{"tracker.probe", "tracker.statuses", "tracker.createMeta", "tracker.count", "tracker.countInterval"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s handler present without its Reader field", name)
		}
	}
}

func TestDoctorWireShapes(t *testing.T) {
	_, m := handlers(jiraReader(t))
	cases := []struct{ method, params, want string }{
		{"tracker.probe", `{` + target + `}`, `{"status":401}`},
		{"tracker.statuses", `{` + target + `}`, `{"statuses":["To Do","Done"]}`},
		{"tracker.createMeta", `{` + target + `,"issueType":"Task"}`,
			`{"issueTypes":["Task","Bug"],"fields":[{"fieldId":"summary","name":"Summary","required":true,"hasDefaultValue":false}]}`},
		{"tracker.count", `{` + target + `,"label":"ready","status":"In Review"}`, `{"count":12}`},
		{"tracker.countInterval", `{}`, `{"seconds":90}`},
	}
	for _, c := range cases {
		if got := call(t, m, c.method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
}

func TestStatusesWireCarriesCategories(t *testing.T) {
	r := jiraReader(t)
	r.Statuses = func(Target) ([]Status, error) {
		return []Status{{Name: "To Do", Category: "new"}, {Name: "Done", Category: StatusCategoryDone}, {Name: "Odd"}}, nil
	}
	_, m := handlers(r)
	want := `{"statuses":["To Do","Done","Odd"],"categories":{"Done":"done","To Do":"new"}}`
	if got := call(t, m, "tracker.statuses", `{`+target+`}`); got != want {
		t.Errorf("statuses = %s, want %s", got, want)
	}
}

func TestStatusesWireDoneCategoryWinsConflict(t *testing.T) {
	for name, sts := range map[string][]Status{
		"done first": {{Name: "Closed", Category: StatusCategoryDone}, {Name: "Closed", Category: "indeterminate"}},
		"done last":  {{Name: "Closed", Category: "indeterminate"}, {Name: "Closed", Category: StatusCategoryDone}},
	} {
		r := jiraReader(t)
		r.Statuses = func(Target) ([]Status, error) { return sts, nil }
		_, m := handlers(r)
		want := `{"statuses":["Closed","Closed"],"categories":{"Closed":"done"}}`
		if got := call(t, m, "tracker.statuses", `{`+target+`}`); got != want {
			t.Errorf("%s: statuses = %s, want %s", name, got, want)
		}
	}
}

func TestDoctorEmptyListsEncodeAsArrays(t *testing.T) {
	r := jiraReader(t)
	r.Statuses = func(Target) ([]Status, error) { return nil, nil }
	r.CreateMeta = func(Target, string) (CreateMeta, error) { return CreateMeta{}, nil }
	_, m := handlers(r)
	if got, want := call(t, m, "tracker.statuses", `{`+target+`}`), `{"statuses":[]}`; got != want {
		t.Errorf("statuses = %s, want %s", got, want)
	}
	if got, want := call(t, m, "tracker.createMeta", `{`+target+`,"issueType":"Task"}`), `{"issueTypes":[],"fields":[]}`; got != want {
		t.Errorf("createMeta = %s, want %s", got, want)
	}
}

func TestDoctorBadParamsAndErrors(t *testing.T) {
	_, m := handlers(jiraReader(t))
	for _, name := range []string{"tracker.probe", "tracker.statuses", "tracker.createMeta", "tracker.count"} {
		if _, err := m[name](json.RawMessage(`{not json`)); err == nil {
			t.Errorf("%s: bad params accepted", name)
		}
	}
	boom := errors.New("boom")
	r := jiraReader(t)
	r.Probe = func(Target) (int, error) { return 0, boom }
	r.Count = func(Target, Query, int, bool) (int, error) { return 0, boom }
	_, m = handlers(r)
	for _, name := range []string{"tracker.probe", "tracker.count"} {
		if _, err := m[name](json.RawMessage(`{` + target + `}`)); !errors.Is(err, boom) {
			t.Errorf("%s err = %v, want boom", name, err)
		}
	}
}

func TestCountWithoutIntervalPanics(t *testing.T) {
	r := jiraReader(t)
	r.CountInterval = 500 * time.Millisecond
	defer func() {
		if recover() == nil {
			t.Error("Count with CountInterval < 1s: no panic")
		}
	}()
	handlers(r)
}

func TestRemovedLabels(t *testing.T) {
	r := fakeReader(t)
	caps, m := handlers(r)
	if _, ok := m["tracker.removedLabels"]; ok || len(caps) != 2 {
		t.Fatalf("removedLabels advertised without its Reader field: %v", caps)
	}
	r.RemovedLabels = func(tg Target, number string) ([]string, error) {
		if tg != wantTarget || number != "3" {
			t.Errorf("RemovedLabels(%v, %q)", tg, number)
		}
		return []string{"agent-failed", "agent-complete"}, nil
	}
	caps, m = handlers(r)
	if want := []string{"tracker", "tracker.comments", "tracker.removedLabels"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if got, want := call(t, m, "tracker.removedLabels", `{`+target+`,"number":"3"}`), `{"labels":["agent-failed","agent-complete"]}`; got != want {
		t.Errorf("removedLabels = %s, want %s", got, want)
	}
	r.RemovedLabels = func(Target, string) ([]string, error) { return nil, nil }
	_, m = handlers(r)
	if got, want := call(t, m, "tracker.removedLabels", `{`+target+`,"number":"3"}`), `{"labels":[]}`; got != want {
		t.Errorf("empty removedLabels = %s, want %s", got, want)
	}
	if _, err := m["tracker.removedLabels"](json.RawMessage(`{not json`)); err == nil {
		t.Error("bad params accepted")
	}
	boom := errors.New("boom")
	r.RemovedLabels = func(Target, string) ([]string, error) { return nil, boom }
	_, m = handlers(r)
	if _, err := m["tracker.removedLabels"](json.RawMessage(`{` + target + `,"number":"3"}`)); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestCountWireForwardsLimitAndFresh(t *testing.T) {
	r := jiraReader(t)
	var gotLimit int
	var gotFresh bool
	r.Count = func(_ Target, _ Query, limit int, fresh bool) (int, error) {
		gotLimit, gotFresh = limit, fresh
		return 3, nil
	}
	_, m := handlers(r)
	call(t, m, "tracker.count", `{`+target+`,"label":"ready","status":"In Review","limit":50,"fresh":true}`)
	if gotLimit != 50 || !gotFresh {
		t.Errorf("Count got limit=%d fresh=%v, want 50 true", gotLimit, gotFresh)
	}
	call(t, m, "tracker.count", `{`+target+`,"label":"ready","status":"In Review"}`)
	if gotLimit != 0 || gotFresh {
		t.Errorf("absent params: limit=%d fresh=%v, want 0 false", gotLimit, gotFresh)
	}
}

func TestQueryRouteWireShape(t *testing.T) {
	b, err := json.Marshal(Query{Label: "ready"})
	if err != nil || string(b) != `{"label":"ready","status":""}` {
		t.Errorf("unrouted Query = %s, %v; want no route key", b, err)
	}
	b, err = json.Marshal(Query{Route: []Selector{{Kind: "label", Value: "orders"}}})
	if err != nil || string(b) != `{"label":"","status":"","route":[{"kind":"label","value":"orders"}]}` {
		t.Errorf("routed Query = %s, %v", b, err)
	}
}

func TestRouteLabels(t *testing.T) {
	label := func(v string) Selector { return Selector{Kind: "label", Value: v} }
	for name, tc := range map[string]struct {
		label string
		route []Selector
		want  []string
		err   bool
	}{
		"nothing":          {},
		"label only":       {label: "ready", want: []string{"ready"}},
		"route only":       {route: []Selector{label("a"), label("b")}, want: []string{"a", "b"}},
		"label then route": {label: "ready", route: []Selector{label("a")}, want: []string{"ready", "a"}},
		"unrouted comma":   {label: "a,b", want: []string{"a,b"}},
		"routed comma":     {label: "a,b", route: []Selector{label("c")}, err: true},
		"comma in value":   {route: []Selector{label("a,b")}, err: true},
		"empty value":      {route: []Selector{label("")}, err: true},
		"non-label kind":   {label: "ready", route: []Selector{{Kind: "project", Value: "x"}}, err: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := RouteLabels(tc.label, tc.route)
			if (err != nil) != tc.err {
				t.Fatalf("err = %v, want error %v", err, tc.err)
			}
			if !tc.err && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("labels = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStampRouteLabels(t *testing.T) {
	route := []Selector{{Kind: "label", Value: "orders"}, {Kind: "label", Value: "team-a"}}
	got, err := StampRouteLabels([]string{"finding", "team-a"}, route)
	if want := []string{"finding", "team-a", "orders"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("StampRouteLabels = %v, %v; want %v", got, err, want)
	}
	if got, err := StampRouteLabels(nil, nil); err != nil || len(got) != 0 || got == nil {
		t.Errorf("unrouted = %#v, %v; want an empty, non-nil list", got, err)
	}
	if _, err := StampRouteLabels([]string{"finding"}, []Selector{{Kind: "component", Value: "x"}}); err == nil {
		t.Error("a non-label selector was stamped")
	}
}
