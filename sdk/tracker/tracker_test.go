package tracker

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var wantTarget = Target{API: "https://api.github.com", Repo: "o/r", Dialect: "cloud"}

const target = `"target":{"api":"https://api.github.com","repo":"o/r","dialect":"cloud"}`

func fakeReader(t *testing.T) Reader {
	t.Helper()
	return Reader{
		ListIssues: func(tg Target, q Query, limit int) ([]Issue, error) {
			if tg != wantTarget || q != (Query{Label: "ready", Status: "In Review"}) || limit != 7 {
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
			`{"issues":[{"number":"3","title":"t","body":"b","state":"open","labels":["ready"],"closedAt":"","closedNotPlanned":false,"dedupMarker":""}]}`},
		{"tracker.issue", `{` + target + `,"number":"3"}`,
			`{"number":"3","title":"","body":"","state":"closed","labels":[],"closedAt":"","closedNotPlanned":false,"dedupMarker":""}`},
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
		`{"number":"3","title":"","body":"","state":"closed","labels":[],"closedAt":"2026-01-02T03:04:05Z","closedNotPlanned":true,"dedupMarker":"marker-line"}`; got != want {
		t.Errorf("issue = %s, want %s", got, want)
	}
	if got := call(t, m, "tracker.comments", `{`+target+`,"number":"3"}`); !strings.Contains(got, `"researchMarker":true`) {
		t.Errorf("comments = %s, want researchMarker", got)
	}
}
