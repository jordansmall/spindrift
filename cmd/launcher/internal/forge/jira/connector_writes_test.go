package jira_test

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
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

// writeLabels adds the ambiguous label the shared testLabels lack, so the
// ambiguous cases can fall back to it.
var writeLabels = func() forge.DispatchLabels {
	l := testLabels
	l.Ambiguous = "agent-ambiguous-spec"
	return l
}()

// transitionCase is one TransitionState scenario run against the Connector in
// each flavour: the lifecycle and workflow it runs under, the issue it starts
// from, and what the backend and the error must show afterwards.
type transitionCase struct {
	name      string
	mapping   string
	lifecycle string
	backend   writeBackend
	// the seeded issue; status defaults to "To Do"
	status string
	labels []string
	from   forge.DispatchState
	to     forge.DispatchState

	failIssueRead   bool
	failTransitions bool

	// wantErr, when set, are substrings of a plain (non-refusal, non-claim) error
	wantErr     []string
	wantClaimed bool
	wantRefusal *forge.TransitionRefusedError

	wantStatus       string
	wantResolution   string
	wantLabels       []string
	wantPosts        int
	wantLabelsAtPost []string // checked only when wantPosts > 0
}

func sorted(s []string) []string { return slices.Sorted(slices.Values(s)) }

func transitionCases() []transitionCase {
	doneOnly := writeBackend{statuses: []string{"Done"}}
	inProgressOnly := writeBackend{statuses: []string{"In Progress"}}
	const inProg = "agent-in-progress"
	return []transitionCase{
		{
			name: "string mapping posts no resolution", mapping: `{"complete":"Done"}`, lifecycle: jira.LifecycleHybrid,
			backend: doneOnly, status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Complete,
			wantStatus: "Done", wantPosts: 1, wantLabelsAtPost: []string{inProg},
		},
		{
			name: "object mapping sends its resolution", mapping: `{"complete":{"status":"Done","resolution":"Won't Do"}}`, lifecycle: jira.LifecycleHybrid,
			backend: writeBackend{[]string{"Done"}, doneResolutionScreen}, status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Complete,
			wantStatus: "Done", wantResolution: "Won't Do", wantPosts: 1, wantLabelsAtPost: []string{inProg},
		},
		{
			name: "resolution without a screen field is refused, not swapped for a label", mapping: `{"complete":{"status":"Done","resolution":"Won't Do"}}`, lifecycle: jira.LifecycleHybrid,
			backend: doneOnly, status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Complete,
			wantErr:    []string{"Done", "PROJ-1", "Won't Do", "no resolution field"},
			wantStatus: "In Progress", wantLabels: []string{inProg},
		},
		{
			name: "a required field with no value is refused, not swapped for a label", mapping: `{"complete":"Done"}`, lifecycle: jira.LifecycleHybrid,
			backend: writeBackend{[]string{"Done"}, map[string]map[string]jiraWireTransitionField{"Done": {
				"resolution":        {Required: true, Name: "Resolution"},
				"customfield_10001": {Required: true},
			}}},
			status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Complete,
			wantErr:    []string{"Resolution (resolution)", "customfield_10001", "Done", "PROJ-1"},
			wantStatus: "In Progress", wantLabels: []string{inProg},
		},
		{
			name: "a required field with a default does not block", mapping: `{"complete":"Done"}`, lifecycle: jira.LifecycleHybrid,
			backend: writeBackend{[]string{"Done"}, map[string]map[string]jiraWireTransitionField{"Done": {
				"resolution": {Required: true, HasDefaultValue: true, Name: "Resolution"},
				"labels":     {Name: "Labels"},
			}}},
			status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Complete,
			wantStatus: "Done", wantPosts: 1, wantLabelsAtPost: []string{inProg},
		},
		{
			name: "a native claim strips settled labels before it transitions", mapping: `{"inProgress":"In Progress"}`, lifecycle: jira.LifecycleHybrid,
			backend: inProgressOnly, labels: []string{"agent-failed", "agent-complete", "ready-for-agent", "unrelated"},
			from: forge.Dispatchable, to: forge.InProgress,
			wantStatus: "In Progress", wantLabels: []string{"unrelated"}, wantPosts: 1,
			wantLabelsAtPost: []string{"ready-for-agent", "unrelated"},
		},
		{
			name: "hybrid honours a native In Progress status as claimed", mapping: `{"dispatchable":"To Do","inProgress":"In Progress"}`, lifecycle: jira.LifecycleHybrid,
			backend: inProgressOnly, status: "In Progress", from: forge.Dispatchable, to: forge.InProgress,
			wantClaimed: true, wantStatus: "In Progress",
		},
		{
			name: "status mode honours a native In Progress status as claimed", mapping: `{"dispatchable":"To Do","inProgress":"In Progress"}`, lifecycle: jira.LifecycleStatus,
			backend: inProgressOnly, status: "In Progress", from: forge.Dispatchable, to: forge.InProgress,
			wantClaimed: true, wantStatus: "In Progress",
		},
		{
			name: "hybrid honours the in-progress label as claimed", mapping: `{"dispatchable":"To Do","inProgress":"In Progress"}`, lifecycle: jira.LifecycleHybrid,
			backend: inProgressOnly, labels: []string{inProg}, from: forge.Dispatchable, to: forge.InProgress,
			wantClaimed: true, wantStatus: "To Do", wantLabels: []string{inProg},
		},
		{
			name: "status mode ignores a stale in-progress label", mapping: `{"dispatchable":"To Do","inProgress":"In Progress"}`, lifecycle: jira.LifecycleStatus,
			backend: inProgressOnly, labels: []string{inProg}, from: forge.Dispatchable, to: forge.InProgress,
			wantStatus: "In Progress", wantLabels: []string{inProg}, wantPosts: 1, wantLabelsAtPost: []string{inProg},
		},
		{
			name: "a mapped ambiguous state transitions", mapping: `{"ambiguous":"Needs Spec"}`, lifecycle: jira.LifecycleHybrid,
			backend: writeBackend{statuses: []string{"Needs Spec"}}, status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Ambiguous,
			wantStatus: "Needs Spec", wantPosts: 1, wantLabelsAtPost: []string{inProg},
		},
		{
			name: "an unmapped ambiguous state falls back to its label", mapping: ``, lifecycle: jira.LifecycleHybrid,
			status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Ambiguous,
			wantStatus: "In Progress", wantLabels: []string{"agent-ambiguous-spec"},
		},
		{
			name: "an unavailable ambiguous transition falls back to its label", mapping: `{"ambiguous":"Needs Spec"}`, lifecycle: jira.LifecycleHybrid,
			backend: writeBackend{statuses: []string{"Backlog"}}, status: "In Progress", labels: []string{inProg}, from: forge.InProgress, to: forge.Ambiguous,
			wantStatus: "In Progress", wantLabels: []string{"agent-ambiguous-spec"},
		},
		{
			name: "a failing transition list is an error, not a label swap", mapping: `{"inProgress":"In Progress"}`, lifecycle: jira.LifecycleHybrid,
			failTransitions: true, from: forge.Dispatchable, to: forge.InProgress,
			wantErr: []string{"500"}, wantStatus: "To Do",
		},
		{
			name: "status mode refuses an unmapped target with no label edit", mapping: `{"dispatchable":"To Do","inProgress":"In Progress"}`, lifecycle: jira.LifecycleStatus,
			backend: inProgressOnly, status: "In Progress", from: forge.InProgress, to: forge.Recoverable,
			wantRefusal: &forge.TransitionRefusedError{Issue: "PROJ-1", From: "In Progress", State: "recoverable"},
			wantStatus:  "In Progress",
		},
		{
			name: "a refusal carries the status the issue sat in", mapping: `{"dispatchable":"To Do","complete":"Done"}`, lifecycle: jira.LifecycleStatus,
			backend: writeBackend{statuses: []string{"Blocked", "To Do"}}, status: "In Review", from: forge.InProgress, to: forge.Complete,
			wantRefusal: &forge.TransitionRefusedError{Issue: "PROJ-1", From: "In Review", To: "Done", Offered: []string{"Blocked", "To Do"}},
			wantStatus:  "In Review",
		},
		{
			name: "a refusal survives a failed status read", mapping: `{"dispatchable":"To Do","complete":"Done"}`, lifecycle: jira.LifecycleStatus,
			backend: writeBackend{statuses: []string{"Blocked"}}, status: "In Review", failIssueRead: true, from: forge.InProgress, to: forge.Complete,
			wantRefusal: &forge.TransitionRefusedError{Issue: "PROJ-1", To: "Done", Offered: []string{"Blocked"}},
			wantStatus:  "In Review",
		},
	}
}

// The native adapter's status-only and transition cases, run against the
// Connector: every decision is core's, so each outcome must match.
func TestConnector_TransitionCases(t *testing.T) {
	for _, fl := range connectorFlavours {
		for _, tc := range transitionCases() {
			t.Run(fl.name+"/"+tc.name, func(t *testing.T) {
				lc, err := connectorLifecycle(dispatchkind.TrackerWork, writeLabels, forge.ResearchVerdictLabels(), tc.mapping, tc.lifecycle, "Task")
				if err != nil {
					t.Fatal(err)
				}
				h := newConnectorHarnessWith(t, fl.cloud, lc)
				tc.backend.apply(h)
				h.transitionsFail = tc.failTransitions
				h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t", Labels: tc.labels})
				status := tc.status
				if status == "" {
					status = "To Do"
				}
				h.mutate("PROJ-1", func(r *jiraIssueRecord) { r.status = status })
				if tc.failIssueRead {
					h.FailNativeDeps("PROJ-1") // fails the issue read
				}

				err = h.tr.TransitionState("PROJ-1", tc.from, tc.to)
				switch {
				case tc.wantRefusal != nil:
					var got *forge.TransitionRefusedError
					if !errors.As(err, &got) {
						t.Fatalf("err = %v, want a *forge.TransitionRefusedError", err)
					}
					if !reflect.DeepEqual(got, tc.wantRefusal) {
						t.Errorf("refusal = %+v, want %+v", got, tc.wantRefusal)
					}
				case tc.wantClaimed:
					if !errors.Is(err, forge.ErrAlreadyClaimed) {
						t.Fatalf("err = %v, want forge.ErrAlreadyClaimed", err)
					}
				case tc.wantErr != nil:
					if err == nil {
						t.Fatal("want an error")
					}
					var refused *forge.TransitionRefusedError
					if errors.As(err, &refused) || errors.Is(err, forge.ErrAlreadyClaimed) {
						t.Fatalf("err = %v, want a plain error", err)
					}
					for _, want := range tc.wantErr {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q does not name %q", err, want)
						}
					}
				case err != nil:
					t.Fatalf("TransitionState: %v", err)
				}

				rec := h.issueRecord("PROJ-1")
				if rec.status != tc.wantStatus || rec.resolution != tc.wantResolution || rec.posts != tc.wantPosts {
					t.Errorf("status %q resolution %q posts %d, want %q / %q / %d",
						rec.status, rec.resolution, rec.posts, tc.wantStatus, tc.wantResolution, tc.wantPosts)
				}
				if !slices.Equal(sorted(rec.labels), sorted(tc.wantLabels)) {
					t.Errorf("labels = %v, want %v", rec.labels, tc.wantLabels)
				}
				if tc.wantPosts > 0 && !slices.Equal(sorted(rec.labelsAtPost), sorted(tc.wantLabelsAtPost)) {
					t.Errorf("labels at the transition = %v, want %v", rec.labelsAtPost, tc.wantLabelsAtPost)
				}
			})
		}
	}
}

// A transition whose mapping is unavailable gives core the same outcome through
// the Connector as through the native adapter: hybrid falls back to the label,
// status refuses with what the workflow offers.
func TestConnector_UnavailableTransitionMatchesNative(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","complete":"Done"}`
	for _, fl := range connectorFlavours {
		for _, lifecycle := range []string{jira.LifecycleHybrid, jira.LifecycleStatus} {
			t.Run(fl.name+"/"+lifecycle, func(t *testing.T) {
				lc, err := connectorLifecycle(dispatchkind.TrackerWork, testLabels, forge.ResearchVerdictLabels(), mapping, lifecycle, "Task")
				if err != nil {
					t.Fatal(err)
				}
				h := newConnectorHarnessWith(t, fl.cloud, lc)
				h.workflowStatuses = []string{"Blocked", "To Do"}
				targets, err := jira.ParseStatusMapping(mapping)
				if err != nil {
					t.Fatal(err)
				}
				email := ""
				if fl.cloud {
					email = connectorEmail
				}
				native := jira.NewJiraClient(jira.JiraConfig{
					BaseURL:       h.srv.URL,
					ProjectKey:    connectorProject,
					Email:         email,
					Token:         connectorToken,
					Labels:        testLabels,
					StatusMapping: targets,
					StatusOnly:    lifecycle == jira.LifecycleStatus,
					HTTPClient:    h.srv.Client(),
				})

				// The same issue per side, reseeded identically between them.
				sides := []struct {
					name string
					key  string
					tr   forge.IssueTracker
				}{{"native", "PROJ-1", native}, {"connector", "PROJ-1", h.tr}}
				outcomes := map[string]struct {
					err    error
					labels []string
				}{}
				for _, s := range sides {
					h.SeedIssue(forge.Issue{Number: s.key, Title: "t", Labels: []string{testLabels.InProgress}})
					h.mutate(s.key, func(r *jiraIssueRecord) { r.status = "In Review" })
					err := s.tr.TransitionState(s.key, forge.InProgress, forge.Complete)
					outcomes[s.name] = struct {
						err    error
						labels []string
					}{err, sorted(h.issueRecord(s.key).labels)}
					if rec := h.issueRecord(s.key); rec.status != "In Review" {
						t.Errorf("%s moved the issue to %q, want it left in In Review", s.name, rec.status)
					}
				}

				n, c := outcomes["native"], outcomes["connector"]
				if !slices.Equal(n.labels, c.labels) {
					t.Errorf("labels: native %v, connector %v", n.labels, c.labels)
				}
				if lifecycle == jira.LifecycleHybrid {
					if n.err != nil || c.err != nil {
						t.Fatalf("hybrid errors: native %v, connector %v, want none", n.err, c.err)
					}
					if want := []string{testLabels.Complete}; !slices.Equal(c.labels, want) {
						t.Errorf("labels = %v, want the fallback %v", c.labels, want)
					}
					return
				}
				var nRef, cRef *forge.TransitionRefusedError
				if !errors.As(n.err, &nRef) || !errors.As(c.err, &cRef) {
					t.Fatalf("status errors: native %v, connector %v, want both a TransitionRefusedError", n.err, c.err)
				}
				if !reflect.DeepEqual(nRef, cRef) || cRef.From != "In Review" || cRef.To != "Done" || !slices.Equal(cRef.Offered, []string{"Blocked", "To Do"}) {
					t.Errorf("refusals differ or are incomplete: native %+v, connector %+v", nRef, cRef)
				}
				if !slices.Equal(c.labels, []string{testLabels.InProgress}) {
					t.Errorf("labels = %v, want the seeded label untouched", c.labels)
				}
			})
		}
	}
}
