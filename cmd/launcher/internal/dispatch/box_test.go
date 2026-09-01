package dispatch

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/runner"
)

// TestRunOnce_PreservesPriorAttemptLogOnRetry verifies that a retried
// dispatch does not destroy the failed first attempt's log output: the
// prior attempt's content survives on disk, the current log holds only the
// latest attempt, and classification of the second attempt is not confused
// by a transient marker left over from the first (issue #561).
func TestRunOnce_PreservesPriorAttemptLogOnRetry(t *testing.T) {
	fr := runner.NewFake()
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		calls++
		if calls == 1 {
			box.Output.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error"}}` + "\n")) //nolint:errcheck
			return boxErr
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	d.driver = drv // exercise the real classifier against on-disk content

	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if !result.Resolved.Found || result.Resolved.Outcome.Status != "ready" {
		t.Fatalf("Run: want ready outcome, got %+v", result)
	}
	if calls != 2 {
		t.Fatalf("RunFunc calls: got %d, want 2", calls)
	}

	cur, err := os.ReadFile(d.logPath())
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if strings.Contains(string(cur), "rate_limit_error") {
		t.Errorf("current log still carries the first attempt's marker: %q", cur)
	}

	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	preserved := false
	for _, e := range entries {
		if e.Name() == filepath.Base(d.logPath()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if strings.Contains(string(b), "rate_limit_error") {
			preserved = true
		}
	}
	if !preserved {
		t.Error("first attempt's output was not preserved on disk")
	}
}

// TestRunOnce_RotatesPreExistingLogFromDuplicateLaunch verifies that a fresh
// dispatch does not truncate a log file already sitting at logPath -- the
// scenario the issue calls out explicitly: a duplicate/collided launch
// finding another attempt's log already there.
func TestRunOnce_RotatesPreExistingLogFromDuplicateLaunch(t *testing.T) {
	fr := runner.NewFake()

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	staleContent := "podman: name conflict, an earlier live Box's streaming output\n"
	if err := writeFile(d.logPath(), staleContent); err != nil {
		t.Fatalf("seed stale log: %v", err)
	}

	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	cur, err := os.ReadFile(d.logPath())
	if err != nil {
		t.Fatalf("read current log: %v", err)
	}
	if strings.Contains(string(cur), "podman") {
		t.Errorf("current log still carries the pre-existing content: %q", cur)
	}

	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	preserved := false
	for _, e := range entries {
		if e.Name() == filepath.Base(d.logPath()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if string(b) == staleContent {
			preserved = true
		}
	}
	if !preserved {
		t.Error("pre-existing log was not preserved on disk")
	}
}

// TestRun_QuarantinesPriorRunLogsBeforeFirstAttempt verifies that a fresh
// Run() call does not charge this run for a wholly unrelated EARLIER run's
// spend left on disk at the same log paths -- the scenario a re-dispatch of
// the same issue in a persistent pwd produces (agent-failed -> re-label,
// waves/continuous): the earlier run's bare initial log, its own rotated
// retry sibling, and a leftover fix-pass log all pre-date this Run() call
// entirely. AllAttemptLogPaths (and so CumulativeUsage/UsageReport) must
// reflect only the usage this fresh attempt itself produced, not the prior
// run's, even though the prior run's content still survives on disk
// afterward (issue #561's preserve intent) under a different name (issue
// #2575).
func TestRun_QuarantinesPriorRunLogsBeforeFirstAttempt(t *testing.T) {
	fr := runner.NewFake()

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	fr.WriteToOutput = nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok")

	priorInitial := `{"type":"result","num_turns":1,"total_cost_usd":9.00,"usage":{"input_tokens":90000,"output_tokens":9000}}` + "\n"
	priorRetry := `{"type":"result","num_turns":1,"total_cost_usd":5.00,"usage":{"input_tokens":50000,"output_tokens":5000}}` + "\n"
	priorFix := `{"type":"result","num_turns":1,"total_cost_usd":3.00,"usage":{"input_tokens":30000,"output_tokens":3000}}` + "\n"
	priorConflictResolve := `{"type":"result","num_turns":1,"total_cost_usd":1.00,"usage":{"input_tokens":10000,"output_tokens":1000}}` + "\n"
	if err := writeFile(d.logPath(), priorInitial); err != nil {
		t.Fatalf("seed prior initial log: %v", err)
	}
	if err := writeFile(d.logPath()+".1", priorRetry); err != nil {
		t.Fatalf("seed prior retry log: %v", err)
	}
	if err := writeFile(d.fixLogPath(1), priorFix); err != nil {
		t.Fatalf("seed prior fix log: %v", err)
	}
	if err := writeFile(d.conflictLogPath(), priorConflictResolve); err != nil {
		t.Fatalf("seed prior conflict-resolve log: %v", err)
	}

	drv, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	d.driver = drv

	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	got := d.CumulativeUsage()
	if got.InputTokens != 0 {
		t.Errorf("CumulativeUsage.InputTokens = %d, want 0 (the fresh attempt's status-line-only log has no result event, and the prior run's usage must not be charged)", got.InputTokens)
	}
	if diff := got.TotalCostUSD; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("CumulativeUsage.TotalCostUSD = %v, want 0 (prior run's spend must not be charged to this run)", got.TotalCostUSD)
	}

	// The prior content must still be on disk somewhere -- quarantined, not
	// destroyed.
	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var foundInitial, foundRetry, foundFix, foundConflictResolve bool
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		switch string(b) {
		case priorInitial:
			foundInitial = true
		case priorRetry:
			foundRetry = true
		case priorFix:
			foundFix = true
		case priorConflictResolve:
			foundConflictResolve = true
		}
	}
	if !foundInitial || !foundRetry || !foundFix || !foundConflictResolve {
		t.Errorf("prior run's logs were not preserved on disk: initial=%v retry=%v fix=%v conflict-resolve=%v",
			foundInitial, foundRetry, foundFix, foundConflictResolve)
	}
}

// TestRun_QuarantineFailureDoesNotSettleOnStaleLog verifies that when
// quarantinePriorRunLogs itself fails (e.g. a filesystem permission
// problem renaming a prior run's log aside), Run() does not fall through to
// settledOutcome and parse whatever content is still sitting at logPath --
// content this run never produced, left over from the exact prior run
// quarantine was trying to move aside -- as if it were this run's own
// verdict (issue #2575). It must instead retry (this fixture's permission
// problem never clears, so every retry fails the same way) up to
// Policy.Max attempts, then report a definite failure with no resolved
// outcome at all, having never dispatched a box.
func TestRun_QuarantineFailureDoesNotSettleOnStaleLog(t *testing.T) {
	fr := runner.NewFake()

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	staleOutcome := nonceLine(d, "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/999 status=ready note=stale")
	if err := writeFile(d.logPath(), string(staleOutcome)); err != nil {
		t.Fatalf("seed stale prior-run log: %v", err)
	}

	logDir := HostLogDirFor(d.pwd)
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(logDir, 0o755); err != nil {
			t.Fatalf("chmod log dir writable: %v", err)
		}
	})

	result := d.Run()

	if result.Success {
		t.Errorf("Run: want Success=false on a quarantine failure, got %+v", result)
	}
	if result.Resolved.Found {
		t.Errorf("Run: want Resolved.Found=false (must not settle on the stale prior run's outcome), got %+v", result.Resolved)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("runner.Run: want 0 calls when quarantine fails before dispatch, got %d", len(fr.RunCalls))
	}
}

// TestRun_QuarantineFailureRetriesWithBackoffBeforeGivingUp verifies the
// degrade posture finding for issue #2575's quarantine step: a quarantine
// failure is a local filesystem hiccup, not a terminal give-up, so it must
// retry with the same linear backoff any other transient failure uses --
// Policy.Max attempts, each sleeping through the injected Clock --
// before finally giving up, rather than failing the whole dispatch outright
// on the very first failure.
func TestRun_QuarantineFailureRetriesWithBackoffBeforeGivingUp(t *testing.T) {
	fr := runner.NewFake()
	var sleeps []time.Duration
	clock := fakeClock(time.Now(), &sleeps)

	d := newTestDispatch(t, retryConfig(3, 5, 0), fr, fakeDriver{}, clock)

	logDir := HostLogDirFor(d.pwd)
	if err := os.Chmod(logDir, 0o555); err != nil {
		t.Fatalf("chmod log dir read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(logDir, 0o755); err != nil {
			t.Fatalf("chmod log dir writable: %v", err)
		}
	})

	result := d.Run()

	if result.Success {
		t.Errorf("Run: want Success=false once the retry cap is exhausted, got %+v", result)
	}
	if len(sleeps) != 3 {
		t.Errorf("Sleep calls = %d, want 3 (Policy.Max) -- a quarantine failure must retry with backoff, not give up on the first attempt", len(sleeps))
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("runner.Run: want 0 calls when quarantine keeps failing, got %d", len(fr.RunCalls))
	}
}

// TestQuarantinePriorRunLogs_BoundedOnNonNotExistStatError verifies
// quarantinePriorRunLogs' free-suffix probe returns a real error instead of
// looping forever when os.Stat(dest) fails with something other than "not
// found" (issue #2575). A self-referential symlink at the very first
// candidate destination (<path>.prior-run.1) makes os.Stat on it fail with
// ELOOP, which os.IsNotExist never reports as true, so a probe that treated
// anything but that specific case as "free slot, rename here" would spin
// n++ forever with no timeout or cap.
func TestQuarantinePriorRunLogs_BoundedOnNonNotExistStatError(t *testing.T) {
	fr := runner.NewFake()
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	if err := writeFile(d.logPath(), "prior content\n"); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	dest := d.logPath() + ".prior-run.1"
	if err := os.Symlink(filepath.Base(dest), dest); err != nil {
		t.Fatalf("seed self-referential symlink: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- quarantinePriorRunLogs(d.pwd, d.number, fr) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("quarantinePriorRunLogs: want a non-nil error on a non-NotExist stat failure, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quarantinePriorRunLogs: did not return -- free-suffix probe looped unbounded on a non-NotExist stat error")
	}
}

// TestQuarantinePriorRunLogs_NoOpWhenAlreadyRunning verifies the safe
// direction of the IsRunning guard: when the runner reports this issue's Box
// name is already running, quarantinePriorRunLogs must be a complete no-op
// -- it must not rename, or otherwise touch, any pre-existing log or its
// rotated .N sibling (issue #562 territory, mirrored by quarantine per its
// own doc comment).
func TestQuarantinePriorRunLogs_NoOpWhenAlreadyRunning(t *testing.T) {
	fr := runner.NewFake()
	fr.IsRunningRet = true

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	initialContent := "live run's initial attempt output\n"
	rotatedContent := "live run's rotated retry sibling\n"
	if err := writeFile(d.logPath(), initialContent); err != nil {
		t.Fatalf("seed initial log: %v", err)
	}
	if err := writeFile(d.logPath()+".1", rotatedContent); err != nil {
		t.Fatalf("seed rotated log: %v", err)
	}

	if err := quarantinePriorRunLogs(d.pwd, d.number, fr); err != nil {
		t.Fatalf("quarantinePriorRunLogs: %v", err)
	}

	cur, err := os.ReadFile(d.logPath())
	if err != nil {
		t.Fatalf("read initial log: %v", err)
	}
	if string(cur) != initialContent {
		t.Errorf("initial log was touched: got %q, want %q", cur, initialContent)
	}
	rotated, err := os.ReadFile(d.logPath() + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if string(rotated) != rotatedContent {
		t.Errorf("rotated log was touched: got %q, want %q", rotated, rotatedContent)
	}

	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "prior-run") {
			t.Errorf("quarantine created a .prior-run.N file while a container was running: %s", e.Name())
		}
	}
	if len(entries) != 2 {
		t.Errorf("expected exactly the 2 seeded files untouched, got entries=%v", entries)
	}
}

// TestQuarantinePriorRunLogs_HoldSleepBlindSpot pins a known, currently
// unfixed limitation rather than asserting it is correct: IsRunning only
// reports whether a container is running RIGHT NOW, so it cannot tell "no
// run in progress for this issue" apart from "a run for this same issue is
// between attempts (e.g. mid dispatchWithRetry hold-sleep after a 429, see
// retry.go) with no container currently running." fr.IsRunningRet=false
// here stands in for that mid-hold-sleep window. In that window a second,
// genuinely colliding Run() for the SAME issue number would call
// quarantinePriorRunLogs and -- exactly as this test verifies -- it
// proceeds to rename the first, still-live run's own logs aside as if they
// belonged to a wholly unrelated stale prior run. That is not correct
// behaviour, but it is the current behaviour, inherited from the same
// IsRunning blind spot runOnce already has (issue #562) and out of scope to
// close here (it needs a real cross-process lock, a separate piece of
// work). This test exists so a future change to this behaviour is a
// deliberate decision, not an accidental regression nobody noticed.
func TestQuarantinePriorRunLogs_HoldSleepBlindSpot(t *testing.T) {
	fr := runner.NewFake()
	fr.IsRunningRet = false

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	initialContent := "mid-hold-sleep run's initial attempt output\n"
	if err := writeFile(d.logPath(), initialContent); err != nil {
		t.Fatalf("seed initial log: %v", err)
	}

	if err := quarantinePriorRunLogs(d.pwd, d.number, fr); err != nil {
		t.Fatalf("quarantinePriorRunLogs: %v", err)
	}

	if _, err := os.Stat(d.logPath()); !os.IsNotExist(err) {
		t.Fatalf("logPath: want removed by quarantine (blind spot pinned), got err=%v", err)
	}

	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if !strings.Contains(e.Name(), "prior-run") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if string(b) == initialContent {
			found = true
		}
	}
	if !found {
		t.Error("expected the live, mid-hold-sleep run's log to be quarantined to a .prior-run.N file (known blind spot)")
	}
}

// TestEnsureRunLineage_QuarantinesWhenMarkerAbsent verifies the fix for the
// recover/adopt entry point's own quarantine gap (issue #2575):
// quarantinePriorRunLogs only ever ran from Run's own very first attempt
// (box.go), so a Dispatch built the way main.go's recoverByNumber builds one
// -- via Factory.New, then straight into CumulativeUsage/UsageReport/Fix
// through settle's SettleAdopted, with Run never called at all -- used to
// never quarantine anything. This seeds a rotated ".1" sibling directly to
// disk BEFORE constructing the Dispatch at all (simulating a leftover from
// an earlier, unrelated attempt sequence that no Run() call in this process
// ever quarantined) with no run-lineage marker present either, then shows
// that calling EnsureRunLineage -- exactly as recoverByNumber now does
// before touching CumulativeUsage/UsageReport/Fix -- quarantines that
// leftover aside before it can be folded into this cycle's usage, leaving
// CumulativeUsage at zero rather than silently inheriting someone else's
// spend.
func TestEnsureRunLineage_QuarantinesWhenMarkerAbsent(t *testing.T) {
	dir := tempLogDir(t)

	// A leftover from an earlier, unrelated attempt sequence -- written
	// straight to disk, before any Dispatch for this issue exists in this
	// process, so nothing has had a chance to quarantine it, and with no
	// run-lineage marker either.
	leftover := `{"type":"result","num_turns":1,"total_cost_usd":7.00,"usage":{"input_tokens":70000,"output_tokens":7000}}` + "\n"
	if err := writeFile(logPathFor(dir, "20")+".1", leftover); err != nil {
		t.Fatalf("seed leftover rotated log: %v", err)
	}

	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	// Mirrors main.go's recoverByNumber: Factory.New, then EnsureRunLineage,
	// then straight to CumulativeUsage -- Run is never called.
	d := f.New("20", "test issue")

	if err := d.EnsureRunLineage(); err != nil {
		t.Fatalf("EnsureRunLineage: %v", err)
	}

	got := d.CumulativeUsage()
	if got.InputTokens != 0 {
		t.Errorf("CumulativeUsage.InputTokens = %d, want 0 (EnsureRunLineage must quarantine the unmarked leftover before CumulativeUsage ever walks it)", got.InputTokens)
	}
	if got.TotalCostUSD != 0 {
		t.Errorf("CumulativeUsage.TotalCostUSD = %v, want 0 (EnsureRunLineage must quarantine the unmarked leftover before CumulativeUsage ever walks it)", got.TotalCostUSD)
	}

	if _, err := os.Stat(runLineageMarkerPath(dir, "20")); err != nil {
		t.Errorf("run-lineage marker: want created by EnsureRunLineage, stat error: %v", err)
	}
}

// TestEnsureRunLineage_TrustsExistingLogsWhenMarkerPresent verifies the
// common recover/adopt case: an open PR can only exist because some earlier
// Run(), in some earlier launcher process, already quarantined-then-marked
// this issue's log lineage (box.go's Run). When that marker is already on
// disk, EnsureRunLineage must be a complete no-op -- it must not touch any
// pass log -- so CumulativeUsage still sums this run's own genuine history
// across the process restart recover exists to survive.
func TestEnsureRunLineage_TrustsExistingLogsWhenMarkerPresent(t *testing.T) {
	dir := tempLogDir(t)

	initial := `{"type":"result","num_turns":1,"total_cost_usd":1.50,"usage":{"input_tokens":1000,"output_tokens":200}}` + "\n"
	if err := writeFile(logPathFor(dir, "20"), initial); err != nil {
		t.Fatalf("seed initial log: %v", err)
	}
	if err := markRunLineage(dir, "20"); err != nil {
		t.Fatalf("markRunLineage: %v", err)
	}

	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()
	d := f.New("20", "test issue")

	if err := d.EnsureRunLineage(); err != nil {
		t.Fatalf("EnsureRunLineage: %v", err)
	}

	got := d.CumulativeUsage()
	if got.InputTokens != 1000 {
		t.Errorf("CumulativeUsage.InputTokens = %d, want 1000 (marker present must trust the existing log, not quarantine it)", got.InputTokens)
	}
	if diff := got.TotalCostUSD - 1.50; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("CumulativeUsage.TotalCostUSD = %v, want ~1.50 (marker present must trust the existing log, not quarantine it)", got.TotalCostUSD)
	}
}

// TestRunOnce_SkipsAlreadyRunningContainerWithoutTouchingLog verifies that
// when the runner reports the box's container/sandbox name is already
// running, runOnce returns without ever rotating or creating the log file:
// the live run's per-issue log must stay exactly as it was found, and
// runner.Run must never be called (issue #562).
func TestRunOnce_SkipsAlreadyRunningContainerWithoutTouchingLog(t *testing.T) {
	fr := runner.NewFake()
	fr.IsRunningRet = true

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

	liveContent := "live Box still streaming output\n"
	if err := writeFile(d.logPath(), liveContent); err != nil {
		t.Fatalf("seed live log: %v", err)
	}

	result := d.Run()

	if result.Success {
		t.Fatalf("Run: want Success=false for an already-in-flight skip, got %+v", result)
	}
	if !result.AlreadyInFlight {
		t.Fatalf("Run: want AlreadyInFlight=true, got %+v", result)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("runner.Run: want 0 calls when already running, got %d", len(fr.RunCalls))
	}

	cur, err := os.ReadFile(d.logPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(cur) != liveContent {
		t.Errorf("log was touched by the skipped attempt: got %q, want %q", cur, liveContent)
	}

	dir := filepath.Dir(d.logPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected no rotated .N sibling from the skipped attempt; got entries=%v", entries)
	}
}

// TestRotateStaleLog_UsesFirstAvailableSuffix verifies that repeated
// rotations of the same logPath do not clobber each other -- each rotation
// picks the next unused .N suffix.
func TestRotateStaleLog_UsesFirstAvailableSuffix(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")

	if err := os.WriteFile(logPath, []byte("attempt 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateStaleLog(logPath); err != nil {
		t.Fatalf("rotateStaleLog (1st): %v", err)
	}
	if err := os.WriteFile(logPath, []byte("attempt 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateStaleLog(logPath); err != nil {
		t.Fatalf("rotateStaleLog (2nd): %v", err)
	}

	got1, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("read .1: %v", err)
	}
	if string(got1) != "attempt 1" {
		t.Errorf(".1 content: got %q, want %q", got1, "attempt 1")
	}
	got2, err := os.ReadFile(logPath + ".2")
	if err != nil {
		t.Fatalf("read .2: %v", err)
	}
	if string(got2) != "attempt 2" {
		t.Errorf(".2 content: got %q, want %q", got2, "attempt 2")
	}
}

// TestRotateStaleLog_NoOpWhenMissing verifies that rotating a path with no
// existing file is a no-op, not an error.
func TestRotateStaleLog_NoOpWhenMissing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")

	if err := rotateStaleLog(logPath); err != nil {
		t.Fatalf("rotateStaleLog: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("logPath: want still absent, got err=%v", err)
	}
}

// TestResetOutboxDir_CreatesOtherWritableDirectory verifies the outbox dir is
// mode 0o777 so the Box's uid-1000 agent user can write a seam bundle
// regardless of how rootless podman/docker remaps host-to-container
// ownership (issue #1723).
func TestResetOutboxDir_CreatesOtherWritableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")

	if err := resetOutboxDir(dir); err != nil {
		t.Fatalf("resetOutboxDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("dir mode: got %o, want %o", got, 0o777)
	}
}

// TestRunOnce_RegistryProxyUpstreamURLSet_MountsListeningSocket verifies that
// a set Config.RegistryProxyUpstreamURL (ADR 0044, issue #2849) starts a
// per-Box registry proxy before Run and hands the Box a non-empty
// RegistryProxySocketPath pointing at a real, listening unix socket that
// forwards through to the configured upstream.
func TestRunOnce_RegistryProxyUpstreamURLSet_MountsListeningSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyUpstreamURL = upstream.URL

	fr := runner.NewFake()
	var socketPath, proxiedBody string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxySocketPath
		if socketPath != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			}}
			resp, err := client.Get("http://unix/")
			if err != nil {
				t.Errorf("GET through registry proxy socket: %v", err)
			} else {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close() //nolint:errcheck
				proxiedBody = string(b)
			}
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if socketPath == "" {
		t.Fatal("box.RegistryProxySocketPath was empty with RegistryProxyUpstreamURL set")
	}
	if proxiedBody != "hello from upstream" {
		t.Errorf("proxied response body = %q, want %q", proxiedBody, "hello from upstream")
	}
}

// setLongTMPDir points $TMPDIR at a base long enough to overflow AF_UNIX's
// sun_path limit on any platform spindrift targets (104 darwin / 108 linux,
// see cmd/launcher/internal/registryproxy) once a generated temp dir name
// and "proxy.sock" are appended (issue #3077).
func setLongTMPDir(t *testing.T) {
	t.Helper()
	longBase := filepath.Join(t.TempDir(), strings.Repeat("x", 200))
	if err := os.MkdirAll(longBase, 0o755); err != nil {
		t.Fatalf("MkdirAll long TMPDIR base: %v", err)
	}
	t.Setenv("TMPDIR", longBase)
}

// TestRunOnce_RegistryProxyUpstreamURLSet_LongTMPDIR_StillWorks pins the
// issue #3077 acceptance criterion end-to-end: a $TMPDIR long enough to
// overflow AF_UNIX's sun_path limit once the generated proxy dir name and
// "proxy.sock" are appended -- the shape nix develop's own
// nix-shell.XXXXXX/ prefix nested under macOS's per-user $TMPDIR produces in
// practice -- must not break the registry proxy: Run still succeeds and the
// proxied request still round-trips through the mounted socket.
func TestRunOnce_RegistryProxyUpstreamURLSet_LongTMPDIR_StillWorks(t *testing.T) {
	setLongTMPDir(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyUpstreamURL = upstream.URL

	fr := runner.NewFake()
	var socketPath, proxiedBody string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxySocketPath
		if socketPath != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			}}
			resp, err := client.Get("http://unix/")
			if err != nil {
				t.Errorf("GET through registry proxy socket: %v", err)
			} else {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close() //nolint:errcheck
				proxiedBody = string(b)
			}
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if socketPath == "" {
		t.Fatal("box.RegistryProxySocketPath was empty with RegistryProxyUpstreamURL set")
	}
	if proxiedBody != "hello from upstream" {
		t.Errorf("proxied response body = %q, want %q", proxiedBody, "hello from upstream")
	}
}

// TestRunOnce_RegistryProxyCredentialSet_AttachesAuthorizationHeader verifies
// that a set Config.RegistryProxyCredential (ADR 0044, issue #2850) reaches
// the outbound leg through the real wiring -- Run through the Box's mounted
// socket to a local upstream that echoes back the Authorization header it
// received -- proving the credential travels from Config all the way to the
// request the proxy sends upstream, not just through registryproxy.New's own
// unit tests.
func TestRunOnce_RegistryProxyCredentialSet_AttachesAuthorizationHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	const credential = "s3kr1t-e2e-token"

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyUpstreamURL = upstream.URL
	cfg.RegistryProxyCredential = credential

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.RegistryProxySocketPath != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", box.RegistryProxySocketPath)
				},
			}}
			resp, err := client.Get("http://unix/")
			if err != nil {
				t.Errorf("GET through registry proxy socket: %v", err)
			} else {
				resp.Body.Close() //nolint:errcheck
			}
		}
		for k, v := range box.Env {
			if v == credential {
				t.Errorf("box.Env[%q] leaked the registry proxy credential into the Box environment", k)
			}
			if strings.Contains(strings.ToUpper(k), "REGISTRY_PROXY_CREDENTIAL") {
				t.Errorf("box.Env contains unexpected registry-proxy-credential-like key %q", k)
			}
		}
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if want := "Bearer " + credential; gotAuth != want {
		t.Errorf("upstream got Authorization %q, want %q", gotAuth, want)
	}
}

// TestRunOnce_RegistryProxyUpstreamURLUnset_NoSocketNoProxy verifies that an
// empty Config.RegistryProxyUpstreamURL leaves the Box's
// RegistryProxySocketPath empty and starts no proxy -- no spindrift-registry-
// proxy-* temp dir is left on disk once Run returns.
func TestRunOnce_RegistryProxyUpstreamURLUnset_NoSocketNoProxy(t *testing.T) {
	tmpDirBefore, _ := filepath.Glob(filepath.Join(os.TempDir(), "spindrift-registry-proxy-*"))

	fr := runner.NewFake()
	var socketPath string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxySocketPath
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if socketPath != "" {
		t.Errorf("box.RegistryProxySocketPath = %q, want empty when RegistryProxyUpstreamURL is unset", socketPath)
	}

	tmpDirAfter, _ := filepath.Glob(filepath.Join(os.TempDir(), "spindrift-registry-proxy-*"))
	if len(tmpDirAfter) != len(tmpDirBefore) {
		t.Errorf("leftover spindrift-registry-proxy-* temp dir(s) under os.TempDir(): before=%v after=%v", tmpDirBefore, tmpDirAfter)
	}
}

// TestRegistryProxySocketDir_ReturnsUsableDir verifies that, under whatever
// $TMPDIR the test environment already has, registryProxySocketDir returns a
// directory that exists and whose "proxy.sock" join fits the AF_UNIX
// sun_path limit.
func TestRegistryProxySocketDir_ReturnsUsableDir(t *testing.T) {
	dir, err := registryProxySocketDir()
	if err != nil {
		t.Fatalf("registryProxySocketDir: %v", err)
	}
	defer os.RemoveAll(dir)

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("returned dir does not exist: %v", err)
	}
	if sock := filepath.Join(dir, registryProxySocketFile); registryproxy.TooLongForUnixSocket(sock) {
		t.Errorf("proxy.sock join %q (%d bytes) is too long for a unix socket", sock, len(sock))
	}
}

// TestRegistryProxySocketDir_LongTMPDIR_FallsBackToTmp verifies the reported
// bug (issue #3077): a $TMPDIR long enough that os.TempDir()-based candidate
// would overflow AF_UNIX's sun_path limit once "spindrift-registry-proxy-*/
// proxy.sock" is appended -- the case nix develop's own
// nix-shell.XXXXXX/ prefix nested under macOS's per-user $TMPDIR triggers in
// practice -- is rescued by falling back to /tmp instead of failing.
func TestRegistryProxySocketDir_LongTMPDIR_FallsBackToTmp(t *testing.T) {
	setLongTMPDir(t)

	dir, err := registryProxySocketDir()
	if err != nil {
		t.Fatalf("registryProxySocketDir: %v", err)
	}
	defer os.RemoveAll(dir)

	if sock := filepath.Join(dir, registryProxySocketFile); registryproxy.TooLongForUnixSocket(sock) {
		t.Errorf("registryProxySocketDir did not fall back: proxy.sock join %q (%d bytes) is still too long", sock, len(sock))
	}
	if wantPrefix := "/tmp" + string(filepath.Separator); !strings.HasPrefix(dir, wantPrefix) {
		t.Errorf("registryProxySocketDir did not fall back to /tmp: dir = %q, want prefix %q", dir, wantPrefix)
	}
}

// TestRegistryProxySocketDir_NonexistentTMPDIR_ReturnsError verifies that a
// $TMPDIR whose os.MkdirTemp fails for a reason other than the issue #3077
// length overflow (here: the base directory does not exist) surfaces that
// error to the caller instead of being silently rerouted to a fresh /tmp
// fallback -- only the length check should ever fall back to /tmp.
func TestRegistryProxySocketDir_NonexistentTMPDIR_ReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("TMPDIR", missing)

	before, _ := filepath.Glob(filepath.Join("/tmp", "spindrift-registry-proxy-*"))

	dir, err := registryProxySocketDir()
	if err == nil {
		os.RemoveAll(dir)
		t.Fatalf("registryProxySocketDir: want error for nonexistent TMPDIR, got dir %q", dir)
	}

	after, _ := filepath.Glob(filepath.Join("/tmp", "spindrift-registry-proxy-*"))
	if len(after) != len(before) {
		t.Errorf("registryProxySocketDir swallowed the error into a /tmp fallback: before=%v after=%v", before, after)
	}
}
