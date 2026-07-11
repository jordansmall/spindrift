package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// TestRun_EmptyQueue_ReturnsErrQueueEmpty asserts run's orchestration logic
// (as opposed to the bootstrap prologue) runs correctly against a
// fake-populated launchContext, with no ISSUE_NUMBER and no dispatchable
// issues in the fake forge.
func TestRun_EmptyQueue_ReturnsErrQueueEmpty(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	err := run(lc)

	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty", err)
	}
}

// TestRunExitCode_EmptyQueue_ReturnsExitCode2 asserts the run-to-exit-code
// translation (errQueueEmpty -> 2) that main previously did inline, against
// a fake-populated launchContext -- no bootstrap, no real config or runtime.
func TestRunExitCode_EmptyQueue_ReturnsExitCode2(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 2 {
		t.Errorf("runExitCode(lc) = %d, want 2 (errQueueEmpty)", got)
	}
}

// TestRunExitCode_QueueMaxJobsZero_NoneDispatchable_ReturnsExitCode3 is the
// end-to-end regression test for #522/#477: with MAX_JOBS unset (0, the
// uncapped drain default) the queue path no longer loops dispatchWaves
// waiting for a blocker — a batch with nothing currently dispatchable exits
// straight to code 3.
func TestRunExitCode_QueueMaxJobsZero_NoneDispatchable_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Blocked by\n- #2",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not yet complete
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 3 {
		t.Errorf("runExitCode(lc) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}
