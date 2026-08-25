package waves

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// testDispatchLabels mirrors the conventional lifecycle-label set used
// across the launcher's tests (cmd/launcher/testhelpers_test.go).
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// baseConfig returns a Config suitable for wave/drain/touches tests.
func baseConfig() Config {
	return Config{
		InProgressLabel: "agent-in-progress",
		FailedLabel:     "agent-failed",
		CompleteLabel:   "agent-complete",
	}
}

// dispatchLabels builds the DispatchLabels mapping a fake forge adapter needs
// from a test Config.
func dispatchLabels(cfg Config) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: cfg.Label,
		InProgress:   cfg.InProgressLabel,
		Complete:     cfg.CompleteLabel,
		Failed:       cfg.FailedLabel,
	}
}

// tempLogDir creates a temp dir with a .spindrift/logs subdirectory.
func tempLogDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.MkdirAll(dispatch.HostLogDirFor(dir), 0o755); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// boxErr is a non-nil error that stands in for a non-zero box exit.
var boxErr = errors.New("exit 1")

// parseStaleDrainField extracts key's numeric value from a stale-drain.log
// STALE_DRAIN line (e.g. "durationSeconds=" or "freeSlotSeconds="), failing
// the test if key is absent or its value isn't a parseable float -- the
// shared extraction continuous_test.go's stale-drain tests otherwise
// copy-pasted per field.
func parseStaleDrainField(t *testing.T, log, key string) float64 {
	t.Helper()
	idx := strings.Index(log, key)
	if idx == -1 {
		t.Fatalf("stale-drain.log: got %q, want %s", log, key)
	}
	field := strings.Fields(log[idx:])[0]
	val, err := strconv.ParseFloat(strings.TrimPrefix(field, key), 64)
	if err != nil {
		t.Fatalf("%s: got %q, not parseable as float: %v", key, strings.TrimPrefix(field, key), err)
	}
	return val
}

// readSingleStaleDrainLog reads dir's stale-drain.log and fails the test
// unless it holds exactly one STALE_DRAIN line, returning its contents --
// the ReadFile-then-count preamble continuous_test.go's stale-drain tests
// otherwise copy-paste per call site.
func readSingleStaleDrainLog(t *testing.T, dir string) string {
	t.Helper()
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	if n := strings.Count(log, "STALE_DRAIN "); n != 1 {
		t.Fatalf("stale-drain.log: got %d STALE_DRAIN line(s), want exactly 1: %q", n, log)
	}
	return log
}

// testFactory builds a dispatch.Factory wired to dir and r, matching
// cmd/launcher's own test helper.
func testFactory(t *testing.T, dir string, r runner.Runner) *dispatch.Factory {
	t.Helper()
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := dispatch.NewFactory(dispatch.Config{
		TransientRetryMax:    3,
		TransientBackoffSecs: 0,
		HoldJitterSecs:       0,
	}, dir, r, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	return f
}

// newSettle builds a *settle.Settle with the immediate-merge, no-poll-delay
// settings the wave/drain tests exercise.
func newSettle(it forge.IssueTracker, cf forge.CodeForge) *settle.Settle {
	return settle.New(settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,
		MergePollTimeout:  100,
	}, it, cf)
}
