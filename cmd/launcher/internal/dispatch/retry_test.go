package dispatch

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
)

// noOpenPR is the default OpenPRForIssue: no PR exists yet, so a zero-exit
// transient classification proceeds to retry rather than short-circuiting.
func noOpenPR(string) (bool, error) { return false, nil }

// retryConfig returns a Config with retry knobs set explicitly.
func retryConfig(max, backoffSecs, holdJitter int) Config {
	return Config{
		TransientRetryMax:    max,
		TransientBackoffSecs: backoffSecs,
		HoldJitterSecs:       holdJitter,
		OpenPRForIssue:       noOpenPR,
	}
}

// fakeClock returns a Clock with a fixed Now and a Sleep that records
// durations into calls.
func fakeClock(now time.Time, calls *[]time.Duration) Clock {
	return Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { *calls = append(*calls, d) },
	}
}

// newTestDispatch builds a Dispatch wired to fr and drv with the given retry
// config and clock, without going through a Factory (so tests can inject a
// fake Clock, which Factory's constructor doesn't expose a seam for
// bypassing the real cache).
func newTestDispatch(t *testing.T, cfg Config, fr runner.Runner, drv fakeDriver, clock Clock) *Dispatch {
	t.Helper()
	dir := tempLogDir(t)
	f, err := NewFactory(cfg, dir, fr, drv, clock)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	return f.New("1", "t")
}

// TestDispatchWithRetry_SuccessOnFirstRun verifies that a successful run
// whose box reports an outcome line returns it without any classify or
// sleep calls.
func TestDispatchWithRetry_SuccessOnFirstRun(t *testing.T) {
	fr := runner.NewFake() // RunErr = nil → success
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	called := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		called = true
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.OutcomeFound {
		t.Fatal("want OutcomeFound=true")
	}
	if result.Outcome.Status != "ready" {
		t.Errorf("Outcome.Status: got %q, want %q", result.Outcome.Status, "ready")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if called {
		t.Error("classify should not be called when an outcome line was found")
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0", len(sleeps))
	}
}

// TestDispatchWithRetry_SuccessWithoutOutcomeClassifies verifies that a
// zero-exit box that wrote no outcome line still gets a best-effort
// classification, so gateIssue-style callers can explain what happened
// without touching the log themselves.
func TestDispatchWithRetry_SuccessWithoutOutcomeClassifies(t *testing.T) {
	fr := runner.NewFake() // RunErr = nil → success, no outcome line written
	wantCls := driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return wantCls, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if result.OutcomeFound {
		t.Fatal("want OutcomeFound=false")
	}
	if result.Classification != wantCls {
		t.Errorf("Classification: got %+v, want %+v", result.Classification, wantCls)
	}
}

// TestDispatchWithRetry_SuccessWithMalformedOutcomeSetsParseErr verifies
// that a zero-exit box whose log has an unparseable SPINDRIFT_OUTCOME line
// (missing required fields) surfaces ParseErr without attempting
// classification.
func TestDispatchWithRetry_SuccessWithMalformedOutcomeSetsParseErr(t *testing.T) {
	fr := runner.NewFake()
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1\n") // missing landing= and status=
	called := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		called = true
		return driver.Classification{}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if result.ParseErr == nil {
		t.Fatal("want ParseErr set for an unparseable outcome line")
	}
	if result.OutcomeFound {
		t.Error("want OutcomeFound=false for an unparseable outcome line")
	}
	if called {
		t.Error("classify should not be called when the outcome line failed to parse")
	}
}

// TestDispatchWithRetry_TerminalNeverRetried verifies that a terminal
// failure exits after one attempt without retrying.
func TestDispatchWithRetry_TerminalNeverRetried(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(time.Time{}, &sleeps))

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (terminal failure), got true")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no retry on terminal)", len(fr.RunCalls))
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no sleep on terminal)", len(sleeps))
	}
}

// TestDispatchWithRetry_HoldThenSuccess verifies that a 429 with resetsAt
// causes a hold sleep and re-dispatch, and that the hold does not consume
// the retry cap when the re-dispatch succeeds.
func TestDispatchWithRetry_HoldThenSuccess(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first fails, second succeeds
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0 for determinism

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after hold), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (initial + hold re-dispatch)", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 2 * time.Hour // resetAt - fixedNow, jitter=0
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

// TestDispatchWithRetry_HoldJitterAdded verifies that HoldJitterSecs is
// added to the hold sleep duration.
func TestDispatchWithRetry_HoldJitterAdded(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 10), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=10s

	d.Run()

	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 1*time.Hour + 10*time.Second
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

// TestDispatchWithRetry_ConsecutiveHoldsConsumeCapAndFail verifies that a
// series of consecutive 429s without progress eventually exhausts the hold
// cap and returns Success=false.
func TestDispatchWithRetry_ConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	// With max=3: run1→429(free), run2→429(count=1), run3→429(count=2),
	// run4→429(count=3 >= 3) → fail before 4th sleep.
	// Total runs: 4, total sleeps: 3.
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(sleeps))
	}
}

// TestDispatchWithRetry_HoldNotCountedAfterProgress verifies that holdCount
// resets after a non-429 outcome: a hold-then-different-transient-then-
// success sequence does not accumulate cap from the first hold.
func TestDispatchWithRetry_HoldNotCountedAfterProgress(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake()
	// run1 fails (429), run2 fails (529 — different class), run3 succeeds
	fr.RunErrs = []error{boxErr, boxErr, nil}
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")

	rateLimitCls := driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}
	overloadedCls := driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}
	calls := 0
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		calls++
		if calls == 1 {
			return rateLimitCls, nil
		}
		return overloadedCls, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(1, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // tight cap

	result := d.Run()

	// Even with max=1, the sequence succeeds because:
	// - run1 → 429 hold (free, prevWasHold=true)
	// - run2 → 529 (prevWasHold reset to false, transientCount=1 ≤ 1)
	// - run3 → success
	if !result.Success {
		t.Error("want Success=true (succeeded after mixed transients), got false")
	}
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
}

// TestDispatchWithRetry_TransientBackoffRetryAndSucceed verifies that a
// 529/network transient is retried with backoff and succeeds on
// re-dispatch.
func TestDispatchWithRetry_TransientBackoffRetryAndSucceed(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first fails (529), second succeeds
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 10, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // backoffSecs=10

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after backoff retry), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 10*time.Second {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], 10*time.Second)
	}
}

// TestDispatchWithRetry_TransientCapExhausted verifies that a 529/network
// transient that never recovers exhausts the cap and returns Success=false.
func TestDispatchWithRetry_TransientCapExhausted(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr // all runs fail
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Network}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(2, 5, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // max=2, backoffSecs=5

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	// max=2: initial run + 2 retries = 3 total runs, 2 sleeps.
	if len(fr.RunCalls) != 3 {
		t.Errorf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleep calls: got %d, want 2", len(sleeps))
	}
	// Linear backoff: retry1 = 5s*1, retry2 = 5s*2
	if sleeps[0] != 5*time.Second {
		t.Errorf("sleep[0]: got %v, want %v", sleeps[0], 5*time.Second)
	}
	if sleeps[1] != 10*time.Second {
		t.Errorf("sleep[1]: got %v, want %v", sleeps[1], 10*time.Second)
	}
}

// TestDispatchWithRetry_RateLimitWithoutResetAtUsesBackoff verifies that a
// 429 with no resetsAt is treated as a plain transient (backoff retry, not
// hold).
func TestDispatchWithRetry_RateLimitWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: nil}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // backoffSecs=15

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (success after backoff for 429 without resetsAt), got false")
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	// Should use backoff, not hold: 15s * 1
	if sleeps[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", sleeps[0])
	}
}

// TestDispatchWithRetry_HoldWithPastResetUsesJitterOnly verifies that when
// resetsAt is in the past the sleep is clamped to HoldJitterSecs.
func TestDispatchWithRetry_HoldWithPastResetUsesJitterOnly(t *testing.T) {
	fixedNow := time.Unix(2_000_000, 0).UTC()
	resetAt := fixedNow.Add(-1 * time.Hour) // in the past

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil}
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 7), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=7s

	d.Run()

	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 7*time.Second {
		t.Errorf("sleep duration: got %v, want 7s (clamped to jitter)", sleeps[0])
	}
}

// TestDispatchWithRetry_ZeroExitRateLimitHoldsAndRedispatches verifies issue
// #565: a box that exits zero but writes no SPINDRIFT_OUTCOME line, whose log
// nonetheless classifies as a rate limit with a known resetsAt, is held and
// re-dispatched exactly like a non-zero 429 exit — instead of dead-ending as
// status=missing.
func TestDispatchWithRetry_ZeroExitRateLimitHoldsAndRedispatches(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 2 && box.Output != nil {
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")) //nolint:errcheck
		}
		return nil // always exits zero, first attempt writes no outcome line
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // holdJitter=0

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.OutcomeFound {
		t.Fatal("want OutcomeFound=true after hold + re-dispatch")
	}
	if calls != 2 {
		t.Errorf("Run calls: got %d, want 2 (initial zero-exit + hold re-dispatch)", calls)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	wantSleep := 2 * time.Hour
	if sleeps[0] != wantSleep {
		t.Errorf("sleep duration: got %v, want %v", sleeps[0], wantSleep)
	}
}

// TestDispatchWithRetry_ZeroExitTransientWithoutResetAtUsesBackoff verifies
// issue #565's third acceptance criterion: a zero-exit, no-outcome run whose
// log carries a transient marker but no resetsAt (or a non-rate-limit
// transient) follows the existing backoff-retry path rather than an
// indefinite hold or an immediate status=missing.
func TestDispatchWithRetry_ZeroExitTransientWithoutResetAtUsesBackoff(t *testing.T) {
	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 2 && box.Output != nil {
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")) //nolint:errcheck
		}
		return nil // always exits zero
	}
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 15, 0), fr, drv, fakeClock(time.Time{}, &sleeps)) // backoffSecs=15

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true, got false")
	}
	if !result.OutcomeFound {
		t.Fatal("want OutcomeFound=true after backoff + re-dispatch")
	}
	if calls != 2 {
		t.Errorf("Run calls: got %d, want 2 (initial zero-exit + backoff re-dispatch)", calls)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1", len(sleeps))
	}
	if sleeps[0] != 15*time.Second {
		t.Errorf("sleep duration: got %v, want 15s (backoff, not hold)", sleeps[0])
	}
}

// TestDispatchWithRetry_ZeroExitConsecutiveHoldsConsumeCapAndFail verifies
// issue #565's second acceptance criterion: consecutive zero-exit rate-limit
// holds that never recover count against the transient retry cap, landing on
// Success=false rather than a silent or confusing status=missing.
func TestDispatchWithRetry_ZeroExitConsecutiveHoldsConsumeCapAndFail(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(30 * time.Minute)

	fr := runner.NewFake() // always exits zero, never writes an outcome line
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps)) // max=3

	result := d.Run()

	if result.Success {
		t.Error("want Success=false (cap exhausted), got true")
	}
	if len(fr.RunCalls) != 4 {
		t.Errorf("RunCalls: got %d, want 4", len(fr.RunCalls))
	}
	if len(sleeps) != 3 {
		t.Errorf("sleep calls: got %d, want 3 (one per hold before cap)", len(sleeps))
	}
}

// TestDispatchWithRetry_ZeroExitTransientSkipsRetryWhenPRExists verifies
// issue #565's safety guard: a zero-exit, no-outcome box that classifies as
// transient is NOT re-dispatched when OpenPRForIssue reports an open PR
// already exists for the branch -- the box's work already landed, so retrying
// would duplicate it. The Result passes through unchanged, exactly as before
// #565, letting settle's own PR lookup route it.
func TestDispatchWithRetry_ZeroExitTransientSkipsRetryWhenPRExists(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(2 * time.Hour)

	fr := runner.NewFake() // always exits zero, never writes an outcome line
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	cfg := retryConfig(3, 0, 0)
	cfg.OpenPRForIssue = func(string) (bool, error) { return true, nil }
	d := newTestDispatch(t, cfg, fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Run()

	if !result.Success {
		t.Error("want Success=true (zero exit passthrough), got false")
	}
	if result.OutcomeFound {
		t.Error("want OutcomeFound=false")
	}
	if result.Classification.Reason != driver.RateLimit {
		t.Errorf("Classification: got %+v, want RateLimit passthrough", result.Classification)
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no re-dispatch when a PR already exists)", len(fr.RunCalls))
	}
	if len(sleeps) != 0 {
		t.Errorf("sleep calls: got %d, want 0 (no hold when a PR already exists)", len(sleeps))
	}
}

// TestDispatchWithRetry_AppliesToFixToo verifies the behavior change called
// out in issue #441: a 429 during a fix pass now holds until reset instead
// of burning a fix attempt, because the retry policy applies uniformly to
// Fix as it does to Run.
func TestDispatchWithRetry_AppliesToFixToo(t *testing.T) {
	fixedNow := time.Unix(1_000_000, 0).UTC()
	resetAt := fixedNow.Add(1 * time.Hour)

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // fix pass fails once (429), then succeeds
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		return driver.Classification{Class: driver.Transient, Reason: driver.RateLimit, ResetAt: &resetAt}, nil
	}}
	var sleeps []time.Duration
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, fakeClock(fixedNow, &sleeps))

	result := d.Fix(1, "ci failure detail")

	if !result.Success {
		t.Error("want Success=true (fix succeeded after hold), got false")
	}
	if len(fr.RunCalls) != 2 {
		t.Errorf("RunCalls: got %d, want 2 (initial fix attempt + hold re-dispatch)", len(fr.RunCalls))
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep calls: got %d, want 1 (held instead of burning the fix attempt)", len(sleeps))
	}
	if sleeps[0] != 1*time.Hour {
		t.Errorf("sleep duration: got %v, want 1h (hold until reset)", sleeps[0])
	}
}
