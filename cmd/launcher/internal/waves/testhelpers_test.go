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

// testDispatchLabels mirrors the lifecycle-label set the launcher's other
// tests use (cmd/launcher/testhelpers_test.go).
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// testInProgressLabel replaces the Config.InProgressLabel field that moved
// onto LabelClaimer's constructor (issue #2938). One constant covers every
// waves test, since none varied it from baseConfig()'s default.
const testInProgressLabel = "agent-in-progress"

// capsFor resolves capabilities the same way production does (issue #2946),
// so a blocker or readiness test does not have to hand-list which optional
// interfaces its particular *forge.Fake shape implements.
func capsFor(it forge.IssueTracker, cf forge.CodeForge) forge.Capabilities {
	return forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
}

func baseConfig() Config {
	return Config{
		FailedLabel:   "agent-failed",
		CompleteLabel: "agent-complete",
	}
}

// dispatchLabels builds the DispatchLabels a fake forge adapter needs. The
// dispatchable label comes from the caller as a local, no longer from
// Config.Label (issue #2938).
func dispatchLabels(cfg Config, label string) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: label,
		InProgress:   testInProgressLabel,
		Complete:     cfg.CompleteLabel,
		Failed:       cfg.FailedLabel,
	}
}

func tempLogDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.MkdirAll(dispatch.HostLogDirFor(dir), 0o755); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// boxErr is the error a fake runner returns for a non-zero box exit.
var boxErr = errors.New("exit 1")

// fakePending replaces the Queue.Pending closure continuous_test.go's
// stale-drain tests copy-pasted per call site (review finding on #2939). It
// mirrors main.go's production closure: re-list, then filter through
// CountReady, never a raw len(issues) (issue #2777). edges and failed may be
// nil, and claimed passes through because fc.ListIssues filters by label.
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

// testFactory wires a dispatch.Factory to dir and r. RunContinuous takes a
// concrete *dispatch.Factory, not an interface, so no fake can stand in: any
// test that needs a Box to actually launch must call this. AC1's "no real
// dispatch Factory" (issue #2940) covers only the tests where nothing ever
// dispatches, and those pass nil for f and s instead.
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

func newSettle(it forge.IssueTracker, cf forge.CodeForge) *settle.Settle {
	return settle.New(settle.Config{
		MergeMode:         "immediate",
		CompleteLabel:     "agent-complete",
		MergePollInterval: 0,
		MergePollTimeout:  100,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}, it, cf)
}
