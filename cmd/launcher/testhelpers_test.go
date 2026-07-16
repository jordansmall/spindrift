package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// testDispatchLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix and pinned against the agent workflows by
// nix/checks/dispatch-labels.nix (issue #460). forge.NewFake takes labels as
// an explicit constructor argument rather than baking in a copy, so tests in
// this package that exercise ListIssues(state) or TransitionState share this
// one value instead of each restating the four label strings.
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// baseConfig returns a config suitable for merge-gate-adjacent tests
// (preflight, wiring through settle).
func baseConfig() config {
	return config{
		inProgressLabel:   "agent-in-progress",
		failedLabel:       "agent-failed",
		completeLabel:     "agent-complete",
		mergePollInterval: 0,   // no sleep in tests
		mergePollTimeout:  100, // large enough for multi-poll tests
		mergeMode:         "immediate",
		codeForge:         "github",
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

// testFactory builds a dispatch.Factory wired to dir and r, using the real
// claude Driver (its ClassifyTransient degrades to Terminal/TaskFailed on a
// log with no transient markers, matching newDriver(c)'s production default)
// and the real clock. r may be nil for tests that never exercise a Fix or
// Run call.
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
