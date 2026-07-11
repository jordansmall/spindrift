package waves

import (
	"errors"
	"os"
	"path/filepath"
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

// dispatchLabels builds the DispatchLabels mapping a fake forge.Client needs
// from a test Config.
func dispatchLabels(cfg Config) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: cfg.Label,
		InProgress:   cfg.InProgressLabel,
		Complete:     cfg.CompleteLabel,
		Failed:       cfg.FailedLabel,
	}
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
func newSettle(fc forge.Client) *settle.Settle {
	return settle.New(settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,
		MergePollTimeout:  100,
	}, fc)
}
