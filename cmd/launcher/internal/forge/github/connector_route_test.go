package github

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
)

const routeLabel = "orders-api"

// routedClient is a client whose tracker carries the Issue route, set at
// construction because the tracker view is built once.
func routedClient(t *testing.T) *execClient {
	t.Helper()
	return newClient(t, connectorRepo, testLabels, WithRoute(forge.Route{{Kind: "label", Value: routeLabel}}))
}

// An Issue route reaches the Connector on every tracker read and filing, which
// narrows the native labels filter to the route's issues; an unrouted client
// sends none and sees the whole scope.
func TestExecClient_IssueRouteNarrowsReadsAndFilesWithItsLabel(t *testing.T) {
	h, c, _ := routedHarness(t)
	h.SeedIssue(forge.Issue{Number: "1", Title: "in route", Labels: []string{testLabels.Dispatchable, routeLabel, "finding"}})
	h.SeedIssue(forge.Issue{Number: "2", Title: "other route", Labels: []string{testLabels.Dispatchable, "finding"}})

	if n, err := c.CountReady(true); err != nil || n != 2 {
		t.Fatalf("unrouted CountReady = %d, %v; want the whole scope, 2", n, err)
	}
	if got := h.backend.listReads(); len(got) != 1 || !strings.Contains(got[0].query, "labels="+testLabels.Dispatchable+"&") {
		t.Fatalf("unrouted count reads = %+v; want the dispatch label alone", got)
	}

	c = routedClient(t)
	if n, err := c.CountReady(true); err != nil || n != 1 {
		t.Errorf("routed CountReady = %d, %v; want 1", n, err)
	}
	if got, err := c.ListIssues(forge.Dispatchable); err != nil || len(got) != 1 || got[0].Number != "1" {
		t.Errorf("routed ListIssues = %v, %v; want issue 1", got, err)
	}
	if got, err := c.ListIssuesWithLabels(forge.IssueOpen, []string{"finding"}); err != nil || len(got) != 1 || got[0].Number != "1" {
		t.Errorf("routed ListIssuesWithLabels = %v, %v; want issue 1", got, err)
	}
	for _, r := range h.backend.listReads() {
		if !strings.Contains(r.query, "labels=") || !strings.Contains(r.query, routeLabel) {
			t.Errorf("routed read %q lacks the route label", r.query)
		}
	}

	url, err := c.PostIssue("a finding", "body", []string{"agent-butler-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	num := url[strings.LastIndex(url, "/")+1:]
	h.backend.mu.Lock()
	filed := h.backend.find(num)
	var labels []string
	if filed != nil {
		labels = slices.Clone(filed.labels)
	}
	h.backend.mu.Unlock()
	for _, want := range []string{"agent-butler-finding", routeLabel} {
		if !slices.Contains(labels, want) {
			t.Errorf("filed labels = %v, want %q among them", labels, want)
		}
	}
}

func TestStrategy_RouteReachesTheTrackerNotTheCodeForge(t *testing.T) {
	s := backendkit.Shared{Connectors: testRuntime(t), RepoSlug: "o/r", Route: forge.Route{{Kind: "label", Value: routeLabel}}}
	tr, err := Strategy.NewIssueTracker(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.(*execClient).route; !slices.Equal(got, s.Route) {
		t.Errorf("tracker route = %v, want %v", got, s.Route)
	}
	cf, err := Strategy.NewCodeForge(s, nil, forge.SanitizedParent{}, tr)
	if err != nil {
		t.Fatal(err)
	}
	if got := cf.(*execClient).route; got != nil {
		t.Errorf("code forge route = %v, want none", got)
	}
}

// MatchesRoute is the per-issue read of the Issue route that Issue leaves
// unnarrowed: an in-route issue matches, an off-route one does not.
func TestExecClient_MatchesRouteReadsOneIssueAgainstTheRoute(t *testing.T) {
	h, c, _ := routedHarness(t)
	h.SeedIssue(forge.Issue{Number: "1", Title: "in route", Labels: []string{testLabels.Dispatchable, routeLabel}})
	h.SeedIssue(forge.Issue{Number: "2", Title: "other route", Labels: []string{testLabels.Dispatchable}})

	if ok, err := c.MatchesRoute("2"); err != nil || !ok {
		t.Errorf("unrouted MatchesRoute(2) = %v, %v; want true", ok, err)
	}
	c = routedClient(t)
	for num, want := range map[string]bool{"1": true, "2": false} {
		if ok, err := c.MatchesRoute(num); err != nil || ok != want {
			t.Errorf("routed MatchesRoute(%s) = %v, %v; want %v", num, ok, err, want)
		}
	}
}

// A Connector without tracker.route cannot answer a routed match, so it is
// refused rather than read as in route; an unrouted client needs no answer.
func TestExecClient_MatchesRouteRefusedWithoutTheRouteCapability(t *testing.T) {
	withoutCapability(t, connectortracker.CapRoute)
	h, c, _ := routedHarness(t)
	h.SeedIssue(forge.Issue{Number: "1", Title: "t", Labels: []string{testLabels.Dispatchable}})
	if ok, err := c.MatchesRoute("1"); err != nil || !ok {
		t.Errorf("unrouted MatchesRoute = %v, %v; want true", ok, err)
	}
	c = routedClient(t)
	if _, err := c.MatchesRoute("1"); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("routed MatchesRoute err = %v; want ErrProtocolUnsupported", err)
	}
}
