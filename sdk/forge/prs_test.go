package forge

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"spindrift.dev/sdk/connector"
)

func fakePRs(t *testing.T) PRs {
	t.Helper()
	return PRs{
		Open: func(tg Target, p NewPR) (PR, error) {
			if tg != wantTarget || p != (NewPR{Title: "T", Body: "B", Base: "main", Head: "agent/issue-3", Draft: true}) {
				t.Errorf("Open(%v, %+v)", tg, p)
			}
			return PR{Number: "9", URL: "https://forge.example/o/r/pull/9", State: "open", Draft: true,
				Head: "agent/issue-3", HeadSHA: "abc", Mergeable: "MERGEABLE", MergeCommit: "def"}, nil
		},
		Behind: func(tg Target, n string) (int, error) {
			if tg != wantTarget || n != "9" {
				t.Errorf("Behind(%v, %q)", tg, n)
			}
			return 4, nil
		},
		Files: func(tg Target, n string) ([]string, error) {
			if tg != wantTarget || n != "9" {
				t.Errorf("Files(%v, %q)", tg, n)
			}
			return []string{"a.go", "b/c.go"}, nil
		},
		SetDraft: func(tg Target, n string, draft bool) error {
			if tg != wantTarget || n != "9" || draft {
				t.Errorf("SetDraft(%v, %q, %v)", tg, n, draft)
			}
			return nil
		},
		Comment: func(tg Target, n, body string) error {
			if tg != wantTarget || n != "9" || body != "hello" {
				t.Errorf("Comment(%v, %q, %q)", tg, n, body)
			}
			return nil
		},
		Merge: func(tg Target, n string) error {
			if tg != wantTarget || n != "9" {
				t.Errorf("Merge(%v, %q)", tg, n)
			}
			return nil
		},
	}
}

func withOptionals(p PRs) PRs {
	p.AutoMergeAllowed = func(Target) (bool, error) { return true, nil }
	p.EnqueueAutoMerge = func(Target, string) error { return nil }
	p.CIRun = func(Target, string) (string, error) { return "https://ci.example/run/1", nil }
	p.BranchProtected = func(_ Target, branch string) (bool, error) { return branch == "main", nil }
	p.Probe = func(Target) (string, error) { return "o/r", nil }
	return p
}

var requiredPRMethods = []string{
	"forge.openPR", "forge.behind", "forge.prFiles", "forge.setDraft", "forge.comment", "forge.merge",
}

func TestPRCapabilities(t *testing.T) {
	caps, m := prHandlers(fakePRs(t))
	if want := []string{"forge.prs"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if len(m) != len(requiredPRMethods) {
		t.Errorf("%d handlers, want %d", len(m), len(requiredPRMethods))
	}
	for _, name := range requiredPRMethods {
		if _, ok := m[name]; !ok {
			t.Errorf("%s handler missing", name)
		}
	}

	caps, m = prHandlers(withOptionals(fakePRs(t)))
	want := []string{"forge.prs", "forge.autoMerge", "forge.ciRun", "forge.branchProtection", "forge.probe"}
	if !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"forge.autoMergeAllowed", "forge.enqueueAutoMerge", "forge.ciRun",
		"forge.branchProtected", "forge.probe"} {
		if _, ok := m[name]; !ok {
			t.Errorf("%s handler missing", name)
		}
	}
	if len(m) != len(requiredPRMethods)+5 {
		t.Errorf("%d handlers, want %d", len(m), len(requiredPRMethods)+5)
	}
}

func TestPRWireShapes(t *testing.T) {
	_, m := prHandlers(withOptionals(fakePRs(t)))
	cases := []struct{ method, params, want string }{
		{"forge.openPR", `{` + target + `,"pr":{"title":"T","body":"B","base":"main","head":"agent/issue-3","draft":true}}`,
			`{"number":"9","url":"https://forge.example/o/r/pull/9","state":"open","merged":false,` +
				`"draft":true,"head":"agent/issue-3","headSha":"abc","mergeable":"MERGEABLE","mergeCommit":"def"}`},
		{"forge.behind", `{` + target + `,"number":"9"}`, `{"behindBy":4}`},
		{"forge.prFiles", `{` + target + `,"number":"9"}`, `{"files":["a.go","b/c.go"]}`},
		{"forge.setDraft", `{` + target + `,"number":"9","draft":false}`, `{}`},
		{"forge.comment", `{` + target + `,"number":"9","body":"hello"}`, `{}`},
		{"forge.merge", `{` + target + `,"number":"9"}`, `{}`},
		{"forge.autoMergeAllowed", `{` + target + `}`, `{"allowed":true}`},
		{"forge.enqueueAutoMerge", `{` + target + `,"number":"9"}`, `{}`},
		{"forge.ciRun", `{` + target + `,"number":"9"}`, `{"url":"https://ci.example/run/1"}`},
		{"forge.branchProtected", `{` + target + `,"branch":"main"}`, `{"protected":true}`},
		{"forge.probe", `{` + target + `}`, `{"repo":"o/r"}`},
	}
	for _, c := range cases {
		if got := call(t, m, c.method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
}

func TestPRZeroFieldsOmitted(t *testing.T) {
	p := fakePRs(t)
	p.Open = func(Target, NewPR) (PR, error) { return PR{Number: "9", URL: "u", State: "open"}, nil }
	_, m := prHandlers(p)
	got := call(t, m, "forge.openPR", `{`+target+`,"pr":{}}`)
	if want := `{"number":"9","url":"u","state":"open","merged":false}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestPRFilesEncodesEmptyList(t *testing.T) {
	p := fakePRs(t)
	p.Files = func(Target, string) ([]string, error) { return nil, nil }
	_, m := prHandlers(p)
	if got := call(t, m, "forge.prFiles", `{`+target+`,"number":"9"}`); got != `{"files":[]}` {
		t.Errorf("got %s", got)
	}
}

func TestPRBadParams(t *testing.T) {
	_, m := prHandlers(withOptionals(fakePRs(t)))
	for name, fn := range m {
		if _, err := fn(json.RawMessage(`{not json`)); err == nil {
			t.Errorf("%s: bad params accepted", name)
		}
	}
}

func TestPRErrorPassesThrough(t *testing.T) {
	boom := connector.Errorf(connector.NotFound, "gone")
	p := withOptionals(fakePRs(t))
	p.Open = func(Target, NewPR) (PR, error) { return PR{}, boom }
	p.Behind = func(Target, string) (int, error) { return 0, boom }
	p.Files = func(Target, string) ([]string, error) { return nil, boom }
	p.SetDraft = func(Target, string, bool) error { return boom }
	p.Comment = func(Target, string, string) error { return boom }
	p.Merge = func(Target, string) error { return boom }
	p.AutoMergeAllowed = func(Target) (bool, error) { return false, boom }
	p.EnqueueAutoMerge = func(Target, string) error { return boom }
	p.CIRun = func(Target, string) (string, error) { return "", boom }
	p.BranchProtected = func(Target, string) (bool, error) { return false, boom }
	p.Probe = func(Target) (string, error) { return "", boom }
	_, m := prHandlers(p)
	for method, params := range map[string]string{
		"forge.openPR":           `{` + target + `}`,
		"forge.behind":           `{` + target + `,"number":"9"}`,
		"forge.prFiles":          `{` + target + `,"number":"9"}`,
		"forge.setDraft":         `{` + target + `,"number":"9"}`,
		"forge.comment":          `{` + target + `,"number":"9"}`,
		"forge.merge":            `{` + target + `,"number":"9"}`,
		"forge.autoMergeAllowed": `{` + target + `}`,
		"forge.enqueueAutoMerge": `{` + target + `,"number":"9"}`,
		"forge.ciRun":            `{` + target + `,"number":"9"}`,
		"forge.branchProtected":  `{` + target + `,"branch":"b"}`,
		"forge.probe":            `{` + target + `}`,
	} {
		if _, err := m[method](json.RawMessage(params)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the Connector's own error", method, err)
		}
	}
}

func TestMissingPRFuncPanics(t *testing.T) {
	full := fakePRs(t)
	for name, mut := range map[string]func(*PRs){
		"Open":     func(p *PRs) { p.Open = nil },
		"Behind":   func(p *PRs) { p.Behind = nil },
		"Files":    func(p *PRs) { p.Files = nil },
		"SetDraft": func(p *PRs) { p.SetDraft = nil },
		"Comment":  func(p *PRs) { p.Comment = nil },
		"Merge":    func(p *PRs) { p.Merge = nil },
		"AutoMergeAllowed only": func(p *PRs) {
			p.AutoMergeAllowed = func(Target) (bool, error) { return true, nil }
		},
		"EnqueueAutoMerge only": func(p *PRs) { p.EnqueueAutoMerge = func(Target, string) error { return nil } },
	} {
		p := full
		mut(&p)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			prHandlers(p)
		}()
	}
}

func TestPROptionalsAreIndependent(t *testing.T) {
	full := withOptionals(fakePRs(t))
	for cap, mut := range map[string]func(*PRs){
		"forge.ciRun":            func(p *PRs) { p.CIRun = nil },
		"forge.branchProtection": func(p *PRs) { p.BranchProtected = nil },
		"forge.probe":            func(p *PRs) { p.Probe = nil },
		"forge.autoMerge": func(p *PRs) {
			p.AutoMergeAllowed, p.EnqueueAutoMerge = nil, nil
		},
	} {
		p := full
		mut(&p)
		caps, _ := prHandlers(p)
		if slices.Contains(caps, cap) || len(caps) != 4 {
			t.Errorf("without %s: caps = %v", cap, caps)
		}
	}
}
