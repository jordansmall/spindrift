package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runner"
)

// retryConfig returns a config with retry knobs set explicitly.
func retryConfig(max, backoffSecs, holdJitter int) config {
	c := baseConfig()
	c.transientRetryMax = max
	c.transientBackoffSecs = backoffSecs
	c.holdJitterSecs = holdJitter
	return c
}

// setupClassify replaces classifyFn with a sequence of results. The last
// result is repeated if the sequence is exhausted. Restored via t.Cleanup.
func setupClassify(t *testing.T, results []outcome.Classification) {
	t.Helper()
	orig := classifyFn
	i := 0
	classifyFn = func(driver.Driver, string) (outcome.Classification, error) {
		r := results[len(results)-1]
		if i < len(results) {
			r = results[i]
			i++
		}
		return r, nil
	}
	t.Cleanup(func() { classifyFn = orig })
}

// setupSleep replaces sleepFn with a recorder. Restored via t.Cleanup.
func setupSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	orig := sleepFn
	var calls []time.Duration
	sleepFn = func(d time.Duration) { calls = append(calls, d) }
	t.Cleanup(func() { sleepFn = orig })
	return &calls
}

// setupNow replaces nowFn with a fixed clock. Restored via t.Cleanup.
func setupNow(t *testing.T, now time.Time) {
	t.Helper()
	orig := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = orig })
}

// tempLogDir creates a temp dir with a logs/ subdirectory.
func tempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// boxErr is a non-nil error that stands in for a non-zero box exit.
var boxErr = errors.New("exit 1")

// TestRunWithRetry_SuccessOnFirstRun verifies that a successful run returns true
// without any classify or sleep calls.
func TestRunWithRetry_SuccessOnFirstRun(t *testing.T) {
	dir := tempLogDir(t)
	fr := runner.NewFake() // RunErr = nil → success

	orig := classifyFn
	called := false
	classifyFn = func(driver.Driver, string) (outcome.Classification, error) {
		called = true
		return outcome.Classification{}, nil
	}
	t.Cleanup(func() { classifyFn = orig })
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 0)
	got := runWithRetry(c, dir, fr, issue{number: "1", title: "t"})

	if !got {
		t.Error("want true (success), got false")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if called {
		t.Error("classifyFn should not be called on success")
	}
	if len(*sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0", len(*sleeps))
	}
}

// TestRunWithRetry_TerminalNeverRetried verifies that a terminal failure exits
// after one attempt without retrying.
func TestRunWithRetry_TerminalNeverRetried(t *testing.T) {
	dir := tempLogDir(t)
	fr := runner.NewFake()
	fr.RunErr = boxErr

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Terminal, Reason: outcome.TaskFailed},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 0)
	got := runWithRetry(c, dir, fr, issue{number: "1", title: "t"})

	if got {
		t.Error("want false (terminal failure), got true")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no retry on terminal)", len(fr.RunCalls))
	}
	if len(*sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no sleep on terminal)", len(*sleeps))
	}
}

// TestRunWithRetry_HoldThenSuccess verifies that a 429 with resetsAt causes a
// hold sleep and re-dispatch, and that the hold does not consume the retry cap
// when the re-dispatch succeeds.
func TestRunWithRetry_HoldThenSuccess(t *testing.T) {
	dir := tempLogDir(t)

	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)
	setupNow(t, fixedNow)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first fails, second succeeds

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.RateLimit, ResetAt: &resetAt},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 0) // holdJitter=0 for determinism
	got := runWithRetry(c, dir, fr, issue{number: "2", title: "t"})

	if !got {
		t.Error("want true (success after hold), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (initial + hold re-dispatch)", len(fr.RunCalls))
	}
	if len(*sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(*sleeps))
	}
	wantSleep := 2 * time.Hour // resetAt - fixedNow, jitter=0
	if (*sleeps)[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", (*sleeps)[0], wantSleep)
	}
}

// TestRunWithRetry_HoldJitterAdded verifies that holdJitterSecs is added to
// the hold sleep duration.
func TestRunWithRetry_HoldJitterAdded(t *testing.T) {
	dir := tempLogDir(t)

	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)
	setupNow(t, fixedNow)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.RateLimit, ResetAt: &resetAt},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 10) // holdJitter=10s
	runWithRetry(c, dir, fr, issue{number: "3", title: "t"})

	if len(*sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(*sleeps))
	}
	wantSleep := 1*time.Hour + 10*time.Second
	if (*sleeps)[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", (*sleeps)[0], wantSleep)
	}
}

// TestRunWithRetry_ConsecutiveHoldsConsumeCapAndFail verifies that a series
// of consecutive 429s without progress eventually exhausts the hold cap and
// returns false.
func TestRunWithRetry_ConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	dir := tempLogDir(t)

	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)
	setupNow(t, fixedNow)

	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail

	rateLimitCls := outcome.Classification{
		Class:   outcome.Transient,
		Reason:  outcome.RateLimit,
		ResetAt: &resetAt,
	}
	setupClassify(t, []outcome.Classification{rateLimitCls}) // repeats forever
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 0) // max=3
	got := runWithRetry(c, dir, fr, issue{number: "4", title: "t"})

	if got {
		t.Error("want false (cap exhausted), got true")
	}
	// With max=3: run1→429(free), run2→429(count=1), run3→429(count=2),
	// run4→429(count=3 >= 3) → fail before 4th sleep.
	// Total runs: 4, total sleeps: 3.
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(*sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(*sleeps))
	}
}

// TestRunWithRetry_HoldCapNotConsumedBySuccess verifies that holdCount resets
// after a non-429 outcome: a hold-then-success-then-429 sequence does not
// accumulate cap from the first hold.
func TestRunWithRetry_HoldNotCountedAfterProgress(t *testing.T) {
	dir := tempLogDir(t)

	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)
	setupNow(t, fixedNow)

	fr := runner.NewFake()
	// run1 fails (429), run2 fails (529 — different class), run3 succeeds
	fr.RunErrs = []error{boxErr, boxErr, nil}

	rateLimitCls := outcome.Classification{
		Class:   outcome.Transient,
		Reason:  outcome.RateLimit,
		ResetAt: &resetAt,
	}
	overloadedCls := outcome.Classification{
		Class:  outcome.Transient,
		Reason: outcome.Overloaded,
	}
	setupClassify(t, []outcome.Classification{rateLimitCls, overloadedCls})
	setupSleep(t)

	c := retryConfig(1, 0, 0) // tight cap
	got := runWithRetry(c, dir, fr, issue{number: "5", title: "t"})

	// Even with max=1, the sequence succeeds because:
	// - run1 → 429 hold (free, prevWasHold=true)
	// - run2 → 529 (prevWasHold reset to false, transientCount=1 ≤ 1)
	// - run3 → success
	if !got {
		t.Error("want true (succeeded after mixed transients), got false")
	}
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
}

// TestRunWithRetry_TransientBackoffRetryAndSucceed verifies that a 529/network
// transient is retried with backoff and succeeds on re-dispatch.
func TestRunWithRetry_TransientBackoffRetryAndSucceed(t *testing.T) {
	dir := tempLogDir(t)
	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first fails (529), second succeeds

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.Overloaded},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 10, 0) // backoffSecs=10
	got := runWithRetry(c, dir, fr, issue{number: "6", title: "t"})

	if !got {
		t.Error("want true (success after backoff retry), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	if len(*sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(*sleeps))
	}
	// First retry: backoff = 10s * 1 = 10s
	if (*sleeps)[0] != 10*time.Second {
		t.Errorf("sleep duration: got %v, want %v", (*sleeps)[0], 10*time.Second)
	}
}

// TestRunWithRetry_TransientCapExhausted verifies that a 529/network transient
// that never recovers exhausts the cap and returns false.
func TestRunWithRetry_TransientCapExhausted(t *testing.T) {
	dir := tempLogDir(t)
	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.Network},
	})
	sleeps := setupSleep(t)

	c := retryConfig(2, 5, 0) // max=2, backoffSecs=5
	got := runWithRetry(c, dir, fr, issue{number: "7", title: "t"})

	if got {
		t.Error("want false (cap exhausted), got true")
	}
	// max=2: initial run + 2 retries = 3 total runs, 2 sleeps.
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
	if len(*sleeps) != 2 {
		t.Fatalf("sleep calls: got %d, want 2", len(*sleeps))
	}
	// Linear backoff: retry1 = 5s*1, retry2 = 5s*2
	if (*sleeps)[0] != 5*time.Second {
		t.Errorf("sleep[0]: got %v, want %v", (*sleeps)[0], 5*time.Second)
	}
	if (*sleeps)[1] != 10*time.Second {
		t.Errorf("sleep[1]: got %v, want %v", (*sleeps)[1], 10*time.Second)
	}
}

// TestRunWithRetry_RateLimitWithoutResetAtUsesBackoff verifies that a 429 with
// no resetsAt is treated as a plain transient (backoff retry, not hold).
func TestRunWithRetry_RateLimitWithoutResetAtUsesBackoff(t *testing.T) {
	dir := tempLogDir(t)
	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.RateLimit, ResetAt: nil},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 15, 0) // backoffSecs=15
	got := runWithRetry(c, dir, fr, issue{number: "8", title: "t"})

	if !got {
		t.Error("want true (success after backoff for 429 without resetsAt), got false")
	}
	if len(*sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(*sleeps))
	}
	// Should use backoff, not hold: 15s * 1
	if (*sleeps)[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", (*sleeps)[0])
	}
}

// TestRunWithRetry_HoldWithPastResetUsesJitterOnly verifies that when resetsAt
// is in the past the sleep is clamped to holdJitterSecs.
func TestRunWithRetry_HoldWithPastResetUsesJitterOnly(t *testing.T) {
	dir := tempLogDir(t)

	fixedNow := time.Unix(2_000_000, 0).UTC()
	resetAt := fixedNow.Add(-1 * time.Hour) // in the past
	setupNow(t, fixedNow)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}

	setupClassify(t, []outcome.Classification{
		{Class: outcome.Transient, Reason: outcome.RateLimit, ResetAt: &resetAt},
	})
	sleeps := setupSleep(t)

	c := retryConfig(3, 0, 7) // holdJitter=7s
	runWithRetry(c, dir, fr, issue{number: "9", title: "t"})

	if len(*sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(*sleeps))
	}
	if (*sleeps)[0] != 7*time.Second {
		t.Errorf("sleep duration: got %v, want 7s (clamped to jitter)", (*sleeps)[0])
	}
}
