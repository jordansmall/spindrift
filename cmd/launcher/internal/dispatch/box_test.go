package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/unixsocket"
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
// dispatch does not truncate a log file already sitting at logPath: a
// duplicate or collided launch finding another attempt's log already there.
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
// Run() is not charged for an earlier run's spend left at the same log
// paths, the way a re-dispatch of the same issue in a persistent pwd
// produces. CumulativeUsage counts only this attempt, though the prior
// content still survives on disk under a different name (issues #561, #2575).
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
// quarantinePriorRunLogs fails, Run() does not fall through to
// settledOutcome and parse the prior run's leftover content at logPath as
// this run's own verdict (issue #2575). This fixture's permission problem
// never clears, so every retry fails the same way and no box is dispatched.
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

// TestRun_QuarantineFailureRetriesWithBackoffBeforeGivingUp verifies that a
// quarantine failure is a local filesystem hiccup, not a terminal give-up:
// it retries with the same linear backoff any other transient failure uses,
// Policy.Max attempts each sleeping through the injected Clock, rather than
// failing the whole dispatch on the first failure (issue #2575).
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

// TestQuarantinePriorRunLogs_BoundedOnNonNotExistStatError verifies the
// free-suffix probe returns a real error instead of looping forever when
// os.Stat(dest) fails with something other than "not found" (issue #2575).
// A self-referential symlink at the first candidate makes os.Stat fail with
// ELOOP, which os.IsNotExist never reports as true.
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
// name is already running, quarantinePriorRunLogs must not rename or
// otherwise touch any pre-existing log or its rotated .N sibling (issue
// #562).
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

// TestQuarantinePriorRunLogs_HoldSleepBlindSpot pins a known, unfixed
// limitation rather than correct behaviour: IsRunning cannot tell "no run in
// progress" apart from "a run for this issue is mid hold-sleep with no
// container up", so a colliding Run() renames the still-live run's logs aside.
// runOnce has the same blind spot (issue #562); closing it needs a real lock.
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
// quarantinePriorRunLogs only ever ran from Run's first attempt, so a
// Dispatch built the way main.go's recoverByNumber builds one inherited an
// unmarked leftover's spend. EnsureRunLineage quarantines it first.
func TestEnsureRunLineage_QuarantinesWhenMarkerAbsent(t *testing.T) {
	dir := tempLogDir(t)

	// This leftover lands on disk before any Dispatch for this issue exists in
	// the process, so nothing has had a chance to quarantine it, and it carries
	// no run-lineage marker either.
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
	// then straight to CumulativeUsage, with Run never called.
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
// common recover/adopt case: an open PR can only exist because an earlier
// Run(), in an earlier launcher process, already quarantined then marked
// this issue's log lineage. With that marker on disk, EnsureRunLineage must
// touch no pass log, so CumulativeUsage still sums this run's own history.
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
// rotations of the same logPath do not clobber each other: each rotation
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
// a non-empty Config.RegistryProxyRoutes (ADR 0044, issue #2849) starts a
// per-Box proxy and hands the Box a listening unix socket that forwards to
// the configured upstream, and that the TCP-fallback env keys (issue #3111)
// stay absent from box.Env on this socket-capable branch.
func TestRunOnce_RegistryProxyUpstreamURLSet_MountsListeningSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	var socketPath, proxiedBody string
	var envSnapshot map[string]string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxy.Endpoint.SocketPath()
		envSnapshot = box.Env
		if socketPath != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			}}
			resp, err := client.Get("http://unix/" + cfg.RegistryProxyRoutes[0].Prefix + "/")
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
		t.Fatal("box.RegistryProxy.Endpoint.SocketPath() was empty with RegistryProxyRoutes set")
	}
	if proxiedBody != "hello from upstream" {
		t.Errorf("proxied response body = %q, want %q", proxiedBody, "hello from upstream")
	}
	for _, key := range []string{"REGISTRY_PROXY_TCP_HOST", "REGISTRY_PROXY_TCP_PORT", "REGISTRY_PROXY_TCP_SECRET"} {
		if _, ok := envSnapshot[key]; ok {
			t.Errorf("box.Env contains %q = %q on the socket-capable branch, want absent", key, envSnapshot[key])
		}
	}
}

// TestRunOnce_RegistryProxyUnixTransport_SocketsHasProxyEntry verifies that
// under a unix transport verdict, box.Sockets carries exactly one entry
// whose Source is the same host path RegistryProxy.Endpoint.SocketPath()
// mints and whose Target is runner.RegistryProxySocketTarget (issue #3723):
// the mount layer reads Sockets, not RegistryProxy, so the registry proxy
// socket must appear there or it never reaches the Box.
func TestRunOnce_RegistryProxyUnixTransport_SocketsHasProxyEntry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	var sockets []runner.SocketMount
	var proxySocketPath string
	var dialErr error
	fr.RunFunc = func(box runner.Box) error {
		sockets = box.Sockets
		proxySocketPath = box.RegistryProxy.Endpoint.SocketPath()
		// Dial while the proxy is still listening: the proxy and its
		// socket dir are torn down by defers that fire once runOnce
		// returns, so dialing after d.Run() completes would race a
		// closed listener.
		if len(sockets) == 1 {
			var conn net.Conn
			conn, dialErr = net.Dial("unix", sockets[0].Source)
			if dialErr == nil {
				conn.Close() //nolint:errcheck
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
	if proxySocketPath == "" {
		t.Fatal("box.RegistryProxy.Endpoint.SocketPath() was empty, want a minted host path")
	}
	if len(sockets) != 1 {
		t.Fatalf("box.Sockets = %+v, want exactly one entry", sockets)
	}
	if sockets[0].Source != proxySocketPath {
		t.Errorf("box.Sockets[0].Source = %q, want %q (RegistryProxy.Endpoint.SocketPath())", sockets[0].Source, proxySocketPath)
	}
	if sockets[0].Target != runner.RegistryProxySocketTarget {
		t.Errorf("box.Sockets[0].Target = %q, want %q", sockets[0].Target, runner.RegistryProxySocketTarget)
	}
	if dialErr != nil {
		t.Errorf("dial box.Sockets[0].Source %q: %v, want a connectable listening socket", sockets[0].Source, dialErr)
	}
}

// TestRunOnce_RegistryProxyTCPTransport_SocketsEmpty verifies that under a
// TCP transport verdict the Box dials the proxy directly, so box.Sockets
// stays empty rather than carrying a meaningless mount entry (issue #3723).
func TestRunOnce_RegistryProxyTCPTransport_SocketsEmpty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("host.docker.internal", "")
	var sockets []runner.SocketMount
	fr.RunFunc = func(box runner.Box) error {
		sockets = box.Sockets
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if len(sockets) != 0 {
		t.Errorf("box.Sockets = %+v, want empty on the TCP transport branch", sockets)
	}
}

// TestRunOnce_NoRegistryProxyRoutes_SocketsEmpty verifies that with no
// RegistryProxyRoutes configured, box.Sockets stays empty: the feature is
// off entirely, so nothing mints a socket to list (issue #3723).
func TestRunOnce_NoRegistryProxyRoutes_SocketsEmpty(t *testing.T) {
	fr := runner.NewFake()
	var sockets []runner.SocketMount
	fr.RunFunc = func(box runner.Box) error {
		sockets = box.Sockets
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if len(sockets) != 0 {
		t.Errorf("box.Sockets = %+v, want empty when RegistryProxyRoutes is unset", sockets)
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

// stubRegistryProxyMkdirTemp points the mkdir seam at fn for the duration of
// the test. No test in this package calls t.Parallel(), so swapping the
// package-level var cannot be observed by a concurrently running test.
func stubRegistryProxyMkdirTemp(t *testing.T, fn func(base, pattern string) (string, error)) {
	t.Helper()
	orig := registryProxyMkdirTemp
	t.Cleanup(func() { registryProxyMkdirTemp = orig })
	registryProxyMkdirTemp = fn
}

// TestRunOnce_RegistryProxyUpstreamURLSet_LongTMPDIR_StillWorks pins issue
// #3077 end-to-end: a $TMPDIR long enough to overflow AF_UNIX's sun_path
// limit once the generated proxy dir name and "proxy.sock" are appended, the
// shape nix develop's nix-shell.XXXXXX/ prefix under macOS's per-user
// $TMPDIR produces, must still let the request round-trip.
func TestRunOnce_RegistryProxyUpstreamURLSet_LongTMPDIR_StillWorks(t *testing.T) {
	setLongTMPDir(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	var socketPath, proxiedBody string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxy.Endpoint.SocketPath()
		if socketPath != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			}}
			resp, err := client.Get("http://unix/" + cfg.RegistryProxyRoutes[0].Prefix + "/")
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
		t.Fatal("box.RegistryProxy.Endpoint.SocketPath() was empty with RegistryProxyRoutes set")
	}
	if proxiedBody != "hello from upstream" {
		t.Errorf("proxied response body = %q, want %q", proxiedBody, "hello from upstream")
	}
}

// TestRunOnce_RegistryProxyCredentialSet_AttachesAuthorizationHeader verifies
// that a route Credential in Config.RegistryProxyRoutes (ADR 0044, issue
// #2850) reaches the outbound leg through the real wiring: Run through the
// Box's mounted socket to a local upstream that echoes the Authorization
// header back, not just through registryproxy.New's own unit tests.
func TestRunOnce_RegistryProxyCredentialSet_AttachesAuthorizationHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	const credential = "s3kr1t-e2e-token"

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, Credential: credential, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	fr.RunFunc = func(box runner.Box) error {
		if box.RegistryProxy.Endpoint.SocketPath() != "" {
			client := &http.Client{Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", box.RegistryProxy.Endpoint.SocketPath())
				},
			}}
			resp, err := client.Get("http://unix/" + cfg.RegistryProxyRoutes[0].Prefix + "/")
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
// empty Config.RegistryProxyRoutes leaves the Box's RegistryProxy.Endpoint
// zero and starts no proxy, pinned by a seam call count rather than by
// diffing os.TempDir() globs, and that the transport probe (issue #3111)
// never runs: it costs a live exec, so it must be skipped, not discarded.
func TestRunOnce_RegistryProxyUpstreamURLUnset_NoSocketNoProxy(t *testing.T) {
	var mkdirTempCalls int
	stubRegistryProxyMkdirTemp(t, func(base, pattern string) (string, error) {
		mkdirTempCalls++
		return os.MkdirTemp(base, pattern)
	})

	fr := runner.NewFake()
	var socketPath string
	fr.RunFunc = func(box runner.Box) error {
		socketPath = box.RegistryProxy.Endpoint.SocketPath()
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if socketPath != "" {
		t.Errorf("box.RegistryProxy.Endpoint.SocketPath() = %q, want empty when RegistryProxyRoutes is empty", socketPath)
	}

	if fr.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 when RegistryProxyRoutes is empty", fr.RegistryProxyTransportCalls)
	}

	if mkdirTempCalls != 0 {
		t.Errorf("registryProxyMkdirTemp calls = %d, want 0 when RegistryProxyRoutes is empty", mkdirTempCalls)
	}
}

// TestRunOnce_RegistryProxyTransportErrors_AbortsDispatch verifies that when
// the runner's transport probe itself fails (issue #3111), runOnce aborts
// the dispatch with a wrapped error instead of falling through to some
// default transport: the Box must never run with a transport nobody
// confirmed works.
func TestRunOnce_RegistryProxyTransportErrors_AbortsDispatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	probeErr := errors.New("probe: exec failed")
	fr := runner.NewFake()
	fr.RegistryProxyTransportErr = probeErr

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())

	env, err := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	if err != nil {
		t.Fatalf("buildBoxEnv: unexpected error: %v", err)
	}
	err = d.runOnce(d.logPath(), env, d.cacheDir)

	if err == nil {
		t.Fatal("runOnce: want a non-nil error when the transport probe fails, got nil")
	}
	if !errors.Is(err, probeErr) {
		t.Errorf("runOnce error = %v, want it to wrap %v", err, probeErr)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %d, want 0: the Box must never run when the transport probe itself errors", len(fr.RunCalls))
	}
}

// TestRunOnce_RegistryProxyTransportSocketIncapable_MountsTCPLocation verifies
// that when the runner cannot carry a connectable unix socket into the Box
// (issue #3111), runOnce falls back to TCP. Host and port reach the guest
// through REGISTRY_PROXY_MANIFEST (ADR 0045), so REGISTRY_PROXY_TCP_HOST and
// _PORT stay absent from box.Env while the secret is forwarded on its own.
func TestRunOnce_RegistryProxyTransportSocketIncapable_MountsTCPLocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello from upstream")) //nolint:errcheck
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("host.docker.internal", "")

	var loc runner.RegistryProxyLocation
	var envSnapshot map[string]string
	var proxiedBody string
	fr.RunFunc = func(box runner.Box) error {
		loc = box.RegistryProxy
		envSnapshot = box.Env
		if loc.Endpoint.Host() != "" {
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%s/%s/", loc.Endpoint.Port(), cfg.RegistryProxyRoutes[0].Prefix), nil)
			if err != nil {
				t.Fatalf("build TCP proxy request: %v", err)
			}
			req.Header.Set(registrymanifest.TCPSecretHeader, loc.TCPSecret)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("GET through registry proxy TCP port: %v", err)
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
	if loc.Endpoint.SocketPath() != "" {
		t.Errorf("box.RegistryProxy.Endpoint.SocketPath() = %q, want empty on the TCP transport branch", loc.Endpoint.SocketPath())
	}
	if loc.Endpoint.Host() != "host.docker.internal" {
		t.Errorf("box.RegistryProxy.Endpoint.Host() = %q, want %q", loc.Endpoint.Host(), "host.docker.internal")
	}
	if loc.Endpoint.Port() == "" || loc.Endpoint.Port() == "0" {
		t.Errorf("box.RegistryProxy.Endpoint.Port() = %q, want a bound ephemeral port", loc.Endpoint.Port())
	}
	if loc.TCPSecret == "" {
		t.Error("box.RegistryProxy.TCPSecret was empty, want a minted per-run secret")
	}
	if proxiedBody != "hello from upstream" {
		t.Errorf("proxied response body = %q, want %q", proxiedBody, "hello from upstream")
	}

	for _, key := range []string{"REGISTRY_PROXY_TCP_HOST", "REGISTRY_PROXY_TCP_PORT"} {
		if _, ok := envSnapshot[key]; ok {
			t.Errorf("box.Env contains %q = %q, want absent now REGISTRY_PROXY_MANIFEST carries the endpoint (ADR 0045)", key, envSnapshot[key])
		}
	}
	if got := envSnapshot["REGISTRY_PROXY_TCP_SECRET"]; got != loc.TCPSecret {
		t.Errorf("box.Env[REGISTRY_PROXY_TCP_SECRET] = %q, want %q", got, loc.TCPSecret)
	}

	raw, ok := envSnapshot[registrymanifest.EnvVar]
	if !ok || raw == "" {
		t.Fatal("box.Env[REGISTRY_PROXY_MANIFEST] was absent/empty on the TCP branch")
	}
	manifest, err := registrymanifest.Parse(raw)
	if err != nil {
		t.Fatalf("registrymanifest.Parse(box.Env[REGISTRY_PROXY_MANIFEST]): %v", err)
	}
	if !manifest.Endpoint.IsTCP() || manifest.Endpoint.Host() != loc.Endpoint.Host() || manifest.Endpoint.Port() != loc.Endpoint.Port() {
		t.Errorf("manifest.Endpoint = %+v, want a tcp endpoint matching box.RegistryProxy.Endpoint %+v", manifest.Endpoint, loc.Endpoint)
	}
}

// TestRunOnce_RegistryProxyTransportSocketIncapable_SecretDiffersPerRun
// verifies the minted TCP secret (issue #3111) is genuinely per-run, not a
// fixed or reused value.
func TestRunOnce_RegistryProxyTransportSocketIncapable_SecretDiffersPerRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	secretFor := func() string {
		fr := runner.NewFake()
		fr.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("host.docker.internal", "")

		var secret string
		fr.RunFunc = func(box runner.Box) error {
			secret = box.RegistryProxy.TCPSecret
			box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
			return nil
		}

		d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
		if result := d.Run(); !result.Success {
			t.Fatalf("Run: want Success=true, got %+v", result)
		}
		return secret
	}

	first := secretFor()
	second := secretFor()
	if first == "" || second == "" {
		t.Fatalf("expected non-empty secrets, got first=%q second=%q", first, second)
	}
	if first == second {
		t.Errorf("two separate runOnce dispatches minted the same TCP secret %q, want distinct per-run values", first)
	}
}

// TestRunOnce_RegistryProxyManifest_UnixEndpoint verifies runOnce mints
// REGISTRY_PROXY_MANIFEST (ADR 0045) on the unix branch: its Endpoint names
// the fixed in-box mount target, not the host-side mount-source path a
// Box-side reader could never dial (issue #3141), and its Routes carry each
// route's own Prefix from registryproxy.AssignPrefixes.
func TestRunOnce_RegistryProxyManifest_UnixEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := retryConfig(3, 0, 0)
	cfg.RegistryProxyRoutes = registryproxy.AssignPrefixes([]registryproxy.Route{{Upstream: upstream.URL, EnforcedPaths: []string{"/"}}})

	fr := runner.NewFake()
	fr.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")

	var envSnapshot map[string]string
	var socketPath string
	fr.RunFunc = func(box runner.Box) error {
		envSnapshot = box.Env
		socketPath = box.RegistryProxy.Endpoint.SocketPath()
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n")) //nolint:errcheck
		return nil
	}

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())
	result := d.Run()
	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	raw, ok := envSnapshot[registrymanifest.EnvVar]
	if !ok || raw == "" {
		t.Fatal("box.Env[REGISTRY_PROXY_MANIFEST] was absent/empty with RegistryProxyRoutes set")
	}
	manifest, err := registrymanifest.Parse(raw)
	if err != nil {
		t.Fatalf("registrymanifest.Parse(box.Env[REGISTRY_PROXY_MANIFEST]): %v", err)
	}
	if socketPath == "" {
		t.Fatal("box.RegistryProxy.Endpoint.SocketPath() was empty, want the launcher's own mount-source path")
	}
	if manifest.Endpoint.SocketPath() == socketPath {
		t.Errorf("manifest.Endpoint.SocketPath() = %q, want it to differ from the host-side mount source %q (a Box-side reader can't dial a host path)", manifest.Endpoint.SocketPath(), socketPath)
	}
	if !manifest.Endpoint.IsUnix() || manifest.Endpoint.SocketPath() != runner.RegistryProxySocketTarget {
		t.Errorf("manifest.Endpoint = %+v, want a unix endpoint at the fixed in-box target %q", manifest.Endpoint, runner.RegistryProxySocketTarget)
	}
	if len(manifest.Routes) != 1 {
		t.Fatalf("manifest.Routes = %+v, want exactly 1 route", manifest.Routes)
	}
	if manifest.Routes[0].Prefix != "r0" {
		t.Errorf("manifest.Routes[0].Prefix = %q, want %q", manifest.Routes[0].Prefix, "r0")
	}
	wantHost := strings.TrimPrefix(upstream.URL, "http://")
	if manifest.Routes[0].UpstreamHost != wantHost {
		t.Errorf("manifest.Routes[0].UpstreamHost = %q, want %q", manifest.Routes[0].UpstreamHost, wantHost)
	}
}

// TestRegistryManifestRoutes_ProjectsPrefixAndEcosystems verifies that
// registryManifestRoutes carries each route's own Prefix and whole Ecosystems
// declaration block into the manifest Route (issue #3404): two routes with
// distinct prefixes and cargo names each land on the matching entry, not
// swapped or collapsed onto one shared value.
func TestRegistryManifestRoutes_ProjectsPrefixAndEcosystems(t *testing.T) {
	routes := []registryproxy.Route{
		{
			Upstream:   "https://npm.example.com",
			Prefix:     "npm-example-com",
			Ecosystems: ecosystem.CargoRouteBlock("crates-io-mirror"),
		},
		{
			Upstream:   "https://cargo.example.com",
			Prefix:     "cargo-example-com",
			Ecosystems: ecosystem.CargoRouteBlock("internal", "vendor"),
		},
	}

	got := registryManifestRoutes(routes)

	if len(got) != 2 {
		t.Fatalf("registryManifestRoutes returned %d routes, want 2", len(got))
	}
	if got[0].Prefix != "npm-example-com" {
		t.Errorf("got[0].Prefix = %q, want %q", got[0].Prefix, "npm-example-com")
	}
	if want := []string{"crates-io-mirror"}; !slices.Equal(ecosystem.CargoRouteRegistries(got[0].Ecosystems), want) {
		t.Errorf("got[0] cargo registries = %v, want %v", ecosystem.CargoRouteRegistries(got[0].Ecosystems), want)
	}
	if got[1].Prefix != "cargo-example-com" {
		t.Errorf("got[1].Prefix = %q, want %q", got[1].Prefix, "cargo-example-com")
	}
	if want := []string{"internal", "vendor"}; !slices.Equal(ecosystem.CargoRouteRegistries(got[1].Ecosystems), want) {
		t.Errorf("got[1] cargo registries = %v, want %v", ecosystem.CargoRouteRegistries(got[1].Ecosystems), want)
	}
}

// TestRegistryManifestRoutes_ProjectsEnforcedSubtreesAsEnforcedPaths verifies
// that registryManifestRoutes converts registryproxy.Route's EnforcedSubtrees
// one-to-one into registrymanifest.Route's tagged EnforcedPaths (issue
// #3259), distinct from the untagged EnforcedPaths registryproxy.Route also
// carries for the Forwarder's admission check and never copies here.
func TestRegistryManifestRoutes_ProjectsEnforcedSubtreesAsEnforcedPaths(t *testing.T) {
	routes := []registryproxy.Route{
		{
			Upstream: "https://host.example.com",
			Prefix:   "host-example-com",
			EnforcedSubtrees: []registryvocab.Subtree{
				{Ecosystem: "npm", Path: "/npm"},
				{Ecosystem: "yarn", Path: "/yarn"},
			},
		},
	}

	got := registryManifestRoutes(routes)

	if len(got) != 1 {
		t.Fatalf("registryManifestRoutes returned %d routes, want 1", len(got))
	}
	want := []registryvocab.Subtree{
		{Ecosystem: "npm", Path: "/npm"},
		{Ecosystem: "yarn", Path: "/yarn"},
	}
	if !reflect.DeepEqual(got[0].EnforcedPaths, want) {
		t.Errorf("got[0].EnforcedPaths = %+v, want %+v", got[0].EnforcedPaths, want)
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
	if sock := filepath.Join(dir, registryProxySocketFile); unixsocket.TooLong(sock) {
		t.Errorf("proxy.sock join %q (%d bytes) is too long for a unix socket", sock, len(sock))
	}
}

// TestRegistryProxySocketDir_LongTMPDIR_FallsBackToTmp verifies the reported
// bug (issue #3077): a $TMPDIR long enough that the os.TempDir() candidate
// would overflow AF_UNIX's sun_path limit once the proxy dir and proxy.sock
// are appended, the case nix develop's nix-shell.XXXXXX/ prefix under macOS's
// per-user $TMPDIR triggers, is rescued by falling back to /tmp.
func TestRegistryProxySocketDir_LongTMPDIR_FallsBackToTmp(t *testing.T) {
	setLongTMPDir(t)

	dir, err := registryProxySocketDir()
	if err != nil {
		t.Fatalf("registryProxySocketDir: %v", err)
	}
	defer os.RemoveAll(dir)

	if sock := filepath.Join(dir, registryProxySocketFile); unixsocket.TooLong(sock) {
		t.Errorf("registryProxySocketDir did not fall back: proxy.sock join %q (%d bytes) is still too long", sock, len(sock))
	}
	if wantPrefix := "/tmp" + string(filepath.Separator); !strings.HasPrefix(dir, wantPrefix) {
		t.Errorf("registryProxySocketDir did not fall back to /tmp: dir = %q, want prefix %q", dir, wantPrefix)
	}
}

// TestRegistryProxySocketDir_NonexistentTMPDIR_ReturnsError verifies that a
// $TMPDIR whose os.MkdirTemp fails for a reason other than the issue #3077
// length overflow reaches the caller instead of being rerouted to a /tmp
// fallback; only the length check falls back. A seam records every base
// mkProxyDir sees, so nothing has to inspect shared, world-writable /tmp.
func TestRegistryProxySocketDir_NonexistentTMPDIR_ReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv("TMPDIR", missing)

	var bases []string
	stubRegistryProxyMkdirTemp(t, func(base, pattern string) (string, error) {
		bases = append(bases, base)
		return os.MkdirTemp(base, pattern)
	})

	dir, err := registryProxySocketDir()
	if err == nil {
		os.RemoveAll(dir)
		t.Fatalf("registryProxySocketDir: want error for nonexistent TMPDIR, got dir %q", dir)
	}

	if want := []string{""}; !reflect.DeepEqual(bases, want) {
		t.Errorf("registryProxySocketDir swallowed the error into a /tmp fallback: mkProxyDir called with bases %v, want %v", bases, want)
	}
}

// TestRegistryProxySocketDir_RemoveOverlongDirFails_ReturnsError verifies
// that when the over-long primary dir's cleanup fails, registryProxySocketDir
// wraps and surfaces that removal error instead of falling through to the
// /tmp fallback (issue #3103). Only an EACCES/EROFS/EBUSY-class error fails
// os.RemoveAll on a fresh dir, so the branch needs an injected seam.
func TestRegistryProxySocketDir_RemoveOverlongDirFails_ReturnsError(t *testing.T) {
	setLongTMPDir(t)

	sentinel := errors.New("boom: remove failed")
	origRemoveAll := registryProxyRemoveAll
	t.Cleanup(func() { registryProxyRemoveAll = origRemoveAll })
	registryProxyRemoveAll = func(string) error { return sentinel }

	dir, err := registryProxySocketDir()
	if err == nil {
		os.RemoveAll(dir) //nolint:errcheck
		t.Fatalf("registryProxySocketDir: want error when removing the over-long dir fails, got dir %q", dir)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("registryProxySocketDir error = %v, want wrapped sentinel %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "remove over-long registry proxy dir") {
		t.Errorf("registryProxySocketDir error = %q, want it to name the over-long dir removal", err.Error())
	}
}

// TestRegistryProxySocketDir_TmpFallbackMkdirFails_ReturnsError verifies
// that the fallback mkProxyDir("/tmp") leg's own error reaches the caller
// (issue #3103). Go line coverage cannot tell the two mkProxyDir call sites
// apart, both on the same source line, so exercising only the primary leg
// let this fallback leg read as covered while never actually running.
func TestRegistryProxySocketDir_TmpFallbackMkdirFails_ReturnsError(t *testing.T) {
	setLongTMPDir(t)

	sentinel := errors.New("boom: tmp mkdir failed")
	stubRegistryProxyMkdirTemp(t, func(base, pattern string) (string, error) {
		if base == "/tmp" {
			return "", sentinel
		}
		return os.MkdirTemp(base, pattern)
	})

	dir, err := registryProxySocketDir()
	if err == nil {
		os.RemoveAll(dir) //nolint:errcheck
		t.Fatalf("registryProxySocketDir: want error when the /tmp fallback mkdir fails, got dir %q", dir)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("registryProxySocketDir error = %v, want wrapped sentinel %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), `"/tmp"`) {
		t.Errorf("registryProxySocketDir error = %q, want it to name the /tmp fallback base", err.Error())
	}
}

// TestRun_IssueTextForErrorFailsDispatch verifies that a Config.IssueTextFor
// error reaches Run's Result as a failure instead of being warned and dropped
// (issue #3445, blocking review finding on dispatch.go:168): every
// *-prompt.md tells the Box its body is in the injected ISSUE_TEXT section,
// so a Box launched without it would have no recourse.
func TestRun_IssueTextForErrorFailsDispatch(t *testing.T) {
	fr := runner.NewFake()
	sentinel := errors.New("boom: rate limited")

	cfg := retryConfig(0, 0, 0)
	cfg.IssueTextFor = func(string) (string, error) { return "", sentinel }

	d := newTestDispatch(t, cfg, fr, fakeDriver{}, RealClock())

	result := d.Run()

	if result.Success {
		t.Fatalf("Run: want Success=false when IssueTextFor errors, got %+v", result)
	}
	if !errors.Is(result.Err, sentinel) {
		t.Errorf("Run result.Err = %v, want it to wrap %v", result.Err, sentinel)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("Run: want the box never launched, got %d RunCalls", len(fr.RunCalls))
	}
}
