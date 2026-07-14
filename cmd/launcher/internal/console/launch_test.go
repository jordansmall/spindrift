package console

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// TestRunContinuous_DrainsScriptedQueue_LaunchesOneDispatchEndToEnd drives
// the continuous engine directly (no console Model, View, or Run loop) with
// a scripted operator queue: one queued pick, single slot (MaxParallel=1).
// It proves the whole "one Dispatch end to end" AC — Pick's queued row
// claims, runs a Box, and settles — through the existing Discoverer seam
// the engine already exposes, with the engine itself staying UI-blind
// (#646).
func TestRunContinuous_DrainsScriptedQueue_LaunchesOneDispatchEndToEnd(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})

	fr := runner.NewFake()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	factory, err := dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)

	inner := settle.NewFake()
	qs := queueSettler{Settler: inner, q: q}

	discover := func() ([]waves.Issue, map[string][]string, error) { return q.Discover(f) }
	fresh := func() (bool, bool, string) { return false, true, "" }

	err = waves.RunContinuous(waves.Config{MaxParallel: 1}, f, f, dir, factory, qs, discover, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "42" {
		t.Fatalf("RunCalls = %+v, want one Box run for #42", fr.RunCalls)
	}
	if len(inner.SettleCalls) != 1 || inner.SettleCalls[0].Num != "42" {
		t.Fatalf("SettleCalls = %+v, want one settle for #42", inner.SettleCalls)
	}
	if got := q.Snapshot()[0].State; got != PickSettled {
		t.Errorf("pick state = %v, want settled", got)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "agent-in-progress") {
		t.Errorf("issue #42 labels = %v, want agent-in-progress (claimed)", iss.Labels)
	}
}
