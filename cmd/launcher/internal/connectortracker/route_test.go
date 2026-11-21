package connectortracker

import (
	"errors"
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
	CapFileIssue:            `{"url":"https://j/browse/P-1"}`,
}

var routeCaps = []string{CapCount, CapLabeled, CapFileIssue}

func routedLifecycle(route forge.Route) Lifecycle {
	lc := testLifecycle
	lc.Route = route
	return lc
}

// ListOpenIssues is the whole-scope readiness read, so it never carries a route.
func TestRouteNarrowsQueuesCountBacklogAndFilingButNotOpenList(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, newNative(), routedLifecycle(testRoute), testTarget, routeCaps, true)
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
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, routeCaps, true)
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

// A 0.1 Connector ignores a selector it does not know and would list the
// whole scope, so a route is refused unsent.
func TestLegacyConnectorRefusesARouteUnsent(t *testing.T) {
	call, calls := stubGuest(t, routeReplies)
	tr := mustTracker(t, call, newNative(), routedLifecycle(testRoute), testTarget, routeCaps, false)
	*calls = nil
	if _, err := tr.ListIssues(forge.Dispatchable); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("ListIssues err = %v, want ErrProtocolUnsupported", err)
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
		t.Errorf("a routed 0.1 call reached the guest: %v", *calls)
	}
}
