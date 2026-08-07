package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestWriteGithubOutput_AppendsKeyValueLine asserts writeGithubOutput appends
// a "key=value\n" line to the file named by GITHUB_OUTPUT.
func TestWriteGithubOutput_AppendsKeyValueLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	t.Setenv("GITHUB_OUTPUT", path)

	if err := writeGithubOutput("recover-reason", "issue 42: no open PR"); err != nil {
		t.Fatalf("writeGithubOutput() error = %v, want nil", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	want := "recover-reason=issue 42: no open PR\n"
	if string(got) != want {
		t.Errorf("file contents = %q, want %q", got, want)
	}
}

// TestWriteGithubOutput_SanitizesNewlines asserts writeGithubOutput replaces
// embedded newlines in value with spaces so a multi-line error text can't
// break the single-line key=value GITHUB_OUTPUT format.
func TestWriteGithubOutput_SanitizesNewlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	t.Setenv("GITHUB_OUTPUT", path)

	if err := writeGithubOutput("recover-reason", "issue 42: no open PR\nEXTRA=injected"); err != nil {
		t.Fatalf("writeGithubOutput() error = %v, want nil", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	want := "recover-reason=issue 42: no open PR EXTRA=injected\n"
	if string(got) != want {
		t.Errorf("file contents = %q, want %q", got, want)
	}
}

// TestWriteGithubOutput_NoopWhenUnset asserts writeGithubOutput is a no-op
// returning nil when GITHUB_OUTPUT is unset/empty.
func TestWriteGithubOutput_NoopWhenUnset(t *testing.T) {
	t.Setenv("GITHUB_OUTPUT", "")

	if err := writeGithubOutput("k", "v"); err != nil {
		t.Errorf("writeGithubOutput() error = %v, want nil", err)
	}
}

// TestCmdRecover_RunsCleanupOnEveryExit asserts cmdRecover runs the launch
// context's cleanup hook (driver-cache cleanup) even on the error exit path
// -- os.Exit no longer lives inside cmdRecover, so this now has to be an
// explicit call/defer rather than relying on process exit to skip it.
func TestCmdRecover_RunsCleanupOnEveryExit(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR registered for the branch -- recoverByNumber returns an error.
	dir := tempLogDir(t)
	called := false
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       newSettle(c, fc, testWired(fc), fc),
		cleanup:      func() { called = true },
	}

	got := cmdRecover(lc, "42")

	if got != 1 {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want 1 (no PR)", got)
	}
	if !called {
		t.Error("cmdRecover did not run lc.cleanup()")
	}
}

// TestCmdRecover_WritesReasonToGithubOutput asserts cmdRecover writes the
// recoverByNumber error text to the GITHUB_OUTPUT file under the
// "recover-reason" key on the no-open-PR error exit path.
func TestCmdRecover_WritesReasonToGithubOutput(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR registered for the branch -- recoverByNumber returns an error.
	dir := tempLogDir(t)
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       newSettle(c, fc, testWired(fc), fc),
		cleanup:      func() {},
	}

	got := cmdRecover(lc, "42")

	if got != 1 {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want 1 (no PR)", got)
	}

	out, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", outputPath, err)
	}
	want := "recover-reason=issue 42: no open PR"
	if !strings.Contains(string(out), want) {
		t.Errorf("GITHUB_OUTPUT contents = %q, want to contain %q", out, want)
	}
}

// TestCmdRecover_DraftPRAdoptedSucceeds asserts cmdRecover no longer treats
// a discovered draft PR as a rejection (issue #2408): recoverByNumber routes
// a draft PR through the same adopt-and-gate path as a non-draft one, so
// with green checks it adopts, gates, and merges the PR. cmdRecover must
// therefore return 0 and never write a "draft PR" rejection reason to
// GITHUB_OUTPUT.
func TestCmdRecover_DraftPRAdoptedSucceeds(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR, IsDraft: true})
	// A leading PENDING proves this run's own checks registered — issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       newSettle(c, fc, testWired(fc), fc),
		cleanup:      func() {},
	}

	got := cmdRecover(lc, "42")

	if got != 0 {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want 0 (draft PR adopted and merged)", got)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}

	if out, err := os.ReadFile(outputPath); err == nil && strings.Contains(string(out), "draft PR") {
		t.Errorf("GITHUB_OUTPUT must not contain a draft-PR rejection reason; got %q", out)
	}
}

// TestCmdDispatchSelective_RunsCleanupOnEveryExit asserts cmdDispatchSelective
// runs the launch context's cleanup hook on the error exit path (unknown
// issue number).
func TestCmdDispatchSelective_RunsCleanupOnEveryExit(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	dir := tempLogDir(t)
	called := false
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
		cleanup:      func() { called = true },
	}

	got := cmdDispatchSelective(lc, []string{"99"}, false)

	if got != 1 {
		t.Errorf("cmdDispatchSelective(lc, [99], false) = %d, want 1 (unknown issue)", got)
	}
	if !called {
		t.Error("cmdDispatchSelective did not run lc.cleanup()")
	}
}

// TestCmdDispatch_RunsCleanupOnEveryExit asserts cmdDispatch runs the launch
// context's cleanup hook on the errQueueEmpty exit path.
func TestCmdDispatch_RunsCleanupOnEveryExit(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	called := false
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
		cleanup:      func() { called = true },
	}

	got := cmdDispatch(lc)

	if got != 2 {
		t.Errorf("cmdDispatch(lc) = %d, want 2 (errQueueEmpty)", got)
	}
	if !called {
		t.Error("cmdDispatch did not run lc.cleanup()")
	}
}

// TestCmdConsole_RunsCleanupOnEveryExit asserts cmdConsole runs the launch
// context's cleanup hook, and actually reaches console.Run (not just
// bootstrap routing) -- a scripted "q" keypress on stdin quits the real
// Bubble Tea program immediately since the fake launchContext's Queue starts
// empty (tea.go's "q" case sends QuitMsg directly unless launch != nil and
// LiveIssues() is non-empty).
func TestCmdConsole_RunsCleanupOnEveryExit(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	dir := tempLogDir(t)
	called := false
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		// runner.NewFake(), not nil like the sibling tests: Init's
		// orphanDetectCmd calls Factory.OrphanedIssues -> f.runner.ListRunning
		// unconditionally on startup, which panics on a nil runner.
		factory: testFactory(t, dir, runner.NewFake()),
		settle:  settle.NewFake(),
		cleanup: func() { called = true },
	}

	stdin := strings.NewReader("q")
	var stdout bytes.Buffer

	got := cmdConsole(lc, stdin, &stdout)

	if got != 0 {
		t.Errorf("cmdConsole(lc, ...) = %d, want 0", got)
	}
	if !called {
		t.Error("cmdConsole did not run lc.cleanup()")
	}
}

// TestCmdConsole_SetsHeartbeatOutToDiscard verifies cmdConsole routes its
// factory's heartbeat sink to io.Discard before console.Run starts (issue
// #1583): Bubble Tea owns the terminal in alt-screen/raw mode, so a
// dispatch's heartbeat writer echoing to os.Stdout there would stairstep
// down the screen instead of returning to column 0.
func TestCmdConsole_SetsHeartbeatOutToDiscard(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake()
	dir := tempLogDir(t)
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, runner.NewFake()),
		settle:       settle.NewFake(),
		cleanup:      func() {},
	}

	stdin := strings.NewReader("q")
	var stdout bytes.Buffer
	cmdConsole(lc, stdin, &stdout)

	if got := lc.factory.HeartbeatOut(); got != io.Discard {
		t.Errorf("factory heartbeat sink = %v, want io.Discard", got)
	}
}
