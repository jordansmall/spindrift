package jira_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/jira"
)

// connectorWriteHarness is the Connector harness for the write contract. It
// forwards only what the contract drives and leaves out forgetest.StateSeeder,
// so the contract moves state through TransitionState; the native adapter
// behind connectortracker.New still fails every call, so a write that reaches
// it fails the run.
type connectorWriteHarness struct{ h *connectorHarness }

func (w connectorWriteHarness) Tracker() forge.IssueTracker { return w.h.tr }
func (w connectorWriteHarness) SeedIssue(i forge.Issue)     { w.h.SeedIssue(i) }
func (w connectorWriteHarness) FailNativeDeps(n string)     { w.h.FailNativeDeps(n) }
func (w connectorWriteHarness) ID(n string) string          { return w.h.ID(n) }
func (w connectorWriteHarness) IsPriorityCapable()          {}
func (w connectorWriteHarness) SeedNativeDeps(n string, ids []string) {
	w.h.SeedNativeDeps(n, ids)
}
func (w connectorWriteHarness) SeedComments(n string, c []forge.Comment) { w.h.SeedComments(n, c) }

var (
	_ forgetest.Harness        = connectorWriteHarness{}
	_ forgetest.NativeCapable  = connectorWriteHarness{}
	_ forgetest.CommentCapable = connectorWriteHarness{}
	_ forgetest.IDMinter       = connectorWriteHarness{}
)

// writeBackend is a workflow the scripted backend offers: the statuses it has a
// transition to, and the screen fields a transition to one of them reports.
type writeBackend struct {
	statuses []string
	fields   map[string]map[string]jiraWireTransitionField
}

func (b writeBackend) apply(h *connectorHarness) {
	h.workflowStatuses = b.statuses
	h.transitionFields = b.fields
}

var doneResolutionScreen = map[string]map[string]jiraWireTransitionField{
	"Done": {"resolution": {Required: true, Name: "Resolution"}},
}

func (h *connectorHarness) issueRecord(num string) jiraIssueRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.issues[num]
}

// countAdmitted adds the audited requests of h's run to admitted, keyed by the
// "METHOD path" of the Route that admitted each.
func (h *connectorHarness) countAdmitted(t *testing.T, admitted map[string]int) {
	t.Helper()
	key := map[string]string{}
	for _, r := range h.routes {
		key[r.String()] = r.Method + " " + r.Path
	}
	for _, e := range h.auditEntries(t) {
		if e.Decision != connectorhost.DecisionSent {
			t.Errorf("audit entry %+v: want every request sent", e)
		}
		admitted[key[e.Route]]++
	}
	h.seenMu.Lock()
	defer h.seenMu.Unlock()
	if len(h.badReq) > 0 {
		t.Errorf("backend saw requests outside the Connector's Route table: %q", h.badReq)
	}
}

// The lifecycle writes (claim, transition, label fallback, verdict, label list)
// run through the compiled jira Connector against the contract, in each
// lifecycle: every decision is core's, the Connector only performs the
// primitive it is asked for.
func TestConnector_TrackerContract(t *testing.T) {
	if os.Getenv(connectorWasmEnv) == "" {
		t.Skipf("%s unset: no compiled jira Connector to test", connectorWasmEnv)
	}
	verdicts := forge.ResearchVerdictLabels()
	const issueType = "Task"
	full := `{"dispatchable":"To Do","inProgress":"In Progress","complete":{"status":"Done","resolution":"Fixed"},"failed":"Failed","ambiguous":"Needs Info"}`
	allStatuses := []string{"To Do", "In Progress", "Done", "Failed", "Needs Info"}

	modes := []struct {
		name      string
		mapping   string
		lifecycle string
		backend   writeBackend
		// mapped trackers claim by native status, which carries no InProgress
		// label for the verdict cases to guard; those run on research's tracker.
		mapped bool
	}{
		{name: "hybrid label-only", lifecycle: jira.LifecycleHybrid},
		{"hybrid full mapping", full, jira.LifecycleHybrid, writeBackend{allStatuses, doneResolutionScreen}, true},
		{"status", full, jira.LifecycleStatus, writeBackend{allStatuses, doneResolutionScreen}, true},
	}

	admitted := map[string]int{}
	for _, fl := range connectorFlavours {
		for _, m := range modes {
			t.Run(fl.name+"/"+m.name, func(t *testing.T) {
				work, err := connectorLifecycle(dispatchkind.TrackerWork, testLabels, verdicts, m.mapping, m.lifecycle, issueType)
				if err != nil {
					t.Fatal(err)
				}
				wh := newConnectorHarnessWith(t, fl.cloud, work)
				m.backend.apply(wh)
				if !m.mapped {
					forgetest.RunTrackerContract(t, connectorWriteHarness{wh})
					// The site-wide list is what a Box's label registry reads.
					if got, err := wh.tr.ListLabels(); err != nil || !slices.Contains(got, testLabels.Failed) {
						t.Errorf("ListLabels = %v, %v, want the labels the run left, including %q", got, err, testLabels.Failed)
					}
				} else {
					forgetest.RunTrackerWorkContract(t, connectorWriteHarness{wh})
					rec := wh.issueRecord("PROJ-101")
					if rec.status != "Done" || rec.resolution != "Fixed" {
						t.Errorf("PROJ-101 ended in status %q resolution %q, want Done / Fixed: the claim and completion must move natively",
							rec.status, rec.resolution)
					}
					if len(rec.labels) != 0 {
						t.Errorf("PROJ-101 labels = %v, want the dispatch labels stripped after the native moves", rec.labels)
					}
				}
				wh.countAdmitted(t, admitted)

				if m.mapped {
					research, err := connectorLifecycle(dispatchkind.TrackerResearch, testLabels, verdicts, m.mapping, m.lifecycle, issueType)
					if err != nil {
						t.Fatal(err)
					}
					rh := newConnectorHarnessWith(t, fl.cloud, research)
					m.backend.apply(rh)
					forgetest.RunTrackerVerdictContract(t, connectorWriteHarness{rh})
					rh.countAdmitted(t, admitted)
				}
			})
		}
	}

	for _, route := range []string{"PUT /rest/api/2/issue", "POST /rest/api/2/issue", "GET /rest/api/2/label"} {
		if admitted[route] == 0 {
			t.Errorf("route %s admitted no request across the write runs", route)
		}
	}
}

func TestConnector_ListLabelsServesTheSiteList(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarnessWith(t, fl.cloud, connectortracker.Lifecycle{Labels: testLabels})
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Labels: []string{"alpha", "beta"}})
			h.SeedIssue(forge.Issue{Number: "PROJ-2", Labels: []string{"beta", "gamma"}})
			got, err := h.tr.ListLabels()
			if err != nil {
				t.Fatalf("ListLabels: %v", err)
			}
			if want := []string{"alpha", "beta", "gamma"}; !slices.Equal(got, want) {
				t.Errorf("ListLabels = %v, want %v", got, want)
			}
		})
	}
}

// In status mode a transition the workflow does not offer is refused, naming
// what it does offer, rather than swapped for a label.
func TestConnector_StatusOnlyRefusesAnUnofferedTransition(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			lc, err := connectorLifecycle(dispatchkind.TrackerWork, testLabels, forge.ResearchVerdictLabels(),
				`{"dispatchable":"To Do","complete":"Done"}`, jira.LifecycleStatus, "Task")
			if err != nil {
				t.Fatal(err)
			}
			h := newConnectorHarnessWith(t, fl.cloud, lc)
			h.workflowStatuses = []string{"To Do"}
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t"})
			err = h.tr.TransitionState("PROJ-1", forge.InProgress, forge.Complete)
			var refused *forge.TransitionRefusedError
			if !errors.As(err, &refused) || refused.To != "Done" || !slices.Equal(refused.Offered, []string{"To Do"}) {
				t.Fatalf("TransitionState err = %v, want a TransitionRefusedError to Done offering [To Do]", err)
			}
			if got := h.issueRecord("PROJ-1").labels; len(got) != 0 {
				t.Errorf("labels = %v after a refused transition, want none", got)
			}
		})
	}
}

// In hybrid mode a mapped transition the workflow does not offer falls back to
// the state's label; the issue's native status is left where it was.
func TestConnector_HybridFallsBackToLabelWhenTransitionUnoffered(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			lc, err := connectorLifecycle(dispatchkind.TrackerWork, testLabels, forge.ResearchVerdictLabels(),
				`{"inProgress":"In Progress","failed":"Won't Do"}`, jira.LifecycleHybrid, "Task")
			if err != nil {
				t.Fatal(err)
			}
			h := newConnectorHarnessWith(t, fl.cloud, lc)
			h.workflowStatuses = []string{"In Progress"}
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t"})
			if err := h.tr.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress); err != nil {
				t.Fatalf("claim: %v", err)
			}
			if err := h.tr.TransitionState("PROJ-1", forge.InProgress, forge.Failed); err != nil {
				t.Fatalf("fail: %v", err)
			}
			rec := h.issueRecord("PROJ-1")
			if rec.status != "In Progress" || !slices.Equal(rec.labels, []string{testLabels.Failed}) {
				t.Errorf("status %q labels %v, want the native status kept and only %q labelled", rec.status, rec.labels, testLabels.Failed)
			}
		})
	}
}
