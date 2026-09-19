package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// ambiguousDispatchLabels extends testDispatchLabels with the Ambiguous label
// (issue #2275). It is a separate var rather than a mutation of the shared one,
// which stays pinned at the four-label lifecycle set other settle tests use.
var ambiguousDispatchLabels = forge.DispatchLabels{
	Dispatchable: testDispatchLabels.Dispatchable,
	InProgress:   testDispatchLabels.InProgress,
	Complete:     testDispatchLabels.Complete,
	Failed:       testDispatchLabels.Failed,
	Ambiguous:    "agent-ambiguous-spec",
}

// Issue #2275: a status=ambiguous outcome posts o.Note unconditionally, unlike
// postBlockedNoteComment, which fires only under s.landing != nil || s.readOnly,
// and it transitions the issue to Ambiguous (agent-ambiguous-spec), not
// agent-failed.
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

	// A read-write github-shaped tracker (no landing recorder, not read-only) is
	// the s.landing == nil && !s.readOnly case that suppresses
	// postBlockedNoteComment. The ambiguous comment must still post here.
	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	// Settle posts a separate usage-report comment after the switch on every
	// status, so the ambiguous note is the first of two, not the only one.
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

// The `if o.Note != ""` gate skips the ambiguous-note comment but still
// transitions state, so an empty or malformed note never blocks the label swap.
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

	// postUsageComment still posts after the switch, so 1 comment, not 0.
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

// status=ambiguous is a separate switch branch from "ready" and never drives
// selfHeal or verifyMerged, even when the outcome carries a non-empty Landing
// value that a "ready" outcome would treat as a PR to gate on.
func TestSettle_AmbiguousOutcome_NoMergeMachineryRuns(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(ambiguousDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	// selfHeal would drive MarkReady and EnqueueAutoMerge as part of the merge
	// gate, so empty call logs plus a single TransitionState call are the signal
	// that the branch never ran.

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
