package connectortrackertest_test

import (
	"net/http"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker/connectortrackertest"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// fixtureConnector is the fixture tracker Connector with a hand-written Route
// table, as a Connector's own package declares one.
func fixtureConnector(t *testing.T, wasmFile string) connectorforgetest.Connector {
	t.Helper()
	return connectorforgetest.Connector{
		Wasm:   connectorforgetest.FixtureWasm(t, wasmFile),
		Routes: []connectorhost.Route{{Method: http.MethodGet, Host: "tracker.example", Path: "/api/v1/repos/{repo}"}},
	}
}

func TestFixtureTracker_ReadContract(t *testing.T) {
	h := connectortrackertest.New(t, fixtureConnector(t, "tracker.wasm"))
	forgetest.RunTrackerReadContract(t, h)

	if _, ok := forge.As[forge.CommentLister](h.Tracker()); !ok {
		t.Error("the fixture tracker advertises comments but its tracker is no CommentLister")
	}
	if _, ok := h.(forgetest.NativeCapable); !ok {
		t.Error("the fixture tracker advertises native deps but its harness is not NativeCapable")
	}
}

func TestViolatingTracker_IsLoadable(t *testing.T) {
	h := connectortrackertest.New(t, fixtureConnector(t, "tracker-violate.wasm"))
	h.SeedIssue(forge.Issue{Number: "1", Title: "honest"})
	iss, err := h.Tracker().Issue("1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.Title == "honest" {
		t.Errorf("tracker-violate.wasm answered the seeded title %q, want a wrong one", iss.Title)
	}
}

func writeFixtureConnector(t *testing.T, wasmFile string) connectorforgetest.Connector {
	t.Helper()
	c := fixtureConnector(t, wasmFile)
	c.Routes = append(c.Routes,
		connectorhost.Route{Method: http.MethodPost, Host: "tracker.example", Path: "/api/v1/repos/{repo}"},
		connectorhost.Route{Method: http.MethodDelete, Host: "tracker.example", Path: "/api/v1/repos/{repo}"})
	return c
}

func TestFixtureWriteTracker_FullContract(t *testing.T) {
	forgetest.RunTrackerContract(t, connectortrackertest.New(t, writeFixtureConnector(t, "tracker-writes.wasm")))
}

// transitionLeavesLabels moves issue 1 from Dispatchable to InProgress and
// returns the labels it carries afterwards.
func transitionLeavesLabels(t *testing.T, wasmFile string) []string {
	t.Helper()
	h := connectortrackertest.New(t, writeFixtureConnector(t, wasmFile))
	h.SeedIssue(forge.Issue{Number: "1", Title: "t"})
	tr := h.Tracker()
	from := forge.Untriaged
	for _, to := range []forge.DispatchState{forge.Dispatchable, forge.InProgress} {
		if err := tr.TransitionState("1", from, to); err != nil {
			t.Fatalf("TransitionState(%v): %v", to, err)
		}
		from = to
	}
	iss, err := tr.Issue("1")
	if err != nil {
		t.Fatal(err)
	}
	return iss.Labels
}

func TestFixtureWriteTracker_RemovesTheLabelsItLeaves(t *testing.T) {
	if got := transitionLeavesLabels(t, "tracker-writes.wasm"); !slices.Equal(got, []string{connectortrackertest.Labels.InProgress}) {
		t.Errorf("labels after claim = %v, want only %q", got, connectortrackertest.Labels.InProgress)
	}
	if got := transitionLeavesLabels(t, "tracker-writes-violate.wasm"); !slices.Contains(got, connectortrackertest.Labels.Dispatchable) {
		t.Errorf("tracker-writes-violate.wasm removed %q, want it left stale: %v", connectortrackertest.Labels.Dispatchable, got)
	}
}
