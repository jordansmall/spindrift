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
	"spindrift.dev/launcher/internal/testutil"
)

// testDispatchLabels mirrors the conventional lifecycle-label set used
// across the launcher's tests (cmd/launcher/testhelpers_test.go).
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// testInProgressLabel is the in-progress label every waves test used to
// read off Config.InProgressLabel before that field moved onto
// LabelClaimer's constructor (issue #2938) — kept as one constant since no
// test ever varied it from baseConfig()'s "agent-in-progress" default.
const testInProgressLabel = "agent-in-progress"

// baseConfig returns a Config suitable for wave/drain/touches tests.
func baseConfig() Config {
	return Config{
		FailedLabel:   "agent-failed",
		CompleteLabel: "agent-complete",
	}
}

// dispatchLabels builds the DispatchLabels mapping a fake forge adapter
// needs from a test Config plus the caller's own dispatchable label (a
// local, no longer Config.Label — issue #2938).
func dispatchLabels(cfg Config, label string) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: label,
		InProgress:   testInProgressLabel,
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

// runStaleDrain drives RunContinuous with discover against an
// always-immediately-stale freshness check, and returns the captured stdout
// alongside the appended stale-drain.log contents -- the boilerplate every
// heldBack stale-drain test in continuous_test.go otherwise repeats
// (#2778 review finding). Requires c.MaxParallel == 1: the sole slot is
// acquired then immediately released by the stale branch's defer, so
// outstanding never leaves zero and heldBack is computed from the full
// discovered batch. Asserts the two outcomes every caller shares --
// ErrImageStale and zero RunCalls -- so each caller only needs to assert
// its own heldBack count.
func runStaleDrain(t *testing.T, c Config, fc *forge.Fake, discover Discoverer) (stdout, log string) {
	t.Helper()
	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	var err error
	stdout = testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, QueueFromDiscoverer(discover), fresh)
	})
	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (stale fired before any launch)", fr.RunCalls)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	return stdout, string(logBytes)
}
