package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
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
	if err := writeFile(d.logPath(), priorInitial); err != nil {
		t.Fatalf("seed prior initial log: %v", err)
	}
	if err := writeFile(d.logPath()+".1", priorRetry); err != nil {
		t.Fatalf("seed prior retry log: %v", err)
	}
	if err := writeFile(d.fixLogPath(1), priorFix); err != nil {
		t.Fatalf("seed prior fix log: %v", err)
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
	var foundInitial, foundRetry, foundFix bool
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
		}
	}
	if !foundInitial || !foundRetry || !foundFix {
		t.Errorf("prior run's logs were not preserved on disk: initial=%v retry=%v fix=%v", foundInitial, foundRetry, foundFix)
	}
}

// TestRun_QuarantineFailureDoesNotSettleOnStaleLog verifies that when
// quarantinePriorRunLogs itself fails (e.g. a filesystem permission
// problem renaming a prior run's log aside), Run() does not fall through to
// settledOutcome and parse whatever content is still sitting at logPath --
// content this run never produced, left over from the exact prior run
// quarantine was trying to move aside -- as if it were this run's own
// verdict (issue #2575). It must instead report a definite failure with no
// resolved outcome at all, and must never dispatch a box.
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
