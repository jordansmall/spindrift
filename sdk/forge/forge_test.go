package forge

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

var wantTarget = Target{API: "https://api.github.com", Repo: "o/r"}

const target = `"target":{"api":"https://api.github.com","repo":"o/r"}`

func fakeReader(t *testing.T) Reader {
	t.Helper()
	return Reader{
		PRsForBranch: func(tg Target, branch string) ([]PR, error) {
			if tg != wantTarget || branch != "agent/issue-3" {
				t.Errorf("PRsForBranch(%v, %q)", tg, branch)
			}
			return []PR{{Number: "9", URL: "https://github.com/o/r/pull/9", State: "closed", Merged: true}}, nil
		},
		PR: func(tg Target, number string) (PR, error) {
			if tg != wantTarget || number != "9" {
				t.Errorf("PR(%v, %q)", tg, number)
			}
			return PR{Number: "9", State: "open"}, nil
		},
		Checks: func(tg Target, number string) (Rollup, error) {
			if tg != wantTarget || number != "9" {
				t.Errorf("Checks(%v, %q)", tg, number)
			}
			return Rollup{State: "FAILURE", Checks: []Check{
				{Kind: "CheckRun", Name: "build", Conclusion: "FAILURE", Summary: "boom"},
				{Kind: "StatusContext", Name: "ci", Conclusion: "SUCCESS", Summary: "ok"},
			}}, nil
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
	caps, m := handlers(fakeReader(t))
	if want := []string{"forge"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"forge.prsForBranch", "forge.pr", "forge.checks"} {
		if _, ok := m[name]; !ok {
			t.Errorf("%s handler missing", name)
		}
	}
	if len(m) != 3 {
		t.Errorf("%d handlers, want 3", len(m))
	}
}

func TestWireShapes(t *testing.T) {
	_, m := handlers(fakeReader(t))
	cases := []struct{ method, params, want string }{
		{"forge.prsForBranch", `{` + target + `,"branch":"agent/issue-3"}`,
			`{"prs":[{"number":"9","url":"https://github.com/o/r/pull/9","state":"closed","merged":true}]}`},
		{"forge.pr", `{` + target + `,"number":"9"}`,
			`{"number":"9","url":"","state":"open","merged":false}`},
		{"forge.checks", `{` + target + `,"number":"9"}`,
			`{"state":"FAILURE","checks":[{"kind":"CheckRun","name":"build","conclusion":"FAILURE","summary":"boom"},{"kind":"StatusContext","name":"ci","conclusion":"SUCCESS","summary":"ok"}]}`},
	}
	for _, c := range cases {
		if got := call(t, m, c.method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
}

func TestEmptyListsEncodeAsArrays(t *testing.T) {
	r := Reader{
		PRsForBranch: func(Target, string) ([]PR, error) { return nil, nil },
		PR:           func(Target, string) (PR, error) { return PR{}, nil },
		Checks:       func(Target, string) (Rollup, error) { return Rollup{}, nil },
	}
	_, m := handlers(r)
	for method, c := range map[string]struct{ params, want string }{
		"forge.prsForBranch": {`{` + target + `,"branch":"b"}`, `{"prs":[]}`},
		"forge.checks":       {`{` + target + `,"number":"1"}`, `{"state":"","checks":[]}`},
	} {
		if got := call(t, m, method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", method, got, c.want)
		}
	}
}

func TestBadParams(t *testing.T) {
	_, m := handlers(fakeReader(t))
	for name, fn := range m {
		if _, err := fn(json.RawMessage(`{not json`)); err == nil {
			t.Errorf("%s: bad params accepted", name)
		}
	}
}

func TestReaderErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	r := fakeReader(t)
	r.PR = func(Target, string) (PR, error) { return PR{}, boom }
	r.Checks = func(Target, string) (Rollup, error) { return Rollup{}, boom }
	r.PRsForBranch = func(Target, string) ([]PR, error) { return nil, boom }
	_, m := handlers(r)
	for method, params := range map[string]string{
		"forge.pr":           `{` + target + `,"number":"9"}`,
		"forge.checks":       `{` + target + `,"number":"9"}`,
		"forge.prsForBranch": `{` + target + `,"branch":"b"}`,
	} {
		if _, err := m[method](json.RawMessage(params)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want boom", method, err)
		}
	}
}

func TestMissingRequiredPanics(t *testing.T) {
	full := fakeReader(t)
	for name, mut := range map[string]func(*Reader){
		"PRsForBranch": func(r *Reader) { r.PRsForBranch = nil },
		"PR":           func(r *Reader) { r.PR = nil },
		"Checks":       func(r *Reader) { r.Checks = nil },
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
