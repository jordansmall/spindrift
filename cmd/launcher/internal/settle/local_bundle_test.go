package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// A local Dispatch's blocked status posts the outcome's note as a comment via
// Comment(num, note), the host-mediated substitute for the gh issue comment
// call the Box's issue-prompt runs under github (ADR 0032, issue #1692). The
// tracker's LandingRecorder-implementing shape (plain forge.NewFake()) is this
// suite's "local" convention (see AsLocal/AsNoLandingRecorder).
func TestSettle_LocalForge_BlockedPostsNoteAsComment(t *testing.T) {
	const issNum = "42"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: "blocked", Note: "push rejected; PR opened as draft"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	var noteCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if c.Body == result.Resolved.Outcome.Note {
			noteCalls = append(noteCalls, c)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
	if noteCalls[0].Num != issNum {
		t.Errorf("comment Num: got %q, want %q", noteCalls[0].Num, issNum)
	}
}
