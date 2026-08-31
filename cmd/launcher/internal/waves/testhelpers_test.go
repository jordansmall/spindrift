package waves

import (
	"errors"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
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

// testInProgressLabel is the in-progress label every waves test used to
// read off Config.InProgressLabel before that field moved onto
// LabelClaimer's constructor (issue #2938) — kept as one constant since no
// test ever varied it from baseConfig()'s "agent-in-progress" default.
const testInProgressLabel = "agent-in-progress"

// capsFor resolves it's/cf's forge.Capabilities the same way production
// (drainMaxJobs, issueReadiness, console/queue.go's Discover) does (issue
// #2946), so a blocker/readiness test doesn't have to hand-list which
// optional interfaces its particular *forge.Fake shape (bare, AsLocal,
// AsPushOnly, ...) implements.
func capsFor(it forge.IssueTracker, cf forge.CodeForge) forge.Capabilities {
	return forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
}

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

// fakePending builds a Queue.Pending closure over fc's Dispatchable-labeled
// issues, filtered through CountReady against edges/failed -- the pending
// closure continuous_test.go's stale-drain tests otherwise copy-paste per
// call site (a non-blocking review finding on #2939). edges and failed may
// be nil; CountReady treats a nil map the same as an empty one. This
// mirrors main.go's own production pending closure: re-list, then filter
// through CountReady, never a raw len(issues) (issue #2777). The returned
// closure passes NewHeadlessQueue's claimed set straight through to
// CountReady -- fc.ListIssues already filters by label synchronously, so no
// existing caller's fixture ever has a stale-claimed issue for it to drop.
func fakePending(fc *forge.Fake, c Config, edges map[string][]string, failed map[string]bool) func(map[string]bool) (int, error) {
	return func(claimed map[string]bool) (int, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return 0, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return CountReady(c, fc, fc, Batch{Issues: out, Edges: edges, Failed: failed}, claimed), nil
	}
}

// testFactory builds a dispatch.Factory wired to dir and r, matching
// cmd/launcher's own test helper. RunContinuous's f *dispatch.Factory
// parameter is a concrete struct, not an interface, so there is no fake
// substitute for it: any test that needs a Box to actually launch
// (assertions on fr.RunCalls, or on fc.TransitionStateCalls/
// CompleteVerdictCalls driven by a real dispatch) must call testFactory.
// AC1's "no real dispatch Factory" (issue #2940) only applies to, and is
// achieved by, tests where nothing ever dispatches — those pass a literal
// nil, nil for f and s instead (e.g. the StaleDrainHeldBack* exclusion
// tests in this file and queue_engine_test.go's
// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory).
func testFactory(t *testing.T, dir string, r runner.Runner) *dispatch.Factory {
	t.Helper()
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	f, err := dispatch.NewFactory(dispatch.Config{
		Policy: retry.Policy{Max: 3, Unit: 0, Jitter: 0},
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
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}, it, cf)
}
