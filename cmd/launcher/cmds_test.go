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

// A newline in the value would break GITHUB_OUTPUT's single-line key=value
// format and let a multi-line error text inject a second key.
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

func TestWriteGithubOutput_NoopWhenUnset(t *testing.T) {
	t.Setenv("GITHUB_OUTPUT", "")

	if err := writeGithubOutput("k", "v"); err != nil {
		t.Errorf("writeGithubOutput() error = %v, want nil", err)
	}
}

// os.Exit no longer lives inside cmdRecover, so the driver-cache cleanup has
// to be an explicit call or defer rather than something process exit skips.
func TestCmdRecover_RunsCleanupOnEveryExit(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR is registered for the branch, so recoverByNumber returns an error.
	dir := tempLogDir(t)
	called := false
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       testNewSettle(c, fc, testWired(fc), fc),
		cleanup:      func() { called = true },
	}

	var stdout, stderr bytes.Buffer
	got := cmdRecover(lc, "42", &stdout, &stderr)

	if got != 1 {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want 1 (no PR)", got)
	}
	if !strings.Contains(stdout.String(), "status=skipped  note=no open PR") {
		t.Errorf("stdout = %q, want the skipped line", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no open PR") {
		t.Errorf("stderr = %q, want the failure line", stderr.String())
	}
	if !called {
		t.Error("cmdRecover did not run lc.cleanup()")
	}
}

func TestCmdRecover_WritesReasonToGithubOutput(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	// No PR is registered for the branch, so recoverByNumber returns an error.
	dir := tempLogDir(t)
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       testNewSettle(c, fc, testWired(fc), fc),
		cleanup:      func() {},
	}

	got := cmdRecover(lc, "42", io.Discard, io.Discard)

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

// Issue #2408: cmdRecover used to reject a discovered PR as a draft. Now
// recoverByNumber routes any stranded PR through the adopt-and-gate path, so
// with green checks it adopts, gates, and merges.
func TestCmdRecover_AdoptedPRSucceeds(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix

	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})
	// A leading PENDING proves this run's own checks registered. Issue #1652's
	// adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})

	dir := tempLogDir(t)
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       testNewSettle(c, fc, testWired(fc), fc),
		cleanup:      func() {},
	}

	got := cmdRecover(lc, "42", io.Discard, io.Discard)

	if got != 0 {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want 0 (PR adopted and merged)", got)
	}
	if fc.Merged != testReconcilePR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}

	if out, err := os.ReadFile(outputPath); err == nil && strings.Contains(string(out), "draft PR") {
		t.Errorf("GITHUB_OUTPUT must not contain a draft-PR rejection reason; got %q", out)
	}
}

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

	got := cmdDispatchSelective(lc, []string{"99"}, false, io.Discard, io.Discard)

	if got != 1 {
		t.Errorf("cmdDispatchSelective(lc, [99], false) = %d, want 1 (unknown issue)", got)
	}
	if !called {
		t.Error("cmdDispatchSelective did not run lc.cleanup()")
	}
}

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

	got := cmdDispatch(lc, io.Discard, io.Discard)

	if got != 2 {
		t.Errorf("cmdDispatch(lc) = %d, want 2 (errQueueEmpty)", got)
	}
	if !called {
		t.Error("cmdDispatch did not run lc.cleanup()")
	}
}

// The scripted "q" on stdin quits the real Bubble Tea program at once because
// the fake launchContext's queue starts empty, so this reaches console.Run
// rather than stopping at bootstrap routing. See tea.go's "q" case, which
// sends QuitMsg directly unless launch is non-nil and LiveIssues() is
// non-empty.
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
		// orphanDetectCmd calls Factory.OrphanedIssues, which calls
		// f.runner.ListRunning on startup and panics on a nil runner.
		factory: testFactory(t, dir, runner.NewFake()),
		settle:  settle.NewFake(),
		cleanup: func() { called = true },
	}

	stdin := strings.NewReader("q")
	var stdout bytes.Buffer

	got := cmdConsole(lc, stdin, &stdout, io.Discard)

	if got != 0 {
		t.Errorf("cmdConsole(lc, ...) = %d, want 0", got)
	}
	if !called {
		t.Error("cmdConsole did not run lc.cleanup()")
	}
}

// Issue #3651: Bubble Tea drops its own signal registration on any quit, so
// cmdConsole must hold a launcher-owned one across the whole body, including
// the deferred cleanups that run after Run returns.
func TestCmdConsole_HoldsStopSignalRegistrationThroughCleanup(t *testing.T) {
	orig := installStopSignal
	installs, releases := 0, 0
	heldAtCleanup := false
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		installs++
		return make(chan struct{}), make(chan struct{}), func() { releases++ }
	}
	t.Cleanup(func() { installStopSignal = orig })

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
		// lc.cleanup is the last teardown to run before stopCleanup, so a hold
		// seen here also covers launch.Wait and researchFactory.Cleanup.
		cleanup: func() { heldAtCleanup = installs == 1 && releases == 0 },
	}

	var stdout bytes.Buffer
	if got := cmdConsole(lc, strings.NewReader("q"), &stdout, io.Discard); got != 0 {
		t.Errorf("cmdConsole = %d, want 0", got)
	}
	if installs != 1 {
		t.Errorf("installStopSignal calls = %d, want 1", installs)
	}
	if !heldAtCleanup {
		t.Error("stop-signal registration was not held when lc.cleanup ran")
	}
	if releases != 1 {
		t.Errorf("stop-signal cleanup calls = %d, want 1", releases)
	}
}

// Issue #1583: Bubble Tea owns the terminal in alt-screen raw mode, so a
// dispatch's heartbeat writer echoing to os.Stdout stairsteps down the screen
// instead of returning to column 0.
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
	cmdConsole(lc, stdin, &stdout, io.Discard)

	if got := lc.factory.HeartbeatOut(); got != io.Discard {
		t.Errorf("factory heartbeat sink = %v, want io.Discard", got)
	}
}
