package forge

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRoute(t *testing.T) {
	tests := []struct {
		in      string
		want    Route
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "  ", want: nil},
		{in: "label=orders-api", want: Route{{"label", "orders-api"}}},
		{in: "label=a, label=b", want: Route{{"label", "a"}, {"label", "b"}}},
		{in: "team=x,label=y", want: Route{{"team", "x"}, {"label", "y"}}},
		{in: "label", wantErr: true},
		{in: "label=", wantErr: true},
		{in: "=x", wantErr: true},
		{in: "label=a,", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseRoute(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseRoute(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseRoute(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRoute_LabelsMatchStamp(t *testing.T) {
	r := Route{{"label", "a"}, {"team", "x"}, {"label", "b"}}
	if got := r.Labels(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Labels() = %v, want [a b]", got)
	}
	lr := Route{{"label", "a"}, {"label", "b"}}
	if !lr.MatchesLabels([]string{"b", "a", "z"}) || lr.MatchesLabels([]string{"a"}) {
		t.Error("MatchesLabels must require every label selector")
	}
	if !Route(nil).MatchesLabels(nil) {
		t.Error("nil route must match everything")
	}
	in := []string{"a", "z"}
	if got := r.StampLabels(in); !reflect.DeepEqual(got, []string{"a", "z", "b"}) {
		t.Errorf("StampLabels = %v, want [a z b]", got)
	}
	if !reflect.DeepEqual(in, []string{"a", "z"}) {
		t.Errorf("StampLabels mutated its input: %v", in)
	}
	if got := Route(nil).StampLabels(in); !reflect.DeepEqual(got, in) {
		t.Errorf("nil route StampLabels = %v, want unchanged", got)
	}
}

func TestFake_Route_NarrowsListAndCountAndStampsPostIssue(t *testing.T) {
	f := NewFake(DispatchLabels{Dispatchable: "ready"})
	f.SetIssue(Issue{Number: "1", Labels: []string{"ready", "orders"}})
	f.SetIssue(Issue{Number: "2", Labels: []string{"ready"}})

	all, _ := f.ListIssues(Dispatchable)
	if len(all) != 2 {
		t.Fatalf("unset route listed %d issues, want 2", len(all))
	}
	if _, err := f.AsIssueFiler().(HostPostedIssueFiler).PostIssue("t", "b", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got := f.PostIssueCalls[0].Labels; !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("unset route PostIssue labels = %v, want [x]", got)
	}

	f.Route = Route{{"label", "orders"}}
	got, _ := f.ListIssues(Dispatchable)
	if len(got) != 1 || got[0].Number != "1" {
		t.Errorf("routed ListIssues = %+v, want only 1", got)
	}
	if n, _ := f.CountReady(false); n != 1 {
		t.Errorf("routed CountReady = %d, want 1", n)
	}
	if _, err := f.AsIssueFiler().(HostPostedIssueFiler).PostIssue("t2", "b", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got := f.PostIssueCalls[1].Labels; !reflect.DeepEqual(got, []string{"x", "orders"}) {
		t.Errorf("routed PostIssue labels = %v, want [x orders]", got)
	}
}

// A key read is not a queue: a blocker, parent, or linked issue outside the
// route must still resolve by its key.
func TestFake_Route_IssueReadByKeyIsNotNarrowed(t *testing.T) {
	f := NewFake(DispatchLabels{Dispatchable: "ready"})
	f.SetIssue(Issue{Number: "1", Labels: []string{"ready", "billing"}})
	f.Route = Route{{RouteKindLabel, "orders"}}
	got, err := f.Issue("1")
	if err != nil || got.Number != "1" {
		t.Errorf("Issue(1) under a foreign route = %+v, %v, want the issue", got, err)
	}
}

func TestFake_MatchesRoute(t *testing.T) {
	f := NewFake(DispatchLabels{Dispatchable: "ready"})
	f.SetIssue(Issue{Number: "1", Labels: []string{"ready", "orders"}})
	f.SetIssue(Issue{Number: "2", Labels: []string{"ready", "billing"}})
	f.Route = Route{{RouteKindLabel, "orders"}}
	rm, ok := As[RouteMatcher](f)
	if !ok {
		t.Fatal("the fake tracker is not a RouteMatcher")
	}
	for num, want := range map[string]bool{"1": true, "2": false} {
		if got, err := rm.MatchesRoute(num); err != nil || got != want {
			t.Errorf("MatchesRoute(%s) = %v, %v, want %v", num, got, err, want)
		}
	}
	if _, err := rm.MatchesRoute("9"); err == nil {
		t.Error("MatchesRoute of a missing issue = nil error, want not found")
	}
	f.Route = nil
	if got, err := rm.MatchesRoute("2"); err != nil || !got {
		t.Errorf("unrouted MatchesRoute(2) = %v, %v, want true", got, err)
	}
	if len(f.IssueCalls) != 0 {
		t.Errorf("MatchesRoute recorded %d Issue calls, want none", len(f.IssueCalls))
	}
}

func TestRouteCheckKinds(t *testing.T) {
	r := Route{{"label", "a"}}
	if err := r.CheckKinds("local", []string{"label"}); err != nil {
		t.Errorf("declared kind: CheckKinds = %v, want nil", err)
	}
	if err := Route(nil).CheckKinds("github", nil); err != nil {
		t.Errorf("nil route: CheckKinds = %v, want nil", err)
	}
	err := r.CheckKinds("github", nil)
	if err == nil || !strings.Contains(err.Error(), `"label"`) || !strings.Contains(err.Error(), "ISSUE_TRACKER=github") {
		t.Errorf("undeclared kind: CheckKinds = %v, want an error naming the kind and tracker", err)
	}
}

func TestRoute_MatchesLabelsFailsClosedOnOtherKinds(t *testing.T) {
	r := Route{{"label", "a"}, {"team", "x"}}
	if r.MatchesLabels([]string{"a", "x"}) {
		t.Error("a selector kind MatchesLabels cannot evaluate must not match")
	}
}

func TestResolveRoute(t *testing.T) {
	got, err := ResolveRoute("label=a", "local", []string{"label"})
	if err != nil || !reflect.DeepEqual(got, Route{{"label", "a"}}) {
		t.Errorf("ResolveRoute ok = %v, %v", got, err)
	}
	if got, err := ResolveRoute("", "github", nil); err != nil || got != nil {
		t.Errorf("ResolveRoute(unset) = %v, %v, want nil, nil", got, err)
	}
	for name, tt := range map[string]struct {
		raw   string
		kinds []string
		want  string
	}{
		"malformed":  {"orders", []string{"label"}, "ISSUE_ROUTE: issue route"},
		"undeclared": {"team=x", []string{"label"}, "ISSUE_ROUTE: route kind \"team\""},
		"no kinds":   {"label=a", nil, "ISSUE_ROUTE: route kind \"label\""},
	} {
		if _, err := ResolveRoute(tt.raw, "t", tt.kinds); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want prefix %q", name, err, tt.want)
		}
	}
}

// A selector kind a label tracker cannot evaluate fails closed, like local.
func TestFake_Route_FailsClosedOnUnhandledKind(t *testing.T) {
	fc := NewFake(DispatchLabels{Dispatchable: "ready"})
	fc.Route = Route{{"team", "x"}}
	fc.SetIssue(Issue{Number: "1", Labels: []string{"ready", "x"}})
	issues, err := fc.ListIssues(Dispatchable)
	if err != nil || len(issues) != 0 {
		t.Errorf("ListIssues = %v, %v; want empty, nil", issues, err)
	}
}
