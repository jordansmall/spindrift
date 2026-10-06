package settle

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/seambundle"
)

// fakeRelay returns fc's push-only view as the BundleRelay under test.
func fakeRelay(fc *forge.Fake) forge.BundleRelay {
	return fc.AsLocal().(forge.BundleRelay)
}

// distinctErrs scripts n relay failures, each with its own message.
func distinctErrs(n int) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = fmt.Errorf("relay failure %d", i+1)
	}
	return errs
}

func newRetrySettle(clock dispatch.Clock) *Settle {
	c := baseConfig()
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.Clock = clock
	return newTestSettle(c, forge.NewFake(), forge.NewFake())
}

// newRelay builds a retryingBundleRelay straight from its fields, with a
// stop func that never fires unless the test swaps it.
func newRelay(inner forge.BundleRelay, clock dispatch.Clock, maxRetries int) *retryingBundleRelay {
	return &retryingBundleRelay{
		backoff:    retry.LinearBackoff{Unit: time.Second, Clock: clock},
		maxRetries: maxRetries,
		stopped:    func() bool { return false },
		num:        "7",
		inner:      inner,
	}
}

func relayRetryLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "status=relay-retry") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestRetryingRelay_NilInnerStaysNil(t *testing.T) {
	_, clock := recordingClock()
	if got := newRetrySettle(clock).retryingRelay("1", 0, nil); got != nil {
		t.Errorf("retryingRelay(nil) = %v, want nil", got)
	}
}

func TestRetryingRelay_RecoversAfterFailures(t *testing.T) {
	sleeps, clock := recordingClock()
	fc := forge.NewFake()
	fc.RelayBundleErrs = failThenSucceed(2)

	var err error
	out := captureStdout(t, func() {
		err = newRelay(fakeRelay(fc), clock, 3).RelayBundle(t.TempDir(), "agent/issue-7")
	})

	if err != nil {
		t.Fatalf("RelayBundle: unexpected error: %v", err)
	}
	if len(fc.RelayBundleCalls) != 3 {
		t.Errorf("inner called %d times, want 3", len(fc.RelayBundleCalls))
	}
	lines := relayRetryLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d retry lines, want 2:\n%s", len(lines), out)
	}
	for i, l := range lines {
		want := fmt.Sprintf("attempt=%d/3", i+1)
		if !strings.Contains(l, want) || !strings.Contains(l, "#7") || !strings.Contains(l, "agent/issue-7") {
			t.Errorf("line %d = %q, want it to carry #7, the ref, and %s", i, l, want)
		}
	}
	if len(*sleeps) == 0 {
		t.Error("no backoff sleeps recorded between retries")
	}
}

func TestRetryingRelay_ExhaustsAndReturnsLastError(t *testing.T) {
	_, clock := recordingClock()
	fc := forge.NewFake()
	scripted := distinctErrs(4)
	fc.RelayBundleErrs = scripted

	var err error
	out := captureStdout(t, func() {
		err = newRelay(fakeRelay(fc), clock, 3).RelayBundle(t.TempDir(), "agent/issue-7")
	})

	if len(fc.RelayBundleCalls) != 4 {
		t.Errorf("inner called %d times, want 4 (1 + Max retries)", len(fc.RelayBundleCalls))
	}
	if want := scripted[3]; !errors.Is(err, want) {
		t.Errorf("err = %v, want the last inner error %v", err, want)
	}
	if n := len(relayRetryLines(out)); n != 3 {
		t.Errorf("got %d retry lines, want 3:\n%s", n, out)
	}
}

// A real relay that fails every attempt after `git bundle verify` passes and
// the fetch starts must give up after the bound and leave the Box's bundle in
// the outbox untouched, so a human can re-run the relay by hand.
func TestRetryingRelay_RealRelayFailingEveryAttemptLeavesBundleUntouched(t *testing.T) {
	_, clock := recordingClock()
	const branch = "agent/issue-7"
	// The seeding commits run git directly, with no identity of their own.
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	seed := forgetest.NewGitRepoFixture(t, "main")
	outbox := t.TempDir()
	forgetest.SeedRelayBundle(t, seed.Bare, "main", outbox, branch)
	path := filepath.Join(outbox, seambundle.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A stale ref lock makes the fetch's ref update fail on every attempt, while
	// the bundle still verifies against the repo's real history.
	lock := filepath.Join(seed.Bare, "refs", "heads", branch+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	parent := local.ResolveParent("7", "")
	inner := local.NewLocalCodeForge(seed.Bare, "main", parent, "Test Bot", "bot@example.com", "agent/issue-").(forge.BundleRelay)

	var relayErr error
	out := captureStdout(t, func() {
		relayErr = newRelay(inner, clock, 3).RelayBundle(outbox, branch)
	})

	if relayErr == nil || errors.Is(relayErr, forge.ErrBundleNotFound) {
		t.Fatalf("err = %v, want a persistent non-bundle-missing relay failure", relayErr)
	}
	if msg := relayErr.Error(); strings.Contains(msg, "malformed bundle") || !strings.Contains(msg, "fetch bundle") {
		t.Errorf("err = %v, want a failure in the fetch step after the bundle verified", relayErr)
	}
	if n := len(relayRetryLines(out)); n != 3 {
		t.Errorf("got %d retry lines, want 3:\n%s", n, out)
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.Equal(after, before) {
		t.Errorf("outbox bundle changed across retries: %d bytes -> %d bytes, %v", len(before), len(after), rerr)
	}
}

func TestRetryingRelay_ZeroMaxIsSingleAttempt(t *testing.T) {
	sleeps, clock := recordingClock()
	fc := forge.NewFake()
	fc.RelayBundleErr = errors.New("ssh auth blip")

	err := newRelay(fakeRelay(fc), clock, 0).RelayBundle(t.TempDir(), "r")

	if err == nil || len(fc.RelayBundleCalls) != 1 || len(*sleeps) != 0 {
		t.Errorf("err=%v calls=%d sleeps=%v, want an error after exactly 1 call and no sleeps", err, len(fc.RelayBundleCalls), *sleeps)
	}
}

func TestRetryingRelay_BundleNotFoundIsNotRetried(t *testing.T) {
	sleeps, clock := recordingClock()
	fc := forge.NewFake()
	fc.RelayBundleErr = fmt.Errorf("outbox: %w", forge.ErrBundleNotFound)

	var err error
	out := captureStdout(t, func() {
		err = newRelay(fakeRelay(fc), clock, 3).RelayBundle(t.TempDir(), "r")
	})

	if !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("err = %v, want ErrBundleNotFound", err)
	}
	if len(fc.RelayBundleCalls) != 1 || len(*sleeps) != 0 || len(relayRetryLines(out)) != 0 {
		t.Errorf("calls=%d sleeps=%v retry lines=%d, want a single un-retried call", len(fc.RelayBundleCalls), *sleeps, len(relayRetryLines(out)))
	}
}

func TestRetryingRelay_StopDuringBackoffAbandons(t *testing.T) {
	stop := false
	slept := 0
	clock := dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {
		slept++
		stop = true
	}}
	relayErr := errors.New("ssh auth blip")
	fc := forge.NewFake()
	fc.RelayBundleErr = relayErr
	r := newRelay(fakeRelay(fc), clock, 3)
	r.stopped = func() bool { return stop }

	var err error
	captureStdout(t, func() { err = r.RelayBundle(t.TempDir(), "r") })

	if !errors.Is(err, errAbandoned) || !errors.Is(err, relayErr) {
		t.Errorf("err = %v, want it to wrap both errAbandoned and the relay error", err)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("inner called %d times, want 1 (no relay after the stop)", len(fc.RelayBundleCalls))
	}
	if slept != 1 {
		t.Errorf("slept %d slices after the stop, want 1", slept)
	}
}

// The constructor must wire the Settle's termination registry into the stop
// func, so an operator stop during backoff reaches a relay built the way every
// call site builds it.
func TestRetryingRelay_ConstructorWiresTermination(t *testing.T) {
	var s *Settle
	clock := dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { s.term.Mark("7") }}
	s = newRetrySettle(clock)
	gen := s.term.Begin("7")
	fc := forge.NewFake()
	fc.RelayBundleErr = errors.New("ssh auth blip")

	var err error
	captureStdout(t, func() { err = s.retryingRelay("7", gen, fakeRelay(fc)).RelayBundle(t.TempDir(), "r") })

	if !errors.Is(err, errAbandoned) || len(fc.RelayBundleCalls) != 1 {
		t.Errorf("err = %v calls = %d, want errAbandoned after 1 call", err, len(fc.RelayBundleCalls))
	}
}

// failThenSucceed scripts n relay failures followed by success.
func failThenSucceed(n int) []error {
	errs := make([]error, n)
	for i := range errs {
		errs[i] = errors.New("ssh auth blip")
	}
	return append(errs, nil)
}

// Pins issue #4649 on the push-only path: a transient relay failure retries
// with backoff instead of stranding the bundle, and the issue still lands.
func TestSelfHeal_LocalForge_RelayRetriesTransientFailureThenMerges(t *testing.T) {
	c := baseConfig()
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	sleeps, clock := recordingClock()
	c.Clock = clock
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErrs = failThenSucceed(2)
	branch := fc.AgentBranch("1")
	s := newTestSettle(c, fc, fc.AsLocal())

	var landing landingResult
	stdout := captureStdout(t, func() { landing, _ = s.selfHeal(dispatch.NewFake(), "1", 0, branch) })

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged", landing)
	}
	if len(fc.RelayBundleCalls) != 3 {
		t.Errorf("RelayBundle calls = %d, want 3", len(fc.RelayBundleCalls))
	}
	if got := strings.Count(stdout, "status=relay-retry"); got != 2 {
		t.Errorf("relay-retry lines = %d, want 2; stdout:\n%s", got, stdout)
	}
	if len(*sleeps) < 2 {
		t.Errorf("backoff sleeps = %v, want at least 2", *sleeps)
	}
	if fc.Merged != branch {
		t.Errorf("fc.Merged = %q, want %q", fc.Merged, branch)
	}
}

// Pins issue #4649 on the draft-PR mediation path.
func TestSettle_GithubReadOnly_ReadyRelayRetriesTransientFailureThenOpensDraftPR(t *testing.T) {
	const issNum = "4649"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = "https://github.com/owner/repo/pull/4649"
	fc.RelayBundleErrs = failThenSucceed(2)

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	stdout := captureStdout(t, func() { s.Settle(dispatch.NewFake(), issNum, 0, result) })

	if len(fc.RelayBundleCalls) != 3 {
		t.Errorf("RelayBundle calls = %d, want 3", len(fc.RelayBundleCalls))
	}
	if got := strings.Count(stdout, "status=relay-retry"); got != 2 {
		t.Errorf("relay-retry lines = %d, want 2; stdout:\n%s", got, stdout)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Errorf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
}

// Pins issue #4649 on the blocked hand-off: a persistently failing relay is
// attempted Policy.Max+1 times, and the last error still reaches the log.
func TestSettle_GithubReadOnly_BlockedRelayExhaustsRetriesAndLogsLastError(t *testing.T) {
	const issNum = "4649"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("ssh auth blip")

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-4649", Status: "blocked", Note: "stuck"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { s.Settle(dispatch.NewFake(), issNum, 0, result) })
	})

	if len(fc.RelayBundleCalls) != 4 {
		t.Errorf("RelayBundle calls = %d, want Policy.Max+1 = 4", len(fc.RelayBundleCalls))
	}
	if !strings.Contains(stderr, "could not relay blocked-hand-off bundle") || !strings.Contains(stderr, "ssh auth blip") {
		t.Errorf("stderr must carry the relay-failure phrase and last error, got: %s", stderr)
	}
}

// Pins issue #4651: a status=ready relay that exhausts its retries parks the
// issue agent-failed and leaves the Box's bundle in the outbox for
// `spindrift recover`.
func TestSettle_GithubReadOnly_ReadyRelayExhaustsRetriesParksFailedWithBundlePreserved(t *testing.T) {
	const issNum = "4651"
	outbox := t.TempDir()
	bundle := filepath.Join(outbox, seambundle.FileName)
	if err := os.WriteFile(bundle, []byte("bundle-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("ssh auth blip")

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: 2, Unit: time.Second}
	_, c.Clock = recordingClock()
	c.OutboxDir = func(string) string { return outbox }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	stdout := captureStdout(t, func() { s.Settle(dispatch.NewFake(), issNum, 0, result) })

	if len(fc.RelayBundleCalls) != 3 {
		t.Errorf("RelayBundle calls = %d, want Policy.Max+1 = 3", len(fc.RelayBundleCalls))
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not run after an exhausted relay, got %+v", fc.CreateDraftPRCalls)
	}
	assertRelayFailureParked(t, fc, issNum)
	if !strings.Contains(stdout, "status=relay-failed") {
		t.Errorf("stdout must carry a status=relay-failed line, got:\n%s", stdout)
	}
	if got, err := os.ReadFile(bundle); err != nil || string(got) != "bundle-bytes" {
		t.Errorf("outbox bundle must be preserved untouched, got %q, %v", got, err)
	}
}

// A Box that wrote no bundle has nothing preserved to recover, so a missing
// bundle under status=ready keeps the merge-blocked, agent-in-progress outcome.
func TestSettle_GithubReadOnly_ReadyBundleNotFoundStaysBlockedInProgress(t *testing.T) {
	const issNum = "4651"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = fmt.Errorf("outbox: %w", forge.ErrBundleNotFound)

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: 2, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	captureStdout(t, func() { s.Settle(dispatch.NewFake(), issNum, 0, result) })

	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle calls = %d, want 1 (not retried)", len(fc.RelayBundleCalls))
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-failed") || !containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("a missing bundle stays agent-in-progress, never agent-failed; labels=%v", iss.Labels)
	}
	if comments := issueComments(fc); len(comments) != 1 || !strings.Contains(comments[0], "merge blocked") {
		t.Errorf("want exactly one merge-blocked comment, got %q", comments)
	}
}

// stopOnRelayBackoff builds a clock whose sleep marks num terminated once the
// relay has been attempted, so the stop lands inside the relay's backoff and
// never in an earlier gate or confirm sleep. It records every sleep.
func stopOnRelayBackoff(sp **Settle, fc *forge.Fake, num string) (*[]time.Duration, dispatch.Clock) {
	var sleeps []time.Duration
	return &sleeps, dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) {
		sleeps = append(sleeps, d)
		if len(fc.RelayBundleCalls) > 0 {
			(*sp).term.Mark(num)
		}
	}}
}

// Pins issue #4649 against issue #3523: a stop during the relay that follows
// conflict-resolve must surface as landingAbandoned, not be wrapped into
// errLandingNeverGreen and demote the already-released issue to Failed.
func TestSelfHeal_ConflictResolveRelayStoppedDuringBackoff_Abandons(t *testing.T) {
	var s *Settle
	fc := forge.NewFake(testDispatchLabels)
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	_, c.Clock = stopOnRelayBackoff(&s, fc, "1")
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.RelayBundleErr = errors.New("ssh auth blip")
	s = newTestSettle(c, fc, fc.AsGithubReadOnly())
	gen := s.term.Begin("1")

	var landing landingResult
	var reason string
	captureStdout(t, func() { landing, reason = s.selfHeal(dispatch.NewFake(), "1", gen, testPR) })

	if landing != landingAbandoned || reason != "" {
		t.Errorf("selfHeal = (%v, %q), want (landingAbandoned, \"\")", landing, reason)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle calls = %d, want 1 (no relay after the stop)", len(fc.RelayBundleCalls))
	}
	assertNoTransitionOrComment(t, fc)
	assertClaimUntouched(t, fc)
}

// Pins issue #4649 on the push-only path: the relay runs ahead of the Complete
// commit, so a stop during its backoff leaves the released issue untouched.
func TestSelfHeal_LocalForge_RelayStoppedDuringBackoff_AbandonsWithoutComplete(t *testing.T) {
	var s *Settle
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	c := baseConfig()
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	_, c.Clock = stopOnRelayBackoff(&s, fc, "1")
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("ssh auth blip")
	s = newTestSettle(c, fc, fc.AsLocal())
	gen := s.term.Begin("1")

	var landing landingResult
	captureStdout(t, func() { landing, _ = s.selfHeal(dispatch.NewFake(), "1", gen, fc.AgentBranch("1")) })

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not run after the stop; fc.Merged=%q", fc.Merged)
	}
	assertNoTransitionOrComment(t, fc)
	assertClaimUntouched(t, fc)
}

// Issue #4651 on the push-only path: a relay that exhausts its retries leaves
// the bundle in the outbox, so the issue parks agent-failed (never Complete)
// with one comment naming `spindrift recover`. landingFailed tells every caller
// the transition and comment are already done.
func TestSelfHeal_LocalForge_RelayExhaustedParksFailed(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	c := baseConfig()
	c.Policy = retry.Policy{Max: 1, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("ssh auth blip")
	s := newTestSettle(c, fc, fc.AsLocal())

	var landing landingResult
	captureStdout(t, func() { landing, _ = s.selfHeal(dispatch.NewFake(), "1", 0, fc.AgentBranch("1")) })

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed", landing)
	}
	if len(fc.RelayBundleCalls) != 2 {
		t.Errorf("RelayBundle calls = %d, want 2 (1 + Max retries)", len(fc.RelayBundleCalls))
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not run after a failed relay; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Errorf("want exactly one transition, got %+v", fc.TransitionStateCalls)
	}
	assertRelayFailureParked(t, fc, "1")
}

// Pins issue #4649: a stop during the fix-pass relay's backoff abandons at
// once, instead of logging fix-relay-failed and sleeping through the no-op
// check's confirm poll.
func TestSelfHeal_FixPassRelayStoppedDuringBackoff_AbandonsWithoutConfirmSleep(t *testing.T) {
	var s *Settle
	fc := forge.NewFake()
	c := fixConfig(3)
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	sleeps, clock := stopOnRelayBackoff(&s, fc, "1")
	c.Clock = clock
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure, forge.StateFailure})
	fc.SetHeadCommitSHAs(testPR, []string{"sha-1", "sha-1", "sha-1", "sha-1"})
	fc.RelayBundleErr = errors.New("ssh auth blip")
	s = newTestSettle(c, fc, fc.AsGithubReadOnly())
	gen := s.term.Begin("1")

	var landing landingResult
	out := captureStdout(t, func() { landing, _ = s.selfHeal(dispatch.NewFake(), "1", gen, testPR) })

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if strings.Contains(out, "fix-relay-failed") {
		t.Errorf("a stop must not log fix-relay-failed; stdout:\n%s", out)
	}
	// The red first poll sleeps nothing, so the lone backoff slice is the only
	// sleep; a second one is the no-op check's MergePollInterval confirm poll.
	if len(*sleeps) != 1 {
		t.Errorf("sleeps = %v, want only the one relay backoff slice", *sleeps)
	}
	assertNoTransitionOrComment(t, fc)
}

// stoppedRelayFixture builds a read-only Settle over fc whose relay always
// fails and whose first backoff marks num terminated (issue #3523). It returns
// the Settle and the generation num was begun at.
func stoppedRelayFixture(t *testing.T, num string, tracker func(*forge.Fake) forge.CodeForge) (*Settle, *forge.Fake, uint64) {
	t.Helper()
	var s *Settle
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = "https://github.com/owner/repo/pull/" + num
	fc.RelayBundleErr = errors.New("ssh auth blip")
	c := baseConfig()
	c.ReadOnly = true
	c.Policy = retry.Policy{Max: 3, Unit: time.Second}
	c.OutboxDir = func(n string) string { return "/outbox/" + n }
	c.BaseBranch = "main"
	_, c.Clock = stopOnRelayBackoff(&s, fc, num)
	s = newTestSettle(c, fc.AsNoLandingRecorder(), tracker(fc))
	return s, fc, s.term.Begin(num)
}

func assertNoStoppedIssueWrites(t *testing.T, fc *forge.Fake) {
	t.Helper()
	assertNoTransitionOrComment(t, fc)
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not run after the stop; got %+v", fc.CreateDraftPRCalls)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle calls = %d, want 1 (no relay after the stop)", len(fc.RelayBundleCalls))
	}
}

// Pins issue #4649 against issue #3523 on the read-only draft-PR mediation: a
// stop during the relay's backoff must not post "merge blocked" or a usage
// comment on the issue the operator stopped.
func TestSettle_GithubReadOnly_ReadyRelayStoppedDuringBackoff_PostsNothing(t *testing.T) {
	const issNum = "4649"
	s, fc, gen := stoppedRelayFixture(t, issNum, (*forge.Fake).AsGithubReadOnly)
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	captureStdout(t, func() { s.Settle(dispatch.NewFake(), issNum, gen, result) })

	assertNoStoppedIssueWrites(t, fc)
}

// Pins issue #4649 against issue #3523 on the adopt path: a stop during the
// relay's backoff counts as handled, so the caller's blocked handling never
// comments on or transitions the stopped issue.
func TestSettle_SettleRelayedBranch_RelayStoppedDuringBackoff_HandledWithoutWrites(t *testing.T) {
	const issNum = "4649"
	s, fc, gen := stoppedRelayFixture(t, issNum, (*forge.Fake).AsGithubReadOnly)
	result := dispatch.Result{Resolved: outcome.Resolved{
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
	}}
	sit := s.situationFor(issNum, false, result)

	var got bool
	captureStdout(t, func() { got = s.SettleRelayedBranch(dispatch.NewFake(), issNum, gen, sit, result) })

	if !got {
		t.Errorf("SettleRelayedBranch = false, want true (handled; the stop owns the issue)")
	}
	assertNoStoppedIssueWrites(t, fc)
}

// A stop interrupting the blocked hand-off relay is not a relay failure, so
// it must not log the "could not relay" phrase (issue #4649).
func TestRelayBlockedWork_StoppedDuringBackoff_LogsAbandonedNotFailure(t *testing.T) {
	cases := map[string]func(*forge.Fake) forge.CodeForge{
		"push-only": (*forge.Fake).AsLocal,
		"pr-shaped": (*forge.Fake).AsGithubReadOnly,
	}
	for name, shape := range cases {
		t.Run(name, func(t *testing.T) {
			const issNum = "4649"
			s, fc, gen := stoppedRelayFixture(t, issNum, shape)
			result := dispatch.Result{
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "stuck"},
				},
				PRIntent:      "feat: add widget\n\nAdds a widget.",
				PRIntentFound: true,
			}

			var stderr string
			captureStdout(t, func() {
				stderr = captureStderr(t, func() { s.relayBlockedWork(issNum, gen, result) })
			})

			if strings.Contains(stderr, "could not relay") || strings.Contains(stderr, "?? #") {
				t.Errorf("a stop must not be logged as a relay failure, got: %s", stderr)
			}
			if !strings.Contains(stderr, "blocked-hand-off relay abandoned") {
				t.Errorf("stderr must carry the abandoned line, got: %s", stderr)
			}
			if len(fc.RelayBundleCalls) != 1 {
				t.Errorf("RelayBundle calls = %d, want 1", len(fc.RelayBundleCalls))
			}
		})
	}
}

// Pins issue #4649 against issue #3523 on the blocked path: a stop during the
// blocked hand-off relay's backoff must not post the blocked note or a usage
// comment on the issue the operator stopped.
func TestSettle_ReadOnlyBlocked_RelayStoppedDuringBackoff_PostsNoComment(t *testing.T) {
	cases := map[string]func(*forge.Fake) forge.CodeForge{
		"push-only": (*forge.Fake).AsLocal,
		"pr-shaped": (*forge.Fake).AsGithubReadOnly,
	}
	for name, shape := range cases {
		t.Run(name, func(t *testing.T) {
			const issNum = "4649"
			s, fc, gen := stoppedRelayFixture(t, issNum, shape)
			result := dispatch.Result{
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "stuck"},
				},
				PRIntent:      "feat: add widget\n\nAdds a widget.",
				PRIntentFound: true,
			}

			captureStdout(t, func() {
				captureStderr(t, func() { s.Settle(dispatch.NewFake(), issNum, gen, result) })
			})

			if len(fc.CommentCalls) != 0 {
				t.Errorf("no comment may be posted after the stop; got %+v", fc.CommentCalls)
			}
			if len(fc.RelayBundleCalls) != 1 {
				t.Errorf("RelayBundle calls = %d, want 1", len(fc.RelayBundleCalls))
			}
		})
	}
}

// assertRelayFailureParked pins issue #4651: an exhausted host-side relay moves
// the issue agent-in-progress -> agent-failed with exactly one comment pointing
// at `spindrift recover`, and never the old merge-blocked comment.
func assertRelayFailureParked(t *testing.T, fc *forge.Fake, num string) {
	t.Helper()
	iss, _ := fc.Issue(num)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("an exhausted relay must park the issue agent-failed; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("agent-in-progress must be cleared by the transition; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("an exhausted relay must NOT carry agent-complete (#2036); labels=%v", iss.Labels)
	}
	// postUsageComment adds one empty-bodied comment under dispatch.NewFake, so
	// count CommentCalls directly: the parking comment plus at most that one.
	var parked []string
	for _, c := range fc.CommentCalls {
		if c.Body != "" {
			parked = append(parked, c.Body)
		}
	}
	if len(parked) != 1 || len(fc.CommentCalls) > 2 {
		t.Fatalf("expected exactly one parking comment, got %d of %d calls: %+v", len(parked), len(fc.CommentCalls), fc.CommentCalls)
	}
	body := parked[0]
	if strings.Contains(body, "merge blocked") {
		t.Errorf("comment must not be the merge-blocked one: %q", body)
	}
	for _, want := range []string{"spindrift recover " + num, "outbox"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment %q must contain %q", body, want)
		}
	}
}
