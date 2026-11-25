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
		Probe: func(tg Target) (string, error) {
			if tg != wantTarget {
				t.Errorf("Probe(%v)", tg)
			}
			return "o/r", nil
		},
	}
}

func TestBranchCapabilities(t *testing.T) {
	b := fakeBranches(t)
	caps, m := branchHandlers(b)
	if want := []string{"forge.branches", "forge.probe"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	b.PushOnly = true
	caps, _ = branchHandlers(b)
	if want := []string{"forge.branches", "forge.probe", "forge.pushOnly"}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("push-only caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"forge.branchExists", "forge.deleteBranch", "forge.probe"} {
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
		{"forge.branchExists", `{` + target + `,"branch":"agent/issue-3"}`, `{"exists":true}`},
		{"forge.deleteBranch", `{` + target + `,"branch":"agent/issue-3"}`, `{}`},
		{"forge.probe", `{` + target + `}`, `{"repo":"o/r"}`},
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
	b.BranchExists = func(Target, string) (bool, error) { return false, boom }
	b.DeleteBranch = func(Target, string) error { return boom }
	b.Probe = func(Target) (string, error) { return "", boom }
	_, m := branchHandlers(b)
	for method, params := range map[string]string{
		"forge.branchExists": `{` + target + `,"branch":"b"}`,
		"forge.deleteBranch": `{` + target + `,"branch":"b"}`,
		"forge.probe":        `{` + target + `}`,
	} {
		if _, err := m[method](json.RawMessage(params)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the Connector's own error", method, err)
		}
	}
}

func TestMissingBranchFuncPanics(t *testing.T) {
	full := fakeBranches(t)
	for name, mut := range map[string]func(*Branches){
		"BranchExists": func(b *Branches) { b.BranchExists = nil },
		"DeleteBranch": func(b *Branches) { b.DeleteBranch = nil },
		"Probe":        func(b *Branches) { b.Probe = nil },
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
