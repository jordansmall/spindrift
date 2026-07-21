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
		box.Output.Write([]byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")) //nolint:errcheck
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
	if !result.OutcomeFound || result.Outcome.Status != "ready" {
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
	fr.WriteToOutput = []byte("SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok\n")

	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, fakeDriver{}, RealClock())

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
