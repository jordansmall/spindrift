package forge

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"spindrift.dev/sdk/connector"
)

func fakeBranches(t *testing.T) Branches {
	t.Helper()
	return Branches{
		Remote: func(tg Target) (Remote, error) {
			if tg != wantTarget {
				t.Errorf("Remote(%v)", tg)
			}
			return Remote{CloneURL: "https://forge.example/o/r.git", WebURL: "https://forge.example/o/r"}, nil
		},
		BranchExists: func(tg Target, branch string) (bool, error) {
			if tg != wantTarget || branch != "agent/issue-3" {
				t.Errorf("BranchExists(%v, %q)", tg, branch)
			}
			return true, nil
		},
		DeleteBranch: func(tg Target, branch string) error {
			if tg != wantTarget || branch != "agent/issue-3" {
				t.Errorf("DeleteBranch(%v, %q)", tg, branch)
			}
			return nil
		},
	}
}

func TestBranchCapabilities(t *testing.T) {
	b := fakeBranches(t)
	caps, m := branchHandlers(b)
	if want := []string{"forge.branches"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	b.PushOnly = true
	caps, _ = branchHandlers(b)
	if want := []string{"forge.branches", "forge.pushOnly"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("push-only caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"forge.remote", "forge.branchExists", "forge.deleteBranch"} {
		if _, ok := m[name]; !ok {
			t.Errorf("%s handler missing", name)
		}
	}
	if len(m) != 3 {
		t.Errorf("%d handlers, want 3", len(m))
	}
}

func TestBranchWireShapes(t *testing.T) {
	_, m := branchHandlers(fakeBranches(t))
	cases := []struct{ method, params, want string }{
		{"forge.remote", `{` + target + `}`,
			`{"cloneUrl":"https://forge.example/o/r.git","webUrl":"https://forge.example/o/r"}`},
		{"forge.branchExists", `{` + target + `,"branch":"agent/issue-3"}`, `{"exists":true}`},
		{"forge.deleteBranch", `{` + target + `,"branch":"agent/issue-3"}`, `{}`},
	}
	for _, c := range cases {
		if got := call(t, m, c.method, c.params); got != c.want {
			t.Errorf("%s = %s, want %s", c.method, got, c.want)
		}
	}
}

func TestBranchBadParams(t *testing.T) {
	_, m := branchHandlers(fakeBranches(t))
	for name, fn := range m {
		if _, err := fn(json.RawMessage(`{not json`)); err == nil {
			t.Errorf("%s: bad params accepted", name)
		}
	}
}

func TestBranchErrorPassesThrough(t *testing.T) {
	boom := connector.Errorf(connector.NotFound, "gone")
	b := fakeBranches(t)
	b.Remote = func(Target) (Remote, error) { return Remote{}, boom }
	b.BranchExists = func(Target, string) (bool, error) { return false, boom }
	b.DeleteBranch = func(Target, string) error { return boom }
	_, m := branchHandlers(b)
	for method, params := range map[string]string{
		"forge.remote":       `{` + target + `}`,
		"forge.branchExists": `{` + target + `,"branch":"b"}`,
		"forge.deleteBranch": `{` + target + `,"branch":"b"}`,
	} {
		if _, err := m[method](json.RawMessage(params)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the Connector's own error", method, err)
		}
	}
}

func TestMissingBranchFuncPanics(t *testing.T) {
	full := fakeBranches(t)
	for name, mut := range map[string]func(*Branches){
		"Remote":       func(b *Branches) { b.Remote = nil },
		"BranchExists": func(b *Branches) { b.BranchExists = nil },
		"DeleteBranch": func(b *Branches) { b.DeleteBranch = nil },
	} {
		b := full
		mut(&b)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s unset: no panic", name)
				}
			}()
			branchHandlers(b)
		}()
	}
}
