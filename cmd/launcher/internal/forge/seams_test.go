package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// --- DispatchLabels tests ---

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix and pinned against the agent workflows by
// nix/checks/dispatch-labels.nix (issue #460). NewFake and the production
// adapters (Exec, Local, Jira) take labels as an explicit constructor
// argument rather than baking in a copy, so forge_test shares this one value
// instead of each test restating the four label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

func TestDispatchLabels_Label(t *testing.T) {
	d := testLabels
	if got := d.Label(forge.Dispatchable); got != "ready-for-agent" {
		t.Errorf("Label(Dispatchable) = %q", got)
	}
	if got := d.Label(forge.InProgress); got != "agent-in-progress" {
		t.Errorf("Label(InProgress) = %q", got)
	}
	if got := d.Label(forge.Complete); got != "agent-complete" {
		t.Errorf("Label(Complete) = %q", got)
	}
	if got := d.Label(forge.Failed); got != "agent-failed" {
		t.Errorf("Label(Failed) = %q", got)
	}
}

func TestDispatchLabels_AllLabels(t *testing.T) {
	d := testLabels
	all := d.AllLabels()
	if len(all) != 4 {
		t.Fatalf("AllLabels len = %d, want 4", len(all))
	}
}

// --- IssueTracker / CodeForge type-seam tests ---

// TestFake_ImplementsIssueTracker asserts that *Fake satisfies IssueTracker.
func TestFake_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = forge.NewFake()
}

// TestFake_ImplementsCodeForge asserts that *Fake satisfies CodeForge.
func TestFake_ImplementsCodeForge(t *testing.T) {
	var _ forge.CodeForge = forge.NewFake()
}

// TestFake_ImplementsPRForge asserts that *Fake satisfies the optional
// PRForge surface, matching the github adapter's shape.
func TestFake_ImplementsPRForge(t *testing.T) {
	var _ forge.PRForge = forge.NewFake()
}

// TestFake_AsPushOnly_HidesPRForge verifies that AsPushOnly wraps a Fake so a
// type assertion against PRForge reports absence, matching the git adapter's
// shape — the mechanism Settle uses in place of the removed PushOnly() flag.
func TestFake_AsPushOnly_HidesPRForge(t *testing.T) {
	f := forge.NewFake()
	cf := f.AsPushOnly()
	if _, ok := cf.(forge.PRForge); ok {
		t.Error("AsPushOnly() satisfies PRForge, want it hidden")
	}
}

// --- TransitionState tests ---

func TestFake_TransitionState_DispatchableToInProgress(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	if err := f.TransitionState("42", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-in-progress") {
		t.Error("want agent-in-progress label, not present")
	}
	if containsLabel(iss.Labels, "ready-for-agent") {
		t.Error("want ready-for-agent removed, still present")
	}
	if len(f.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionStateCall, got %d", len(f.TransitionStateCalls))
	}
	call := f.TransitionStateCalls[0]
	if call.Num != "42" || call.From != forge.Dispatchable || call.To != forge.InProgress {
		t.Errorf("unexpected call: %+v", call)
	}
}

func TestFake_TransitionState_InProgressToComplete(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{Number: "7", Labels: []string{"agent-in-progress"}})

	if err := f.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	iss, err := f.Issue("7")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Error("want agent-complete label after Complete transition")
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Error("want agent-in-progress removed after Complete transition")
	}
}

func TestFake_TransitionState_MissingIssueIsNoOp(t *testing.T) {
	f := forge.NewFake()
	// Best-effort: unknown issue number must not error.
	if err := f.TransitionState("999", forge.InProgress, forge.Failed); err != nil {
		t.Fatalf("TransitionState on missing issue: %v", err)
	}
}

func TestFake_TransitionState_Err(t *testing.T) {
	f := forge.NewFake()
	f.TransitionStateErr = forge.ErrAuthFailure

	err := f.TransitionState("1", forge.Dispatchable, forge.InProgress)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// --- CompleteVerdict tests ---

// researchLabels mirrors ResearchDispatchLabels/ResearchVerdictLabels so
// research-kind Fake tests don't restate the label strings.
var researchLabels = forge.ResearchDispatchLabels()
var researchVerdictLabels = forge.ResearchVerdictLabels()

func TestFake_CompleteVerdict_SwapsInProgressForVerdictLabel(t *testing.T) {
	f := forge.NewFake(researchLabels)
	f.VerdictLabels = researchVerdictLabels
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-in-progress"}})

	if err := f.CompleteVerdict("42", forge.Recommend); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research-recommend") {
		t.Error("want agent-research-recommend label, not present")
	}
	if containsLabel(iss.Labels, "agent-research-in-progress") {
		t.Error("want agent-research-in-progress removed, still present")
	}
	if len(f.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdictCall, got %d", len(f.CompleteVerdictCalls))
	}
	call := f.CompleteVerdictCalls[0]
	if call.Num != "42" || call.Verdict != forge.Recommend {
		t.Errorf("unexpected call: %+v", call)
	}
}

func TestFake_CompleteVerdict_MissingInProgressErrors(t *testing.T) {
	f := forge.NewFake(researchLabels)
	f.VerdictLabels = researchVerdictLabels
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"agent-research-recommend"}})

	if err := f.CompleteVerdict("42", forge.Recommend); err == nil {
		t.Fatal("want error when issue lacks InProgress label, got nil")
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research-recommend") {
		t.Error("want agent-research-recommend label untouched, missing")
	}
	if len(f.CompleteVerdictCalls) != 1 {
		t.Fatalf("want 1 CompleteVerdictCall recorded even on error, got %d", len(f.CompleteVerdictCalls))
	}
}

// --- ListIssues(DispatchState) tests ---

func TestFake_ListIssues_ByDispatchState(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{Number: "1", State: "OPEN", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "2", State: "OPEN", Labels: []string{"agent-in-progress"}})
	f.SetIssue(forge.Issue{Number: "3", State: "OPEN", Labels: []string{"ready-for-agent"}})

	dispatchable, err := f.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(dispatchable) != 2 {
		t.Fatalf("want 2 Dispatchable issues, got %d: %+v", len(dispatchable), dispatchable)
	}
	// Canonical order: ascending number.
	if dispatchable[0].Number != "1" || dispatchable[1].Number != "3" {
		t.Errorf("wrong order: %v", dispatchable)
	}

	inProg, err := f.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues(InProgress): %v", err)
	}
	if len(inProg) != 1 || inProg[0].Number != "2" {
		t.Fatalf("want [#2] for InProgress, got %+v", inProg)
	}
}

// --- ListOpenIssues tests ---

// TestFake_ListOpenIssues_AllStatesAscendingExcludesClosed verifies
// ListOpenIssues returns every open issue regardless of dispatch label
// (including one with none at all), ascending by number, and skips closed
// issues — the full backlog the Console browses, unlike ListIssues which
// filters to a single dispatch state.
func TestFake_ListOpenIssues_AllStatesAscendingExcludesClosed(t *testing.T) {
	f := forge.NewFake(testLabels)
	f.SetIssue(forge.Issue{Number: "3", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "1", State: forge.IssueOpen, Labels: []string{"agent-in-progress"}})
	f.SetIssue(forge.Issue{Number: "2", State: forge.IssueOpen}) // untriaged: no dispatch label
	f.SetIssue(forge.Issue{Number: "9", State: forge.IssueClosed, Labels: []string{"agent-complete"}})

	issues, err := f.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("want 3 open issues, got %d: %+v", len(issues), issues)
	}
	if issues[0].Number != "1" || issues[1].Number != "2" || issues[2].Number != "3" {
		t.Errorf("wrong order: %v", issues)
	}
}

// --- DepsOf tests ---

func TestFake_DepsOf_ParsesBody(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{
		Number: "10",
		Body:   "This issue depends on #3 and #5.",
	})

	deps, err := f.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("want 2 deps, got %d: %v", len(deps), deps)
	}
	found3, found5 := false, false
	for _, d := range deps {
		if d.Source != forge.DepSourceBody {
			t.Errorf("want DepSourceBody for %v, got %v", d, d.Source)
		}
		if d.ID == "3" {
			found3 = true
		}
		if d.ID == "5" {
			found5 = true
		}
	}
	if !found3 || !found5 {
		t.Errorf("want [3 5], got %v", deps)
	}
}

func TestFake_DepsOf_EmptyBody(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "11", Body: "No deps here."})

	deps, err := f.DepsOf("11")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("want no deps, got %v", deps)
	}
}

func TestFake_DepsOf_MissingIssue(t *testing.T) {
	f := forge.NewFake()
	_, err := f.DepsOf("999")
	if err == nil {
		t.Fatal("want error for unknown issue, got nil")
	}
}

// --- TouchesOf tests ---

func TestFake_TouchesOf_ParsesBody(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix\n- cmd/launcher/*.go",
	})

	touches, err := f.TouchesOf("10")
	if err != nil {
		t.Fatalf("TouchesOf: %v", err)
	}
	want := []string{"lib/env-schema.nix", "cmd/launcher/*.go"}
	if len(touches) != len(want) || touches[0] != want[0] || touches[1] != want[1] {
		t.Errorf("TouchesOf = %v, want %v", touches, want)
	}
}

// --- ParseBlockerRefs tests (moved from main package) ---

func TestParseBlockerRefs_Empty(t *testing.T) {
	refs := forge.ParseBlockerRefs("")
	if len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_NoRefs(t *testing.T) {
	refs := forge.ParseBlockerRefs("This is just a regular issue body with no blockers.")
	if len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_DependsOn(t *testing.T) {
	refs := forge.ParseBlockerRefs("This issue depends on #12 to work correctly.")
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12], got %v", refs)
	}
}

func TestParseBlockerRefs_BlockedBy(t *testing.T) {
	refs := forge.ParseBlockerRefs("blocked by #1")
	if len(refs) != 1 || refs[0] != "1" {
		t.Errorf("expected [1], got %v", refs)
	}
}

func TestParseBlockerRefs_CaseInsensitive(t *testing.T) {
	refs := forge.ParseBlockerRefs("DEPENDS ON #5")
	if len(refs) != 1 || refs[0] != "5" {
		t.Errorf("expected [5], got %v", refs)
	}

	refs2 := forge.ParseBlockerRefs("Blocked By #7")
	if len(refs2) != 1 || refs2[0] != "7" {
		t.Errorf("expected [7], got %v", refs2)
	}
}

func TestParseBlockerRefs_MultipleRefsOnOneLine(t *testing.T) {
	refs := forge.ParseBlockerRefs("blocked by #12 and #13")
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %v", refs)
	}
	found12, found13 := false, false
	for _, r := range refs {
		if r == "12" {
			found12 = true
		}
		if r == "13" {
			found13 = true
		}
	}
	if !found12 || !found13 {
		t.Errorf("expected [12 13], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderListFormat(t *testing.T) {
	body := "## Blocked by\n- #56 (some issue)\n- #57"
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %v", refs)
	}
	found56, found57 := false, false
	for _, r := range refs {
		if r == "56" {
			found56 = true
		}
		if r == "57" {
			found57 = true
		}
	}
	if !found56 || !found57 {
		t.Errorf("expected [56 57], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderListWithColon(t *testing.T) {
	body := "## Blocked by:\n- #3\n- #4"
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %v", refs)
	}
	found3, found4 := false, false
	for _, r := range refs {
		if r == "3" {
			found3 = true
		}
		if r == "4" {
			found4 = true
		}
	}
	if !found3 || !found4 {
		t.Errorf("expected [3 4], got %v", refs)
	}
}

func TestParseBlockerRefs_HeaderSectionEndsOnNextHeading(t *testing.T) {
	body := "## Blocked by\n- #1\n## Other section\n- #2"
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "1" {
		t.Errorf("expected [1], got %v", refs)
	}
}

func TestParseBlockerRefs_NoDuplicates(t *testing.T) {
	refs := forge.ParseBlockerRefs("depends on #5 and blocked by #5")
	if len(refs) != 1 || refs[0] != "5" {
		t.Errorf("expected [5] (deduplicated), got %v", refs)
	}
}

func TestParseBlockerRefs_SeeAlsoDoesNotBleed(t *testing.T) {
	refs := forge.ParseBlockerRefs("depends on #12. See also #99")
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12] only (not #99), got %v", refs)
	}
}

func TestParseBlockerRefs_IgnoresTriggerInFencedBlock(t *testing.T) {
	body := "```\nblocked by #99\n```"
	if refs := forge.ParseBlockerRefs(body); len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_IgnoresHeaderSectionInFencedBlock(t *testing.T) {
	body := "```\n## Blocked by\n- #1\n```\n## Blocked by\n- #2"
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "2" {
		t.Errorf("expected [2] only, got %v", refs)
	}
}

func TestParseBlockerRefs_IgnoresTriggerInTildeFencedBlock(t *testing.T) {
	body := "~~~\nblocked by #99\n~~~"
	if refs := forge.ParseBlockerRefs(body); len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

func TestParseBlockerRefs_IgnoresTriggerInInlineCodeSpan(t *testing.T) {
	body := "This quotes `blocked by #99` mid-sentence, but depends on #12 is real."
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12] only, got %v", refs)
	}
}

func TestParseBlockerRefs_MultipleInlineSpansOnOneLine(t *testing.T) {
	body := "depends on #12, not `blocked by #98` nor `blocked by #97`, see also #99"
	refs := forge.ParseBlockerRefs(body)
	if len(refs) != 1 || refs[0] != "12" {
		t.Errorf("expected [12] only, got %v", refs)
	}
}

// TestParseBlockerRefs_Issue847FencedExampleDoesNotWedge reconstructs the
// #847 leak: a real "## Blocked by" section reads None, but a fenced
// example of the dispatch log format quotes "blocked by #N" lines that the
// inline keyword matcher used to capture as real refs.
func TestParseBlockerRefs_Issue847FencedExampleDoesNotWedge(t *testing.T) {
	body := "## Blocked by\nNone\n\nExample dispatch log:\n```\n~~ #12 blocked by #13; skipping\n~~ #12 blocked by #14; skipping\n```\n"
	if refs := forge.ParseBlockerRefs(body); len(refs) != 0 {
		t.Errorf("expected [], got %v", refs)
	}
}

// containsLabel is a test helper (not imported from main package).
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}
