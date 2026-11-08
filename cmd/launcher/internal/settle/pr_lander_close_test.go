package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// closeCases are the rows for who closes the issue after a landing: a
// confirmed immediate merge closes it through forge.MergeCloser, a backstop to
// the forge's own merged-PR auto-close (issues #1892, #2259), and nothing
// else does.
func closeCases() []prCase {
	merged := prSettlement(dispatchrecord.ReasonMerged)
	complete := []string{"agent-complete"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	flipOnly := []string{"MarkReady:" + testPR}
	none := []string{}
	noCloseCalls := func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(fc.CloseIssueCalls) != 0 {
			t.Errorf("CloseIssueCalls = %v, want none", fc.CloseIssueCalls)
		}
	}

	return []prCase{
		{
			name: "a confirmed immediate merge closes the issue", tracker: backend.GitHub,
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantClosed: []string{"1"},
		},
		{
			name:  "a forgejo-shaped tracker is closed by the same backstop, with one usage comment",
			shape: shapeForgejo, tracker: backend.Forgejo,
			want: merged, wantLabels: complete, wantNoLabels: []string{"agent-in-progress"}, wantMerged: testPR,
			wantCalls: flipThenMerge, wantClosed: []string{"1"},
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				t.Helper()
				if iss, _ := fc.Issue("1"); iss.State != forge.IssueClosed {
					t.Errorf("issue state = %q, want %q", iss.State, forge.IssueClosed)
				}
				if len(fc.CommentCalls) != 1 {
					t.Errorf("usage comment posts = %d, want 1", len(fc.CommentCalls))
				}
			},
		},
		// Only reconcile's sweep may close a landed local issue (ADR 0029).
		{
			name:  "a local tracker over a PR forge is left for reconcile to close",
			shape: shapeLocalPR, tracker: backend.Local,
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantClosed: none, check: noCloseCalls,
		},
		{
			name: "manual mode leaves the issue open", mergeMode: "manual",
			want: prSettlement(dispatchrecord.ReasonManual), wantLabels: complete, wantCalls: flipOnly, wantClosed: none,
		},
		{
			name: "auto mode leaves the issue open", mergeMode: "auto",
			want:       prSettlement(dispatchrecord.ReasonAutoMergeEnqueued),
			wantLabels: complete, wantCalls: []string{"MarkReady:" + testPR, "EnqueueAutoMerge:" + testPR}, wantClosed: none,
		},
		{
			name: "a red CI never closes the issue", checks: []forge.RollupState{red},
			want:     Settlement{Verdict: ForReason(dispatchrecord.ReasonCIRed), PRURL: testPR, Note: "ci-red: still red after exhausting 0 fix pass(es)"},
			prClosed: true, wantLabels: []string{"agent-failed"}, wantClosed: none,
		},
	}
}
