package settle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
)

const draftPRRetryURL = "https://github.com/owner/repo/pull/5146"

var errDraftPR504 = errors.New("HTTP 504: 504 Gateway Timeout (https://api.github.com/graphql)")

// draftPRRetryFixture builds a read-only Settle whose relay succeeds, so a
// ready hand-off reaches the host-side draft PR create (issue #5146).
func draftPRRetryFixture(t *testing.T, maxRetries int, clock dispatch.Clock) (*Settle, *forge.Fake, dispatch.Result) {
	t.Helper()
	const issNum = "5146"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = draftPRRetryURL
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: maxRetries, Unit: time.Second}
	c.OutboxDir = func(string) string { return t.TempDir() }
	c.BaseBranch = "main"
	if clock.Sleep != nil {
		c.Clock = clock
	} else {
		_, c.Clock = recordingClock()
	}
	return newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly()), fc, result
}

func TestRetryingDraftPR_NilInnerStaysNil(t *testing.T) {
	s := newRetrySettle(dispatch.Clock{})
	if got := s.retryingDraftPR("7", 0, nil, nil); got != nil {
		t.Errorf("retryingDraftPR(nil) = %v, want nil", got)
	}
}

func TestSettle_GithubReadOnly_DraftPRCreateRetriesTransientFailureThenOpens(t *testing.T) {
	s, fc, result := draftPRRetryFixture(t, 3, dispatch.Clock{})
	fc.CreateDraftPRErrs = []error{errDraftPR504, nil}
	tally := installGreenCIAndTally(t, fc)

	stdout := captureStdout(t, func() { s.Settle(dispatch.NewFake(), "5146", 0, result) })

	if len(fc.CreateDraftPRCalls) != 2 {
		t.Errorf("CreateDraftPR calls = %d, want 2", len(fc.CreateDraftPRCalls))
	}
	if !strings.Contains(stdout, "status=draft-pr-retry  attempt=1/3") || !strings.Contains(stdout, "504 Gateway Timeout") {
		t.Errorf("stdout must log the retry attempt and error, got:\n%s", stdout)
	}
	assertDraftPRHandedOff(t, fc, tally, stdout)
}

// A gateway timeout can fire after the forge made the PR: the retry must adopt
// it instead of creating a second one.
func TestSettle_GithubReadOnly_DraftPRCreateTimeoutThatCreatedIsAdopted(t *testing.T) {
	s, fc, result := draftPRRetryFixture(t, 3, dispatch.Clock{})
	branch := fc.AgentBranch("5146")
	fc.CreateDraftPRErrs = []error{errDraftPR504}
	fc.CreateDraftPROnCall = func(head string) {
		fc.SetPR(head, forge.PR{URL: draftPRRetryURL})
	}
	tally := installGreenCIAndTally(t, fc)

	stdout := captureStdout(t, func() { s.Settle(dispatch.NewFake(), "5146", 0, result) })

	if len(fc.CreateDraftPRCalls) != 1 || fc.CreateDraftPRCalls[0].Head != branch {
		t.Errorf("CreateDraftPR calls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	if !strings.Contains(stdout, "status=draft-pr-adopted") {
		t.Errorf("stdout must log the adoption, got:\n%s", stdout)
	}
	assertDraftPRHandedOff(t, fc, tally, stdout)
}

func TestRetryingDraftPR_AdoptsOpenPRBeforeRecreating(t *testing.T) {
	s, fc, _ := draftPRRetryFixture(t, 3, dispatch.Clock{})
	fc.CreateDraftPRErrs = []error{errDraftPR504}
	fc.SetPR("b", forge.PR{URL: draftPRRetryURL})

	url, created, _, err := createViaRetry(t, s, fc)

	if err != nil || created || url != draftPRRetryURL {
		t.Errorf("got (%q, %v, %v), want the adopted URL with created=false", url, created, err)
	}
}

func TestRetryingDraftPR_LookupErrorFallsThroughToRecreate(t *testing.T) {
	s, fc, _ := draftPRRetryFixture(t, 3, dispatch.Clock{})
	fc.CreateDraftPRErrs = []error{errDraftPR504}
	fc.OpenPRForBranchErr = errors.New("lookup blip")

	url, created, stdout, err := createViaRetry(t, s, fc)

	if err != nil || !created || url != draftPRRetryURL || len(fc.CreateDraftPRCalls) != 2 {
		t.Errorf("got (%q, %v, %v) after %d calls, want a successful re-create", url, created, err, len(fc.CreateDraftPRCalls))
	}
	if !strings.Contains(stdout, "status=draft-pr-lookup-failed") || !strings.Contains(stdout, "lookup blip") {
		t.Errorf("stdout must log the failed lookup, got:\n%s", stdout)
	}
}

func TestRetryingDraftPR_ZeroMaxIsSingleAttempt(t *testing.T) {
	s, fc, _ := draftPRRetryFixture(t, 0, dispatch.Clock{})
	fc.CreateDraftPRErrs = []error{errDraftPR504}

	_, _, _, err := createViaRetry(t, s, fc)

	if !errors.Is(err, errDraftPR504) || len(fc.CreateDraftPRCalls) != 1 {
		t.Errorf("err=%v calls=%d, want the 504 after exactly 1 call", err, len(fc.CreateDraftPRCalls))
	}
}

// createViaRetry drives one CreateDraftPR through the retry wrapper directly
// and returns what it logged.
func createViaRetry(t *testing.T, s *Settle, fc *forge.Fake) (url string, created bool, stdout string, err error) {
	t.Helper()
	stdout = captureStdout(t, func() {
		url, created, err = s.retryingDraftPR("5146", 0, fc.AsGithubReadOnly().(forge.DraftPRCreator), fc).
			CreateDraftPR("t", "b", "main", "b")
	})
	return url, created, stdout, err
}

// Pins issue #5144's park as the exhaustion path: Policy.Max+1 attempts, then
// the same agent-failed park.
func TestSettle_GithubReadOnly_DraftPRCreateExhaustsRetriesThenParks(t *testing.T) {
	s, fc, result := draftPRRetryFixture(t, 2, dispatch.Clock{})
	fc.CreateDraftPRErr = errDraftPR504

	stdout := captureStdout(t, func() { s.Settle(dispatch.NewFake(), "5146", 0, result) })

	if len(fc.CreateDraftPRCalls) != 3 {
		t.Errorf("CreateDraftPR calls = %d, want Policy.Max+1 = 3", len(fc.CreateDraftPRCalls))
	}
	assertOutboxParked(t, fc, "5146")
	if !strings.Contains(stdout, "status=draft-pr-failed") {
		t.Errorf("stdout must carry status=draft-pr-failed, got:\n%s", stdout)
	}
	parked := false
	for _, c := range fc.CommentCalls {
		if strings.HasPrefix(c.Body, "draft PR create failed: HTTP 504") && strings.Contains(c.Body, "spindrift recover 5146") {
			parked = true
		}
	}
	if !parked {
		t.Errorf("want the #5144 recover comment, got %+v", fc.CommentCalls)
	}
}

// A stop during the create's backoff is an abandon, not a failure: no comment,
// no park, and no lookup or re-create after the stop.
func TestSettle_GithubReadOnly_DraftPRCreateStoppedDuringBackoff_PostsNothing(t *testing.T) {
	var s *Settle
	var fc *forge.Fake
	clock := dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { s.term.Mark("5146") }}
	s, fc, result := draftPRRetryFixture(t, 3, clock)
	fc.CreateDraftPRErr = errDraftPR504
	fc.CreateDraftPROnCall = func(head string) { fc.SetPR(head, forge.PR{URL: draftPRRetryURL}) }
	gen := s.term.Begin("5146")

	captureStdout(t, func() { s.Settle(dispatch.NewFake(), "5146", gen, result) })

	assertNoTransitionOrComment(t, fc)
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Errorf("CreateDraftPR calls = %d, want 1 (no re-create after the stop)", len(fc.CreateDraftPRCalls))
	}
}

// installGreenCIAndTally scripts green CI on the opened PR and installs a
// process-wide Tally hook (restored on cleanup), so a successful hand-off
// reaches the merge gate's Complete instead of a CI-timeout failure that would
// mask a spurious failed or merge-blocked write.
func installGreenCIAndTally(t *testing.T, fc *forge.Fake) *Tally {
	t.Helper()
	fc.SetCheckStates(draftPRRetryURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))
	return tally
}

// assertDraftPRHandedOff pins that the hand-off proceeded on the opened PR (the
// merge gate's lines name its URL) and settled clean: no failed transition, the
// last transition Complete, and a tally holding only that Complete entry.
func assertDraftPRHandedOff(t *testing.T, fc *forge.Fake, tally *Tally, stdout string) {
	t.Helper()
	if !strings.Contains(stdout, "landing="+draftPRRetryURL) || strings.Contains(stdout, "status=draft-pr-failed") {
		t.Errorf("hand-off must proceed on %s without parking; stdout:\n%s", draftPRRetryURL, stdout)
	}
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "draft PR create failed") || strings.Contains(c.Body, "merge blocked") {
			t.Errorf("unexpected failure comment %q", c.Body)
		}
	}
	for _, tr := range fc.TransitionStateCalls {
		if tr.To == forge.Failed {
			t.Errorf("unexpected transition to failed: %+v", tr)
		}
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("want a transition to complete, got none")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Complete {
		t.Errorf("last transition To=%v, want Complete", last.To)
	}
	// Tally keeps the last write per key, so a lone Complete entry also proves
	// no merge-blocked or failed record survives.
	if outs := tally.Outcomes(); len(outs) != 1 || outs[0].State != forge.Complete.String() {
		t.Errorf("tally = %+v, want exactly one Complete entry", outs)
	}
}
