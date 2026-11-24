package connectortracker

import (
	"errors"
	"maps"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

var testRoute = forge.Route{{Kind: "label", Value: "orders-api"}, {Kind: "component", Value: "Orders"}}

var wantRouteWire = []any{
	map[string]any{"kind": "label", "value": "orders-api"},
	map[string]any{"kind": "component", "value": "Orders"},
}

var routeReplies = map[string]string{
	"tracker.listIssues":    `{"issues":[]}`,
	"tracker.countInterval": `{"seconds":5}`,
	CapCount:                `{"count":1}`,
	CapLabeled:              `{"issues":[]}`,
	CapFileIssue:            `{"url":"https://tracker.example/o/r/issues/7"}`,
	"tracker.matchesRoute":  `{"matches":false}`,
}

var routeCaps = []string{CapCount, CapLabeled, CapFileIssue, CapRoute}

func routedLifecycle(route forge.Route) Lifecycle {
	lc := testLifecycle
	lc.Route = route
	return lc
}

// ListOpenIssues is the whole-scope readiness read, so it never carries a route.
func TestRouteNarrowsQueuesCountBacklogAndFilingButNotOpenList(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, routeCaps, true)
	dc, _ := forge.As[forge.DemandCounter](tr)
	lb, _ := forge.As[forge.LabeledBacklogLister](tr)
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	*calls = nil
	steps := []struct {
		method string
		want   any
		run    func() error
	}{
		{"tracker.listIssues", wantRouteWire, func() error { _, err := tr.ListIssues(forge.Dispatchable); return err }},
		{"tracker.listIssues", nil, func() error { _, err := tr.ListOpenIssues(); return err }},
		{CapCount, wantRouteWire, func() error { _, err := dc.CountReady(false); return err }},
		{CapLabeled, wantRouteWire, func() error {
			_, err := lb.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding"})
			return err
		}},
		{CapFileIssue, wantRouteWire, func() error { _, err := filer.PostIssue("t", "b", []string{"l"}); return err }},
	}
	for i, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("step %d (%s): %v", i, s.method, err)
		}
		c := (*calls)[len(*calls)-1]
		if c.method != s.method {
			t.Fatalf("step %d: last call %s, want %s", i, c.method, s.method)
		}
		if got := c.params["route"]; !reflect.DeepEqual(got, s.want) {
			t.Errorf("step %d (%s): route = %v, want %v", i, s.method, got, s.want)
		}
	}
}

func TestNoRouteLeavesTheParamsWithoutARouteKey(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, testLifecycle, testTarget, routeCaps, true)
	dc, _ := forge.As[forge.DemandCounter](tr)
	lb, _ := forge.As[forge.LabeledBacklogLister](tr)
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	if _, err := tr.ListIssues(forge.Dispatchable); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.CountReady(false); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.ListIssuesWithLabels(forge.IssueClosed, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := filer.PostIssue("t", "b", nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if _, ok := c.params["route"]; ok {
			t.Errorf("%s carried a route key with no route set: %v", c.method, c.params)
		}
	}
}

// A Connector that does not advertise tracker.route drops the unknown route
// field and would answer the whole scope, whatever protocol minor it speaks, so
// a route is refused unsent.
func TestConnectorWithoutRouteCapRefusesARouteUnsent(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, []string{CapCount, CapLabeled, CapFileIssue}, true)
	*calls = nil
	if _, err := tr.ListIssues(forge.Dispatchable); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("ListIssues err = %v, want ErrProtocolUnsupported", err)
	}
	dc, _ := forge.As[forge.DemandCounter](tr)
	if _, err := dc.CountReady(false); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("CountReady err = %v, want ErrProtocolUnsupported", err)
	}
	lb, _ := forge.As[forge.LabeledBacklogLister](tr)
	if _, err := lb.ListIssuesWithLabels(forge.IssueClosed, []string{"x"}); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("ListIssuesWithLabels err = %v, want ErrProtocolUnsupported", err)
	}
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	if _, err := filer.PostIssue("t", "b", nil); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("PostIssue err = %v, want ErrProtocolUnsupported", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a routed call reached the guest: %v", *calls)
	}
}

// A pre-0.2 Connector's capabilities are not trusted, CapRoute included.
func TestPre02ConnectorAdvertisingRouteCapRefusesARoute(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, []string{CapRoute}, false)
	*calls = nil
	if _, err := tr.ListIssues(forge.Dispatchable); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("ListIssues err = %v, want ErrProtocolUnsupported", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a routed call reached the guest: %v", *calls)
	}
}

func TestMatchesRouteAsksTheConnectorWithTheRoute(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, routeCaps, true)
	rm, ok := forge.As[forge.RouteMatcher](tr)
	if !ok {
		t.Fatal("the tracker is not a RouteMatcher")
	}
	*calls = nil
	got, err := rm.MatchesRoute("7")
	if err != nil || got {
		t.Fatalf("MatchesRoute = %v, %v, want false", got, err)
	}
	want := recordedCall{"tracker.matchesRoute", map[string]any{
		"target": map[string]any{"api": testTarget.API, "repo": testTarget.Repo, "dialect": ""},
		"number": "7",
		"route":  wantRouteWire,
	}}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("calls = %+v, want [%+v]", *calls, want)
	}
}

func TestMatchesRouteTrueWhenTheConnectorSaysSo(t *testing.T) {
	call, _ := stubGuest(t, withReply("tracker.matchesRoute", `{"matches":true}`))
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, routeCaps, true)
	rm, _ := forge.As[forge.RouteMatcher](tr)
	if got, err := rm.MatchesRoute("7"); err != nil || !got {
		t.Errorf("MatchesRoute = %v, %v, want true", got, err)
	}
}

func TestMatchesRouteEmptyRouteMatchesWithoutACall(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, testLifecycle, testTarget, routeCaps, true)
	rm, _ := forge.As[forge.RouteMatcher](tr)
	*calls = nil
	if got, err := rm.MatchesRoute("7"); err != nil || !got {
		t.Errorf("MatchesRoute = %v, %v, want true", got, err)
	}
	if len(*calls) != 0 {
		t.Errorf("an unrouted match reached the guest: %v", *calls)
	}
}

func TestMatchesRouteRefusedUnsentWithoutTheRouteCap(t *testing.T) {
	call, calls := stubGuest(t, withReply("tracker.matchesRoute", `{"matches":true}`))
	tr := mustTracker(t, call, routedLifecycle(testRoute), testTarget, []string{CapCount}, true)
	rm, _ := forge.As[forge.RouteMatcher](tr)
	*calls = nil
	if _, err := rm.MatchesRoute("7"); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("MatchesRoute err = %v, want ErrProtocolUnsupported", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a routed call reached the guest: %v", *calls)
	}
}

func withReply(method, reply string) map[string]string {
	m := maps.Clone(routeReplies)
	m[method] = reply
	return m
}
