package main

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

func TestIssueRoute_ValidateRejectsMalformed(t *testing.T) {
	c := minimalValidConfig()
	c.IssueTracker = "local"
	c.IssueRoute = "label"
	for name, fn := range map[string]func(config) error{"validate": validate, "validateConfig": validateConfig} {
		err := fn(c)
		if err == nil || !strings.Contains(err.Error(), "ISSUE_ROUTE") || !strings.Contains(err.Error(), "kind=value") {
			t.Errorf("%s with malformed ISSUE_ROUTE = %v, want an ISSUE_ROUTE kind=value error", name, err)
		}
	}
}

// A tracker that ignored the route would quietly list the whole Tracker scope,
// so a kind the tracker's descriptor does not declare fails loud.
func TestIssueRoute_ValidateRejectsUndeclaredKind(t *testing.T) {
	cases := []struct{ tracker, route string }{
		{"github", "component=x"},
		{"local", "project=x"},
	}
	for _, tc := range cases {
		c := minimalValidConfig()
		c.IssueTracker = tc.tracker
		c.IssueRoute = tc.route
		for name, fn := range map[string]func(config) error{"validate": validate, "validateConfig": validateConfig} {
			err := fn(c)
			if err == nil || !strings.Contains(err.Error(), "ISSUE_ROUTE") || !strings.Contains(err.Error(), tc.tracker) {
				t.Errorf("%s ISSUE_TRACKER=%s ISSUE_ROUTE=%s = %v, want an ISSUE_ROUTE error naming the tracker", name, tc.tracker, tc.route, err)
			}
		}
	}
}

func TestIssueRoute_LocalLabelAcceptedAndReachesShared(t *testing.T) {
	c := minimalValidConfig()
	c.IssueTracker = "local"
	c.IssueRoute = "label=x"
	if err := validateIssueRoute(c); err != nil {
		t.Fatalf("validateIssueRoute(local, label=x) = %v, want nil", err)
	}
	want := forge.Route{{Kind: "label", Value: "x"}}
	if got := backendShared(c).Route; !reflect.DeepEqual(got, want) {
		t.Errorf("backendShared(c).Route = %v, want %v", got, want)
	}
}

func TestIssueRoute_UnsetIsNil(t *testing.T) {
	c := minimalValidConfig()
	if err := validateIssueRoute(c); err != nil {
		t.Fatalf("validateIssueRoute(unset) = %v, want nil", err)
	}
	if got := backendShared(c).Route; got != nil {
		t.Errorf("backendShared(c).Route = %v, want nil", got)
	}
}

// The work and research queues list only issues matching every route selector;
// with no route the queue is the whole dispatchable scope.
func TestIssueRoute_QueueListsOnlyRoutedIssues(t *testing.T) {
	kinds := []struct {
		name   string
		kind   *dispatchkind.Descriptor
		labels forge.DispatchLabels
	}{
		{"work", dispatchkind.Work, testDispatchLabels},
		{"research", dispatchkind.Research, forge.ResearchDispatchLabels()},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			queue := func(route string) []string {
				c := baseConfig()
				c = applyDispatchKind(c, k.kind)
				c.Label = k.labels.Dispatchable
				c.IssueRoute = route
				fc := forge.NewFake(k.labels)
				fc.Route = issueRoute(c)
				fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label, "orders-api"}})
				fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
				fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label, "billing"}})
				issues, _, err := queryOpenIssues(c, fc)
				if err != nil {
					t.Fatalf("queryOpenIssues: %v", err)
				}
				var got []string
				for _, iss := range issues {
					got = append(got, iss.number)
				}
				return got
			}
			if got, want := queue("label=orders-api"), []string{"1"}; !reflect.DeepEqual(got, want) {
				t.Errorf("routed queue = %v, want %v", got, want)
			}
			if got, want := queue(""), []string{"1", "2", "3"}; !reflect.DeepEqual(got, want) {
				t.Errorf("unset-route queue = %v, want %v", got, want)
			}
		})
	}
}
