package jira_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

func useJiraRow(t *testing.T, adjust func(*backendrows.Row)) {
	t.Helper()
	row := backendrows.Row{
		Name:          "jira",
		Module:        filepath.Join(t.TempDir(), "missing.wasm"),
		Routes:        []connectorhost.Route{{Method: "GET", Host: "jira.example", Path: "/rest"}},
		CredentialEnv: "JIRA_TOKEN",
	}
	adjust(&row)
	doc, err := json.Marshal([]backendrows.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(doc, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func newJiraTracker(t *testing.T) error {
	t.Helper()
	t.Chdir(t.TempDir()) // the audit file lands under the host log dir
	env := map[string]string{"JIRA_BASE_URL": "https://jira.example", "JIRA_PROJECT_KEY": "PROJ", "JIRA_TOKEN": "tok"}
	_, err := jira.Strategy.NewIssueTracker(backendkit.Shared{}, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	return err
}

// Under primary the Connector is the only tracker: one that cannot load fails
// the constructor with its error rather than dropping to the native adapter.
func TestPrimaryRowFailsWithTheLoadErrorAndNeverFallsBackToNative(t *testing.T) {
	useJiraRow(t, func(r *backendrows.Row) { r.Primary = true })
	if err := newJiraTracker(t); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the module read error", err)
	}
}

func TestPlainRowKeepsTheNativeAdapter(t *testing.T) {
	useJiraRow(t, func(*backendrows.Row) {})
	if err := newJiraTracker(t); err != nil {
		t.Fatalf("a non-primary row must not load its Connector, got %v", err)
	}
}

// A route the op needs, left out of the row's table, is refused by the host
// before any request leaves, and the failure is the tracker's: there is no
// native adapter to answer instead.
func TestPrimaryRow_RefusedRouteFailsWithoutReachingTheBackend(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newProductionHarness(t, fl.cloud, dispatchkind.TrackerWork, "", jira.LifecycleHybrid, "Task", func(r *backendrows.Row) {
				r.Routes = slices.DeleteFunc(slices.Clone(r.Routes), func(rt connectorhost.Route) bool {
					return rt.Method == "GET" && rt.Path == "/rest/api/2/label"
				})
			})
			if _, err := h.tr.ListLabels(); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Fatalf("ListLabels err = %v, want ErrRouteRefused", err)
			}
			h.seenMu.Lock()
			defer h.seenMu.Unlock()
			if len(h.seen) != 0 {
				t.Errorf("backend saw %q, want nothing: the refused route must not be sent", h.seen)
			}
		})
	}
}

// #6378: a status-mode refusal reads the same from the Connector-backed tracker
// as from the native one, so the comment settle and reconcile post on the card
// (forge.CommentRefusal) is the same sentence.
func TestPrimaryRow_StatusOnlyRefusalMatchesNative(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done"}`
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newProductionHarness(t, fl.cloud, dispatchkind.TrackerWork, mapping, jira.LifecycleStatus, "Task")
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t"})
			h.mutate("PROJ-1", func(r *jiraIssueRecord) { r.status = "In Progress" })

			err := h.tr.TransitionState("PROJ-1", forge.InProgress, forge.Recoverable)
			var refused *forge.TransitionRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want *forge.TransitionRefusedError", err)
			}
			want := forge.TransitionRefusedError{Issue: "PROJ-1", From: "In Progress", State: "recoverable"}
			if refused.Issue != want.Issue || refused.From != want.From || refused.To != want.To || refused.State != want.State || refused.Offered != nil {
				t.Errorf("refusal = %+v, want %+v", refused, want)
			}
			text := `issue PROJ-1 could not move from status "In Progress": the tracker maps no status to state recoverable`
			if err.Error() != text {
				t.Errorf("Error() = %q, want %q", err.Error(), text)
			}
			if got := h.issueRecord("PROJ-1").labels; len(got) != 0 {
				t.Errorf("labels = %v after a refused move, want none", got)
			}

			if _, err := forge.CommentRefusal(h.tr, "PROJ-1", fmt.Errorf("wrapped: %w", err)); err != nil {
				t.Fatalf("CommentRefusal: %v", err)
			}
			stored := h.storedComments("PROJ-1")
			if len(stored) != 1 || stored[0].Body != "spindrift could not move this issue: "+text {
				t.Errorf("stored comments = %+v, want the one refusal sentence", stored)
			}
		})
	}
}

// #6370: on a site that maps failed and ambiguous to one status, the
// Connector-backed ListIssues(Failed) also returns the ambiguous-halted card,
// which never wears the failed label: the listing queue recover must skip it
// from (recover_queue_test.go's sharedFailedStatus fake simulates this).
// It pins the listing property that fake assumes, standing in for running
// recover's skip itself.
func TestPrimaryRow_SharedFailedStatusListsTheAmbiguousCardUnderFailed(t *testing.T) {
	const mapping = `{"failed":"Needs Info","ambiguous":"Needs Info"}`
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newProductionHarness(t, fl.cloud, dispatchkind.TrackerWork, mapping, jira.LifecycleHybrid, "Task")
			h.SeedIssue(forge.Issue{Number: "PROJ-41", Title: "halted by the gate"})
			h.mutate("PROJ-41", func(r *jiraIssueRecord) { r.status = "Needs Info" })
			h.SeedIssue(forge.Issue{Number: "PROJ-42", Title: "failed", Labels: []string{testLabels.Failed}})

			nums := func(state forge.DispatchState) []string {
				got, err := h.tr.ListIssues(state)
				if err != nil {
					t.Fatalf("ListIssues(%v): %v", state, err)
				}
				var out []string
				for _, iss := range got {
					out = append(out, iss.Number)
					if iss.Number == "PROJ-41" && slices.Contains(iss.Labels, testLabels.Failed) {
						t.Errorf("ListIssues(%v): PROJ-41 wears the failed label %v", state, iss.Labels)
					}
				}
				return slices.Sorted(slices.Values(out))
			}
			if got, want := nums(forge.Failed), []string{"PROJ-41", "PROJ-42"}; !slices.Equal(got, want) {
				t.Errorf("ListIssues(Failed) = %v, want %v: the shared status lists the ambiguous card", got, want)
			}
			if got, want := nums(forge.Ambiguous), []string{"PROJ-41"}; !slices.Equal(got, want) {
				t.Errorf("ListIssues(Ambiguous) = %v, want %v", got, want)
			}
		})
	}
}

// A claim whose target status the workflow does not offer is refused with the
// native adapter's sentence (#6378), and the claimer's refusal comment
// (forge.CommentRefusal) lands on the card, which a refused status-mode claim
// leaves no label on. The jira fileset holds no waves, whose once-per-text
// dedup is tracker-agnostic and tested there.
func TestPrimaryRow_RefusedStatusClaimMatchesNativeAndIsCommented(t *testing.T) {
	const mapping = `{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done"}`
	const text = `issue PROJ-1 could not move from status "To Do" to "In Progress": the workflow offers transitions to "To Do"`
	for _, b := range connectorBuilds {
		for _, fl := range connectorFlavours {
			t.Run(b.name+"/"+fl.name, func(t *testing.T) {
				h := b.harness(t, fl.cloud, dispatchkind.TrackerWork, mapping, jira.LifecycleStatus, "Task")
				h.workflowStatuses = []string{"To Do"}
				h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t"})
				h.mutate("PROJ-1", func(r *jiraIssueRecord) { r.status = "To Do" })

				err := h.tr.TransitionState("PROJ-1", forge.Dispatchable, forge.InProgress)
				var refused *forge.TransitionRefusedError
				if !errors.As(err, &refused) {
					t.Fatalf("err = %v, want *forge.TransitionRefusedError", err)
				}
				if refused.To != "In Progress" || !slices.Equal(refused.Offered, []string{"To Do"}) {
					t.Errorf("refusal = %+v, want a move to In Progress offering [To Do]", refused)
				}
				if err.Error() != text {
					t.Errorf("Error() = %q, want %q", err.Error(), text)
				}
				if got := h.issueRecord("PROJ-1").labels; len(got) != 0 {
					t.Errorf("labels = %v after a refused claim, want none", got)
				}

				if _, cerr := forge.CommentRefusal(h.tr, "PROJ-1", err); cerr != nil {
					t.Fatalf("CommentRefusal: %v", cerr)
				}
				stored := h.storedComments("PROJ-1")
				if len(stored) != 1 || stored[0].Body != "spindrift could not move this issue: "+text {
					t.Errorf("stored comments = %+v, want the one refusal sentence", stored)
				}
			})
		}
	}
}

// Jira's native links are authoritative, so under primary a body that merely
// says "Blocked by #12" adds no dependency and a failed native read is the
// answer, as BodyGrammarNativeDepsOnly makes it for the native adapter.
func TestPrimaryRow_DependenciesAreNativeOnly(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newProductionHarness(t, fl.cloud, dispatchkind.TrackerWork, "", jira.LifecycleHybrid, "Task")
			h.SeedIssue(forge.Issue{Number: "PROJ-1", Title: "t", Body: "Blocked by #12"})
			if g, ok := forge.As[forge.BodyGrammarer](h.tr); !ok || g.BodyGrammar() != forge.BodyGrammarNativeDepsOnly {
				t.Fatalf("BodyGrammarer = %v, %v, want BodyGrammarNativeDepsOnly", g, ok)
			}
			if deps, err := forge.DepsOf(h.tr, "PROJ-1"); err != nil || len(deps) != 0 {
				t.Errorf("DepsOf = %v, %v, want none: the body's reference is not a Jira link", deps, err)
			}

			h.FailNativeDeps("PROJ-1")
			if deps, err := forge.DepsOf(h.tr, "PROJ-1"); err == nil {
				t.Errorf("DepsOf = %v, nil, want the native read's error, not a body fallback", deps)
			}
		})
	}
}
