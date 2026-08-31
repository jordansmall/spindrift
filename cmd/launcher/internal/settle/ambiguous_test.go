package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// ambiguousDispatchLabels extends testDispatchLabels with the Ambiguous
// label (issue #2275) — a separate var rather than mutating the shared one,
// since testDispatchLabels' own doc comment pins it at the conventional
// four-label lifecycle set most other settle tests exercise.
var ambiguousDispatchLabels = forge.DispatchLabels{
	Dispatchable: testDispatchLabels.Dispatchable,
	InProgress:   testDispatchLabels.InProgress,
	Complete:     testDispatchLabels.Complete,
	Failed:       testDispatchLabels.Failed,
	Ambiguous:    "agent-ambiguous-spec",
}

// TestSettle_AmbiguousOutcome_PostsNoteAndTransitions verifies that a
// status=ambiguous outcome (issue #2275: the Box halted before
// scouting/implementing because the issue's title/body describe materially
// unrelated work) posts o.Note as a comment unconditionally — unlike
// postBlockedNoteComment, which only fires under s.landing != nil ||
// s.readOnly — and transitions the issue from InProgress to Ambiguous
// (agent-ambiguous-spec), not agent-failed.
func TestSettle_AmbiguousOutcome_PostsNoteAndTransitions(t *testing.T) {
	const issNum = "42"
	const note = "Issue title/body describe unrelated work: title says X, body describes Y."

	fc := forge.NewFake(ambiguousDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: "ambiguous", Note: note},
		},
	}

	// A read-write github-shaped tracker (no landing recorder, not
	// read-only) is exactly the s.landing == nil && !s.readOnly case that
	// would suppress postBlockedNoteComment — the ambiguous comment must
	// still post here, confirming the new case is unconditional.
	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	// Settle always posts a second, separate usage-report comment
	// (postUsageComment) after the switch, on every status alike — so the
	// ambiguous-note comment is the first of two, not the only one.
	if len(fc.CommentCalls) != 2 {
		t.Fatalf("want 2 comments posted (ambiguous note + usage report), got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != note {
		t.Errorf("first comment body: got %q, want %q", fc.CommentCalls[0].Body, note)
	}

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Ambiguous {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Ambiguous", call, issNum)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-ambiguous-spec") {
		t.Errorf("ambiguous outcome must apply agent-ambiguous-spec; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("ambiguous outcome must never fall through to agent-failed; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("ambiguous outcome must remove agent-in-progress; got labels=%v", iss.Labels)
	}
}

// TestSettle_AmbiguousOutcome_EmptyNoteSkipsComment verifies the case="" note
// guard: an ambiguous outcome with no note posts no *ambiguous-note* comment
// (matching the `if o.Note != ""` gate — postUsageComment's own comment
// still posts, unconditionally, after the switch) but still transitions
// state, so a malformed/empty note never blocks the label swap.
func TestSettle_AmbiguousOutcome_EmptyNoteSkipsComment(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(ambiguousDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: "ambiguous", Note: ""},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	// The `if o.Note != ""` guard skips only the ambiguous-note comment —
	// postUsageComment's own comment still posts unconditionally after the
	// switch, so exactly 1 (not 0) comment is expected here.
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted (usage report only, note skipped), got %d", len(fc.CommentCalls))
	}

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Ambiguous {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Ambiguous", call, issNum)
	}
}

// TestSettle_AmbiguousOutcome_NoMergeMachineryRuns verifies that status=
// ambiguous is a clean, separate switch branch from "ready": it never drives
// selfHeal/verifyMerged, even when the outcome carries a non-empty Landing
// value a "ready" outcome would otherwise treat as a PR to gate on.
func TestSettle_AmbiguousOutcome_NoMergeMachineryRuns(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(ambiguousDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	// If selfHeal ran, it would drive MarkReady/EnqueueAutoMerge as part of
	// the merge gate — asserting those call logs stay empty (alongside a
	// single TransitionState call) is the direct signal that branch never
	// ran.

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: testPR, Status: "ambiguous", Note: "unrelated work"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.MarkReadyCalls) != 0 || len(fc.EnqueueAutoMergeCalls) != 0 || len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("ambiguous outcome must never drive merge-gate machinery; MarkReadyCalls=%v EnqueueAutoMergeCalls=%v CloseMergedIssueCalls=%v",
			fc.MarkReadyCalls, fc.EnqueueAutoMergeCalls, fc.CloseMergedIssueCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Errorf("want exactly 1 TransitionState call (no extra merge-gate transitions), got %d: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-ambiguous-spec") {
		t.Errorf("ambiguous outcome must apply agent-ambiguous-spec; got labels=%v", iss.Labels)
	}
}
