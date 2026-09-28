package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// Issue #4015: a status=already-resolved outcome means the Box scouted the
// issue and found the work already done (e.g. landed on main by another PR),
// so it stopped before making any commits. This is a successful, non-crash
// stop, so it must transition to Complete (agent-complete) and close the
// issue as completed, never fall through to agent-failed.
func TestSettle_AlreadyResolvedOutcome_PostsCommentTransitionsAndCloses(t *testing.T) {
	const issNum = "42"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	// Settle posts a separate usage-report comment after the switch on every
	// status, so the closing note is the first of two, not the only one.
	if len(fc.CommentCalls) != 2 {
		t.Fatalf("want 2 comments posted (closing note + usage report), got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	first := fc.CommentCalls[0].Body
	if !strings.Contains(first, "already complete") {
		t.Errorf("first comment body = %q, want it to say the issue is being closed because the work is already complete", first)
	}
	if !strings.Contains(first, note) {
		t.Errorf("first comment body = %q, want it to carry the resolving note %q", first, note)
	}

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}

	if len(fc.CloseMergedIssueCalls) != 1 || fc.CloseMergedIssueCalls[0] != issNum {
		t.Errorf("CloseMergedIssueCalls = %v, want [%s]", fc.CloseMergedIssueCalls, issNum)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("already-resolved outcome must apply agent-complete; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("already-resolved outcome must never fall through to agent-failed; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("already-resolved outcome must remove agent-in-progress; got labels=%v", iss.Labels)
	}
}

// Unlike status=ambiguous, which skips its note comment entirely when o.Note
// is empty, already-resolved always posts the closing sentence: an issue is
// still being closed as complete even when the Box's note came back blank.
func TestSettle_AlreadyResolvedOutcome_EmptyNoteStillPostsClosingComment(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: ""},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 2 {
		t.Fatalf("want 2 comments posted (closing note + usage report) even with an empty note, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "already complete") {
		t.Errorf("first comment body = %q, want it to say the issue is being closed because the work is already complete", fc.CommentCalls[0].Body)
	}
}

// already-resolved never drives selfHeal's merge-gate machinery (MarkReady,
// EnqueueAutoMerge): there is no PR to gate on, only closeIssue's direct
// MergeCloser call.
func TestSettle_AlreadyResolvedOutcome_NoMergeGateMachineryRuns(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: testPR, Status: outcome.StatusAlreadyResolved, Note: "already fixed"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.MarkReadyCalls) != 0 || len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("already-resolved outcome must never drive merge-gate machinery; MarkReadyCalls=%v EnqueueAutoMergeCalls=%v",
			fc.MarkReadyCalls, fc.EnqueueAutoMergeCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Errorf("want exactly 1 TransitionState call (no extra merge-gate transitions), got %d: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
	}
}

// The settled report record for an already-resolved outcome carries state
// "complete" (DispatchState.String()), mirroring the research kind's verdict
// records (report_test.go).
func TestSettle_AlreadyResolvedOutcome_SettledRecordStateComplete(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	const issNum = "42"
	const note = "Already fixed by PR #40."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	want := report.Record{Event: "settled", Issue: issNum, State: "complete", Note: note}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
}

// Issue #3939's actual run shape: a read-only github worker scouts the issue,
// finds the fix already landed on main, makes zero commits, and prints
// status=already-resolved. This replays that shape end to end: no draft PR is
// ever created (there is nothing to relay — no branch, no commits), yet the
// host still reaches agent-complete with the issue closed, not stranded in
// agent-in-progress and never agent-failed.
func TestSettle_GithubReadOnly_AlreadyResolvedZeroCommits_ClosesAsComplete(t *testing.T) {
	const issNum = "3939"
	const note = "Verified the reported bug is already fixed by commit deadbee on main; no changes needed."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	// Zero commits: the Box never printed a pr-intent line and o.Landing is
	// empty, unlike the status=blocked relay shape in blocked_relay_test.go,
	// which always carries a branch name to relay.
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	s := newTestSettle(c, fc, fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("already-resolved with zero commits must never create a draft PR; got %+v", fc.CreateDraftPRCalls)
	}
	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("already-resolved with zero commits must never relay a bundle; got %+v", fc.RelayBundleCalls)
	}

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}

	if len(fc.CloseMergedIssueCalls) != 1 || fc.CloseMergedIssueCalls[0] != issNum {
		t.Errorf("CloseMergedIssueCalls = %v, want [%s]", fc.CloseMergedIssueCalls, issNum)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must never carry agent-failed; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("issue must not be stranded in agent-in-progress; got labels=%v", iss.Labels)
	}

	if len(fc.CommentCalls) < 1 {
		t.Fatalf("want at least 1 comment posted (closing note), got %d", len(fc.CommentCalls))
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "already complete") || !strings.Contains(fc.CommentCalls[0].Body, note) {
		t.Errorf("first comment body = %q, want closing sentence + note %q", fc.CommentCalls[0].Body, note)
	}
}
