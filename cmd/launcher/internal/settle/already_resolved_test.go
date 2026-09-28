package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// Issue #4016 (ADR 0039): an already-resolved claim must not close the issue
// when commits exist. This pins the in-box demotion route: the harness
// already rewrote the outcome line to a synthetic status=blocked when its
// SYNTHETIC backstop found commits on the branch, but the driver's own last
// genuine self-report still says already-resolved. Read-write, no landing
// recorder: settle must post the note itself (the agent never got the
// chance to comment) and never close.
func TestSettle_SyntheticBlockedDemotedFromAlreadyResolved_PostsCommentFailsNotCloses(t *testing.T) {
	const issNum = "4016"
	const note = "agent reported already-resolved but 2 commits exist on agent/issue-4016; treating as blocked"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:           true,
			Provenance:      outcome.ProvenanceSynthetic,
			Outcome:         outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusBlocked, Note: note},
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusAlreadyResolved},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("a demoted already-resolved claim must never close the issue; CloseMergedIssueCalls=%v", fc.CloseMergedIssueCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Failed", call, issNum)
	}

	var noteCalls []forge.CommentCall
	for _, call := range fc.CommentCalls {
		if call.Body == note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment carrying the demotion note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}

	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a demoted already-resolved claim must never carry agent-complete; got labels=%v", iss.Labels)
	}
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("a demoted already-resolved claim must carry agent-failed; got labels=%v", iss.Labels)
	}
}

// Host-side counterpart to the in-box demotion above: the driver's own
// genuine outcome line says already-resolved (no SYNTHETIC backstop
// involved), but the harness left a seam bundle in the outbox — evidence
// only the host can see. Read-only, so the blocked arm's relay also fires
// and must preserve the bundle rather than let it strand.
func TestSettle_GenuineAlreadyResolvedWithBundlePresent_DemotesRelaysNotCloses(t *testing.T) {
	const issNum = "4016"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	outbox := t.TempDir()
	writeBundle(t, outbox)

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:      true,
			Provenance: outcome.ProvenanceGenuine,
			Outcome:    outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: "Already fixed by commit abc1234 on main."},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return outbox }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("a demoted already-resolved claim must never close the issue; CloseMergedIssueCalls=%v", fc.CloseMergedIssueCalls)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d: %+v", len(fc.TransitionStateCalls), fc.TransitionStateCalls)
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Failed {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Failed", call, issNum)
	}

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: outbox, Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=%s ref=%s", fc.RelayBundleCalls, outbox, branch)
	}

	found := false
	for _, call := range fc.CommentCalls {
		if strings.Contains(call.Body, branch) && strings.Contains(call.Body, "already-resolved") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a comment naming branch %s and the already-resolved demotion; got %+v", branch, fc.CommentCalls)
	}

	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a demoted already-resolved claim must never carry agent-complete; got labels=%v", iss.Labels)
	}
}

// The negative case for the host-side bundle check: OutboxDir is configured
// but the outbox holds no bundle file, so bundlePresent reports false and
// the already-resolved outcome must settle exactly as before this issue —
// Complete and closed.
func TestSettle_AlreadyResolvedOutboxConfiguredNoBundle_StillClosesAsComplete(t *testing.T) {
	const issNum = "4016"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	outbox := t.TempDir() // no bundle written

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:      true,
			Provenance: outcome.ProvenanceGenuine,
			Outcome:    outcome.Outcome{Issue: issNum, Landing: "", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc)
	s.Settle(d, issNum, 0, result)

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
}

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

// orderTrackingTracker wraps a forge.IssueTracker to log Comment,
// TransitionState, and CloseMergedIssue calls into one shared slice, in
// call order, across methods — forge.Fake logs each method into its own
// slice, with no cross-method ordering, so this is the cheapest way to
// assert gate.go's already-resolved arm fires comment, then label swap,
// then close, in that order.
type orderTrackingTracker struct {
	forge.IssueTracker
	closer forge.MergeCloser
	order  *[]string
}

func (o orderTrackingTracker) Comment(num, body string) error {
	*o.order = append(*o.order, "comment")
	return o.IssueTracker.Comment(num, body)
}

func (o orderTrackingTracker) TransitionState(num string, from, to forge.DispatchState) error {
	*o.order = append(*o.order, "transition")
	return o.IssueTracker.TransitionState(num, from, to)
}

func (o orderTrackingTracker) CloseMergedIssue(num string) error {
	*o.order = append(*o.order, "close")
	return o.closer.CloseMergedIssue(num)
}

var _ forge.MergeCloser = orderTrackingTracker{}

// Issue #4017: a forgejo-shaped tracker (MergeCloser, no IssueCloser) closes
// an already-resolved issue through closeResolvedIssue's MergeCloser branch,
// same as closeIssue's existing merged-PR backstop.
func TestSettle_AlreadyResolvedOutcome_ForgejoShapeCloses(t *testing.T) {
	const issNum = "42"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	forgejoShaped := fc.AsForgejoShaped()
	closer, ok := forgejoShaped.(forge.MergeCloser)
	if !ok {
		t.Fatalf("AsForgejoShaped() must implement forge.MergeCloser")
	}
	var order []string
	tracker := orderTrackingTracker{IssueTracker: forgejoShaped, closer: closer, order: &order}

	s := newTestSettle(baseConfig(), tracker, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) == 0 || !strings.Contains(fc.CommentCalls[0].Body, "already complete") || !strings.Contains(fc.CommentCalls[0].Body, note) {
		t.Fatalf("first comment: got %+v, want closing sentence + note %q", fc.CommentCalls, note)
	}

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("issue must not be stranded in agent-in-progress; got labels=%v", iss.Labels)
	}

	if len(fc.CloseMergedIssueCalls) != 1 || fc.CloseMergedIssueCalls[0] != issNum {
		t.Errorf("CloseMergedIssueCalls = %v, want [%s]", fc.CloseMergedIssueCalls, issNum)
	}

	// gate.go's already-resolved arm posts the closing comment, then swaps
	// the label (transitionState), then closes, then posts the separate
	// usage-report comment — so the first three entries, not the whole
	// log, are the ones under test here.
	want := []string{"comment", "transition", "close"}
	if len(order) < len(want) {
		t.Fatalf("call order = %v, want a %v prefix", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("call order = %v, want a %v prefix", order, want)
			break
		}
	}
}

// Issue #4017: a local-shaped tracker (IssueCloser, no MergeCloser) closes an
// already-resolved issue through closeResolvedIssue's IssueCloser fallback —
// the one exception to ADR 0029's reconcile-only local closed: axis, since
// already-resolved has no merged PR for reconcile to key off.
func TestSettle_AlreadyResolvedOutcome_LocalShapeCloses(t *testing.T) {
	const issNum = "42"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsLocalShaped(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}

	if len(fc.CloseIssueCalls) != 1 || fc.CloseIssueCalls[0] != issNum {
		t.Errorf("CloseIssueCalls = %v, want [%s]", fc.CloseIssueCalls, issNum)
	}
	if len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
	}

	iss, _ := fc.Issue(issNum)
	if iss.State != forge.IssueClosed {
		t.Errorf("issue must be closed; got state=%v", iss.State)
	}
}

// Issue #4017: closeResolvedIssue's IssueCloser fallback returns an error
// (e.g. the tracker rejects a double close). Settle must not panic and must
// still land on Complete — a close failure is best-effort, logged to
// stderr, matching closeIssue's own error handling.
func TestSettle_AlreadyResolvedOutcome_IssueCloserErrorStillCompletes(t *testing.T) {
	const issNum = "42"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CloseIssueErr = errors.New("tracker rejected close")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsLocalShaped(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("a close error must still leave the issue at agent-complete; got labels=%v", iss.Labels)
	}
	if iss.State == forge.IssueClosed {
		t.Errorf("a rejected close must leave the issue open; got state=%v", iss.State)
	}
}

// Issue #4017: a tracker with neither MergeCloser nor IssueCloser (e.g. a
// jira-shaped tracker) must leave closeResolvedIssue a no-op — Settle still
// reaches Complete, and no close call is attempted on either interface.
func TestSettle_AlreadyResolvedOutcome_NeitherCloserShapeCompletesWithNoClose(t *testing.T) {
	const issNum = "42"
	const note = "Already fixed by commit abc1234 on main."

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: outcome.StatusAlreadyResolved, Note: note},
		},
	}

	tracker := fc.AsNoLandingRecorder()
	if _, ok := tracker.(forge.MergeCloser); ok {
		t.Fatalf("AsNoLandingRecorder() must not implement forge.MergeCloser")
	}
	if _, ok := tracker.(forge.IssueCloser); ok {
		t.Fatalf("AsNoLandingRecorder() must not implement forge.IssueCloser")
	}

	s := newTestSettle(baseConfig(), tracker, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want 1 TransitionState call, got %d", len(fc.TransitionStateCalls))
	}
	call := fc.TransitionStateCalls[0]
	if call.Num != issNum || call.From != forge.InProgress || call.To != forge.Complete {
		t.Errorf("TransitionState call: got %+v, want num=%s from=InProgress to=Complete", call, issNum)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("neither-closer shape must still reach agent-complete; got labels=%v", iss.Labels)
	}
	if len(fc.CloseIssueCalls) != 0 || len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("neither-closer shape must attempt no close calls; CloseIssueCalls=%v CloseMergedIssueCalls=%v", fc.CloseIssueCalls, fc.CloseMergedIssueCalls)
	}
}

// Issue #4017: a tracker implementing both closers closes through MergeCloser
// only — closeResolvedIssue's IssueCloser branch is a fallback, not a second
// close.
func TestSettle_AlreadyResolvedOutcome_BothClosersPrefersMergeCloser(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: outcome.StatusAlreadyResolved, Note: "Already fixed."},
		},
	}

	var tracker forge.IssueTracker = fc
	if _, ok := tracker.(forge.IssueCloser); !ok {
		t.Fatalf("forge.Fake must implement forge.IssueCloser for this test")
	}

	s := newTestSettle(baseConfig(), tracker, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CloseMergedIssueCalls) != 1 || fc.CloseMergedIssueCalls[0] != issNum {
		t.Errorf("CloseMergedIssueCalls = %v, want [%s]", fc.CloseMergedIssueCalls, issNum)
	}
	if len(fc.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none -- MergeCloser wins", fc.CloseIssueCalls)
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
