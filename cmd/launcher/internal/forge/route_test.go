package forge

import (
	"reflect"
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
	if !r.MatchesLabels([]string{"b", "a", "z"}) || r.MatchesLabels([]string{"a"}) {
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
