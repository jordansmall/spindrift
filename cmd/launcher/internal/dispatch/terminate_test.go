package dispatch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestFactory_Kill_UsesDeterministicBoxName verifies Kill reaches the
// runner using the exact box name runOnce derives ("agent-issue-" + number)
// -- Terminate (issue #649) has no live *Dispatch to ask, so it must compute
// the same name a running Dispatch would have launched under.
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

// TestFactory_Kill_PropagatesRunnerError verifies a runner Kill failure
// surfaces to the caller rather than being swallowed.
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

// TestFactory_OrphanedIssues verifies OrphanedIssues extracts issue numbers
// from the runner's currently-running sandbox names, parsed from the
// deterministic "agent-issue-" naming scheme — Console startup orphan
// detection (issue #651). Only an unsigned-integer suffix is a valid issue
// number; every other suffix shape (non-numeric, empty, signed) is silently
// skipped (issue #793, issue #1157) rather than fed to a caller like
// recoverByNumber.
func TestFactory_OrphanedIssues(t *testing.T) {
	tests := []struct {
		name         string
		runningNames []string
		want         []string
	}{
		{
			name:         "valid unsigned suffixes",
			runningNames: []string{"agent-issue-42", "agent-issue-101"},
			want:         []string{"42", "101"},
		},
		{
			name:         "no-prefix-match name is skipped",
			runningNames: []string{"agent-issue-42", "some-other-container"},
			want:         []string{"42"},
		},
		{
			name:         "non-numeric suffix is skipped",
			runningNames: []string{"agent-issue-42", "agent-issue-foo"},
			want:         []string{"42"},
		},
		{
			name:         "empty suffix is skipped",
			runningNames: []string{"agent-issue-42", "agent-issue-"},
			want:         []string{"42"},
		},
		{
			name:         "signed-positive suffix is skipped",
			runningNames: []string{"agent-issue-42", "agent-issue-+5"},
			want:         []string{"42"},
		},
		{
			name:         "signed-negative suffix is skipped",
			runningNames: []string{"agent-issue-42", "agent-issue--42"},
			want:         []string{"42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runner.NewFake()
			r.RunningNames = tt.runningNames
			f, err := NewFactory(Config{}, tempLogDir(t), r, fakeDriver{}, RealClock())
			if err != nil {
				t.Fatalf("NewFactory: %v", err)
			}

			got, err := f.OrphanedIssues()
			if err != nil {
				t.Fatalf("OrphanedIssues: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("OrphanedIssues = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFactory_AppendTerminalLine_AppendsToMostRecentPassLog verifies the
// note lands on the last pass LogPaths reports (a fix pass here), not the
// initial run's log -- the terminal line belongs on whichever log a live
// Box was actually writing when Terminate reaped it.
func TestFactory_AppendTerminalLine_AppendsToMostRecentPassLog(t *testing.T) {
	dir := tempLogDir(t)
	logsDir := filepath.Join(dir, "logs")
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

// TestFactory_AppendTerminalLine_NoPassesYetCreatesInitialLog verifies that
// when no Box ever ran (Terminate landed before claim finished dispatching),
// AppendTerminalLine still records the note by creating the initial log
// rather than silently doing nothing.
func TestFactory_AppendTerminalLine_NoPassesYetCreatesInitialLog(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	if err := f.AppendTerminalLine("2", "terminated by operator"); err != nil {
		t.Fatalf("AppendTerminalLine: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "logs", "issue-2.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "terminated by operator") {
		t.Errorf("initial log = %q, want it to contain the terminal line", got)
	}
}
