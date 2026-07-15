package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// newOrphanTestLauncher builds a Launcher wired to a runner.Fake reporting
// runningNames as still running — Console startup orphan detection (issue
// #651): a crash or dropped SSH leaves these running with no live goroutine
// in a fresh process to account for them.
func newOrphanTestLauncher(t *testing.T, cf forge.CodeForge, runningNames []string) *Launcher {
	t.Helper()
	fr := runner.NewFake()
	fr.RunningNames = runningNames
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
	return &Launcher{CodeForge: cf, Factory: factory, Settle: settle.NewFake(), Queue: NewQueue()}
}

// TestRun_OrphanDetected_ConfirmedYes_CallsRecoverFn verifies Console startup
// detects a container still running from a prior, crashed session and, on
// "y", adopts it through the existing recover path (issue #651, ADR 0023).
func TestRun_OrphanDetected_ConfirmedYes_CallsRecoverFn(t *testing.T) {
	f := forge.NewFake()
	launch := newOrphanTestLauncher(t, f, []string{"agent-issue-42"})

	var recovered []string
	launch.RecoverFn = func(num string) error {
		recovered = append(recovered, num)
		return nil
	}

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("y\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "#42") {
		t.Errorf("output = %q, want the orphaned issue named", out.String())
	}
	if len(recovered) != 1 || recovered[0] != "42" {
		t.Errorf("recovered = %v, want [42]", recovered)
	}
}

// TestRun_OrphanDetected_DeclinedNo_LeavesOrphanUntouched verifies declining
// the recovery offer takes no action — no RecoverFn call, no tracker write —
// leaving the orphan exactly as found.
func TestRun_OrphanDetected_DeclinedNo_LeavesOrphanUntouched(t *testing.T) {
	f := forge.NewFake()
	launch := newOrphanTestLauncher(t, f, []string{"agent-issue-42"})

	var recovered []string
	launch.RecoverFn = func(num string) error {
		recovered = append(recovered, num)
		return nil
	}

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("n\nq\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "#42") {
		t.Errorf("output = %q, want the orphaned issue named", out.String())
	}
	if len(recovered) != 0 {
		t.Errorf("recovered = %v, want none after declining", recovered)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none after declining", f.TransitionStateCalls)
	}
}

// TestRun_NoOrphans_NoRecoverPrompt verifies a clean start (nothing running)
// never blocks on a recovery prompt and consumes no extra input lines.
func TestRun_NoOrphans_NoRecoverPrompt(t *testing.T) {
	f := forge.NewFake()
	launch := newOrphanTestLauncher(t, f, nil)
	launch.RecoverFn = func(string) error {
		t.Fatal("RecoverFn: want no call, nothing orphaned")
		return nil
	}

	var out strings.Builder
	if err := Run(f, t.TempDir(), strings.NewReader("q\n"), &out, launch); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "orphan") {
		t.Errorf("output = %q, want no orphan prompt", out.String())
	}
}
