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
	"spindrift.dev/launcher/internal/seamtest"
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

// testInProgressLabel is the in-progress label LabelClaimer's constructor
// takes (issue #2938); no waves test varies it.
const testInProgressLabel = "agent-in-progress"

// capsFor resolves capabilities the same way production does (issue #2946),
// so a blocker or readiness test does not have to hand-list which optional
// interfaces its particular *forge.Fake shape implements.
func capsFor(it forge.IssueTracker, cf forge.CodeForge) forge.Capabilities {
	return forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
}

// testCompleteLabel is the complete label every waves test forge and settle
// is configured with.
const testCompleteLabel = "agent-complete"

func baseConfig() Config {
	cfg := Config{FailedLabel: "agent-failed"}
	cfg.SettledLabels = forge.DispatchLabels{
		Complete:  testCompleteLabel,
		Failed:    cfg.FailedLabel,
		Ambiguous: "agent-ambiguous-spec",
	}.SettledLabels()
	return cfg
}

// dispatchLabels builds the DispatchLabels a fake forge adapter needs; the
// dispatchable label comes from the caller (issue #2938).
func dispatchLabels(cfg Config, label string) forge.DispatchLabels {
	return forge.DispatchLabels{
		Dispatchable: label,
		InProgress:   testInProgressLabel,
		Complete:     testCompleteLabel,
		Failed:       cfg.FailedLabel,
	}
}

// forgeQueue is a headless Queue over fc's dispatchable issues, claimed
// through a real LabelClaimer.
func forgeQueue(fc *forge.Fake, c Config, label, dir string) Queue {
	discover := func() (Batch, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return Batch{}, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return Batch{Issues: out, Edges: map[string][]string{}}, nil
	}
	return NewHeadlessQueue(discover, NewLabelClaimer(fc, label, dispatchLabels(c, label).InProgress), noopPending, dir)
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

// fakePending is the stale-drain tests' Queue.Pending closure (#2939),
// mirroring main.go's: re-list, then filter through CountReady, never a raw
// len(issues) (issue #2777). edges and failed may be nil, and claimed passes
// through because fc.ListIssues filters by label.
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
// concrete *dispatch.Factory, so any test that launches a Box needs this;
// tests where nothing dispatches pass nil for f and s (issue #2940).
func testFactory(t *testing.T, dir string, r runner.Runner) *dispatch.Factory {
	t.Helper()
	seamtest.RegisterDriver(t)
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
	// The Box never opens its own PR, so every PR forge relays its bundle and
	// the host opens the draft PR.
	if g, ok := cf.(interface{ AsGithub() forge.CodeForge }); ok {
		cf = g.AsGithub()
	}
	return settle.New(settle.Config{
		MergeMode:         "immediate",
		BaseBranch:        "main",
		OutboxDir:         func(string) string { return "/outbox" },
		CompleteLabel:     testCompleteLabel,
		MergePollInterval: 0,
		MergePollTimeout:  100,
		Capabilities:      forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{}),
	}, it, cf)
}
