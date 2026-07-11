package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

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
		settle:       newSettle(c, fc, fc),
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
