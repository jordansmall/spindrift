package tracker

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func fakeWriter(t *testing.T) Writer {
	t.Helper()
	return Writer{
		Transition: func(tg Target, number string, r TransitionRequest) (TransitionResult, error) {
			if tg != wantTarget || number != "3" || !reflect.DeepEqual(r, TransitionRequest{State: "complete", Mapping: map[string]StatusTarget{"complete": {Status: "Done", Resolution: "Fixed"}}}) {
				t.Errorf("Transition(%v, %q, %+v)", tg, number, r)
			}
			return TransitionResult{Outcome: TransitionMoved}, nil
		},
		EditLabels: func(tg Target, number string, add, remove []string) error {
			if tg != wantTarget || number != "3" || !reflect.DeepEqual(add, []string{"a"}) || !reflect.DeepEqual(remove, []string{"b", "c"}) {
				t.Errorf("EditLabels(%v, %q, %v, %v)", tg, number, add, remove)
			}
			return nil
		},
		ListLabels: func(tg Target) ([]string, error) {
			if tg != wantTarget {
				t.Errorf("ListLabels(%v)", tg)
			}
			return []string{"x", "y"}, nil
		},
		CreateLabel: func(tg Target, name, description, color string) error {
			if tg != wantTarget || name != "n" || description != "d" || color != "ff0000" {
				t.Errorf("CreateLabel(%v, %q, %q, %q)", tg, name, description, color)
			}
			return nil
		},
	}
}

func TestWriteCapabilities(t *testing.T) {
	caps, m := writeHandlers(fakeWriter(t))
	if want := []string{CapTransition, CapEditLabels, CapLabels}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	for _, name := range []string{"tracker.transition", "tracker.editLabels", "tracker.listLabels", "tracker.createLabel"} {
		if m[name] == nil {
			t.Errorf("method %s missing", name)
		}
	}

	caps, m = writeHandlers(Writer{})
	if len(caps) != 0 || len(m) != 0 {
		t.Fatalf("empty Writer advertises caps=%v methods=%d", caps, len(m))
	}

	caps, m = writeHandlers(Writer{ListLabels: fakeWriter(t).ListLabels})
	if want := []string{CapLabels}; !reflect.DeepEqual(caps, want) {
		t.Fatalf("caps = %v, want %v", caps, want)
	}
	if m["tracker.listLabels"] == nil || m["tracker.createLabel"] == nil || m["tracker.transition"] != nil {
		t.Errorf("methods = %v", m)
	}
}

func TestTransition(t *testing.T) {
	_, m := writeHandlers(fakeWriter(t))
	got := call(t, m, "tracker.transition", `{`+target+`,"number":"3","state":"complete","mapping":{"complete":{"status":"Done","resolution":"Fixed"}}}`)
	if want := `{"outcome":"moved","status":"","offered":[]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestTransitionOutcomePassthrough(t *testing.T) {
	w := Writer{Transition: func(Target, string, TransitionRequest) (TransitionResult, error) {
		return TransitionResult{Outcome: TransitionUnavailable, Status: "Blocked", Offered: []string{"To Do", "Done"}}, nil
	}}
	_, m := writeHandlers(w)
	got := call(t, m, "tracker.transition", `{`+target+`,"number":"3","state":"failed","mapping":{"failed":{"status":"Blocked","resolution":""}}}`)
	if want := `{"outcome":"unavailable","status":"Blocked","offered":["To Do","Done"]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	w.Transition = func(Target, string, TransitionRequest) (TransitionResult, error) {
		return TransitionResult{Outcome: TransitionUnmapped}, nil
	}
	_, m = writeHandlers(w)
	if got, want := call(t, m, "tracker.transition", `{`+target+`,"number":"3","state":"failed"}`), `{"outcome":"unmapped","status":"","offered":[]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestEditLabels(t *testing.T) {
	_, m := writeHandlers(fakeWriter(t))
	if got := call(t, m, "tracker.editLabels", `{`+target+`,"number":"3","add":["a"],"remove":["b","c"]}`); got != `{}` {
		t.Errorf("got %s, want {}", got)
	}
}

func TestListLabels(t *testing.T) {
	_, m := writeHandlers(fakeWriter(t))
	if got, want := call(t, m, "tracker.listLabels", `{`+target+`}`), `{"labels":["x","y"]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	_, m = writeHandlers(Writer{ListLabels: func(Target) ([]string, error) { return nil, nil }})
	if got, want := call(t, m, "tracker.listLabels", `{`+target+`}`), `{"labels":[]}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestCreateLabel(t *testing.T) {
	_, m := writeHandlers(fakeWriter(t))
	if got := call(t, m, "tracker.createLabel", `{`+target+`,"name":"n","description":"d","color":"ff0000"}`); got != `{}` {
		t.Errorf("got %s, want {}", got)
	}
}

func TestCreateLabelNoRegistryIsNoOp(t *testing.T) {
	_, m := writeHandlers(Writer{ListLabels: func(Target) ([]string, error) { return nil, nil }})
	if got := call(t, m, "tracker.createLabel", `{`+target+`,"name":"n","description":"d","color":"ff0000"}`); got != `{}` {
		t.Errorf("got %s, want {}", got)
	}
}

func TestCreateLabelWithoutListLabelsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	writeHandlers(Writer{CreateLabel: func(Target, string, string, string) error { return nil }})
}

func TestWriteErrorsPass(t *testing.T) {
	boom := errors.New("boom")
	_, m := writeHandlers(Writer{
		Transition:  func(Target, string, TransitionRequest) (TransitionResult, error) { return TransitionResult{}, boom },
		EditLabels:  func(Target, string, []string, []string) error { return boom },
		ListLabels:  func(Target) ([]string, error) { return nil, boom },
		CreateLabel: func(Target, string, string, string) error { return boom },
	})
	for name, params := range map[string]string{
		"tracker.transition":  `{` + target + `,"number":"3"}`,
		"tracker.editLabels":  `{` + target + `,"number":"3"}`,
		"tracker.listLabels":  `{` + target + `}`,
		"tracker.createLabel": `{` + target + `,"name":"n"}`,
	} {
		if _, err := m[name](json.RawMessage(params)); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want boom", name, err)
		}
	}
}

func TestWriteBadParams(t *testing.T) {
	_, m := writeHandlers(fakeWriter(t))
	for name := range m {
		if _, err := m[name](json.RawMessage(`[`)); err == nil {
			t.Errorf("%s accepted malformed params", name)
		}
	}
}

func TestIssueStatusEncodes(t *testing.T) {
	b, err := json.Marshal(normalize(Issue{Number: "1", Status: "In Progress"}))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["status"] != "In Progress" {
		t.Errorf("status = %v in %s", back["status"], b)
	}
}
