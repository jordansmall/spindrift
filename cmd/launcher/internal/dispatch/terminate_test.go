package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// Terminate (issue #649) has no live *Dispatch to ask, so Kill must derive the
// same box name ("agent-issue-" + number) that a running Dispatch launched under.
func TestFactory_Kill_UsesDeterministicBoxName(t *testing.T) {
	r := runner.NewFake()
	f, err := NewFactory(Config{}, tempLogDir(t), r, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	if err := f.Kill("42"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if len(r.KillCalls) != 1 || r.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls: want [agent-issue-42], got %v", r.KillCalls)
	}
}

func TestFactory_Kill_PropagatesRunnerError(t *testing.T) {
	r := runner.NewFake()
	r.KillErr = boxErr
	f, err := NewFactory(Config{}, tempLogDir(t), r, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	if err := f.Kill("42"); err != boxErr {
		t.Errorf("Kill err = %v, want %v", err, boxErr)
	}
}

// The terminal line belongs on whichever log a live Box was writing when
// Terminate reaped it, so it lands on the last pass LogPaths reports (a fix
// pass here), not on the initial run's log.
func TestFactory_AppendTerminalLine_AppendsToMostRecentPassLog(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := HostLogDirFor(dir)
	initial := filepath.Join(logsDir, "issue-1.log")
	fix1 := filepath.Join(logsDir, "issue-1-fix-1.log")
	for _, p := range []string{initial, fix1} {
		if err := os.WriteFile(p, []byte("existing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	if err := f.AppendTerminalLine("1", "terminated by operator"); err != nil {
		t.Fatalf("AppendTerminalLine: %v", err)
	}

	initialContent, err := os.ReadFile(initial)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(initialContent), "terminated by operator") {
		t.Error("terminal line landed on the initial log, want the most recent pass (fix-1)")
	}
	fix1Content, err := os.ReadFile(fix1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fix1Content), "terminated by operator") {
		t.Errorf("fix-1 log = %q, want it to contain the terminal line", fix1Content)
	}
}

// Terminate can land before claim finished dispatching, so when no Box ever ran
// AppendTerminalLine still records the note by creating the initial log.
func TestFactory_AppendTerminalLine_NoPassesYetCreatesInitialLog(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	if err := f.AppendTerminalLine("2", "terminated by operator"); err != nil {
		t.Fatalf("AppendTerminalLine: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(HostLogDirFor(dir), "issue-2.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "terminated by operator") {
		t.Errorf("initial log = %q, want it to contain the terminal line", got)
	}
}

// AsReaper must return a nil interface for a nil *Factory, not a typed nil
// (#3521/#3522): a typed nil boxed into terminate.Reaper compares non-nil and
// defeats terminate.Reclaim's own nil guard.
func TestFactory_AsReaper_NilFactoryYieldsNilInterface(t *testing.T) {
	var f *Factory
	if reaper := f.AsReaper(); reaper != nil {
		t.Errorf("AsReaper() on nil *Factory = %v, want nil interface", reaper)
	}
}

func TestFactory_AsReaper_RealFactoryYieldsItself(t *testing.T) {
	f, err := NewFactory(Config{}, tempLogDir(t), runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	reaper := f.AsReaper()
	if reaper == nil {
		t.Fatal("AsReaper() on real *Factory = nil, want non-nil")
	}
	if got, ok := reaper.(*Factory); !ok || got != f {
		t.Errorf("AsReaper() = %v, want the same *Factory", reaper)
	}
}
