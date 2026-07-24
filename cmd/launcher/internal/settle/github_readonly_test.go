package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSettle_GithubReadOnly_BlockedPostsNoteAsComment mirrors
// TestSettle_LocalForge_BlockedPostsNoteAsComment for a github-shaped
// tracker (AsNoLandingRecorder) under BOX_FORGE_AND_ISSUE_ACCESS=read-only
// (issue #1917): the Box holds no write token in read-only mode, so its
// blocked-note travels via the outcome note= field the same way local's
// always has — driven by Config.ReadOnly, not by the LandingRecorder type
// assertion TestSettle_PostsUsageComment_Blocked exercises for read-write.
func TestSettle_GithubReadOnly_BlockedPostsNoteAsComment(t *testing.T) {
	const issNum = "42"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: "https://github.com/owner/repo/pull/99", Status: "blocked", Note: "push rejected; PR opened as draft"},
	}

	c := baseConfig()
	c.ReadOnly = true
	s := New(c, fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	var noteCalls []forge.CommentCall
	for _, call := range fc.CommentCalls {
		if call.Body == result.Outcome.Note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
	if noteCalls[0].Num != issNum {
		t.Errorf("comment Num: got %q, want %q", noteCalls[0].Num, issNum)
	}
}
