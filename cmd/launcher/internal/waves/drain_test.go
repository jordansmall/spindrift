package waves

import (
	"errors"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = orig

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, rerr := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if rerr != nil {
			break
		}
	}
	return buf.String()
}

// TestDrainMaxJobs_SkipsBlockedDispatchesNext verifies that when MAX_JOBS=1
// the oldest blocked issue is skipped and the next unblocked issue is dispatched.
func TestDrainMaxJobs_SkipsBlockedDispatchesNext(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // blocker, not complete

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
		{Number: "2", Title: "unblocked issue"},
	}, edges, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	// Only the unblocked issue #2 must have been dispatched.
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// TestDrainMaxJobs_SkipsTouchOverlapDispatchesNext verifies that MAX_JOBS
// drain skips a Dispatchable issue whose declared ## Touches overlaps an
// InProgress issue's, without waiting, and dispatches the next candidate —
// matching how it already treats an unmet declared blocker.
func TestDrainMaxJobs_SkipsTouchOverlapDispatchesNext(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.Label},
	})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.InProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "overlapping issue"},
		{Number: "2", Title: "clean issue"},
	}, map[string][]string{}, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// TestDrainMaxJobs_FailsDependentWhenBlockerFails verifies that drain mode
// transitions an issue to failed when an in-batch blocker has already failed,
// matching the wave path's cascade semantics so the ready queue converges.
func TestDrainMaxJobs_FailsDependentWhenBlockerFails(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2

	fc := forge.NewFake(dispatchLabels(c))
	// Issue #1 is blocked by #3 which has already reached the failed label.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "dependent"},
		{Number: "2", Title: "unblocked"},
	}, edges, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	// Issue #1 must have been transitioned to failed.
	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if !containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must have %q when blocker failed; labels=%v", c.FailedLabel, iss1.Labels)
	}

	// Issue #2 (unblocked) must still be dispatched.
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// TestDrainMaxJobs_MaxJobsCapHonored verifies that the maxJobs cap is
// respected even when more unblocked issues follow the cap-trigger in the
// batch — i.e. the labeled-break exits the for loop, not just the switch.
func TestDrainMaxJobs_MaxJobsCapHonored(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 3
	c.MaxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
		{Number: "3", Title: "third"},
	}, map[string][]string{}, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (maxJobs=1 must cap dispatch)", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_ZeroMeansUncapped verifies that cfg.MaxJobs == 0 drains
// every unblocked issue in the batch in one wave — the cap does not apply at
// zero (ADR 0019: MAX_JOBS=0 is an uncapped drain batch, not "dispatch
// nothing").
func TestDrainMaxJobs_ZeroMeansUncapped(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 3
	c.MaxJobs = 0

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
		{Number: "3", Title: "third"},
	}, map[string][]string{}, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3 (MaxJobs=0 must drain every unblocked issue)", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_PrintsRemainingCountAfterPartialWave verifies that when a
// wave dispatches some issues but others stay blocked or deferred, the
// launcher says how many remain and names re-running dispatch as the way to
// continue — a bare `dispatch` caller must never be left believing the queue
// drained (ADR 0019).
func TestDrainMaxJobs_PrintsRemainingCountAfterPartialWave(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 0

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	edges := map[string][]string{"2": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := captureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "unblocked"},
			{Number: "2", Title: "dependent"},
		}, edges, OriginDiscovered); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if !strings.Contains(out, "1 issue(s) remain") {
		t.Errorf("output must report how many issues remain; got:\n%s", out)
	}
	if !strings.Contains(out, "dispatch") {
		t.Errorf("output must name re-running dispatch as the way to continue; got:\n%s", out)
	}
}

// TestDrainMaxJobs_ReturnsErrOpenNoneDispatchable verifies that drainMaxJobs
// returns ErrOpenNoneDispatchable when open dispatchable issues exist but none
// can be selected (all blocked), so a driving loop stops instead of hot-looping.
func TestDrainMaxJobs_ReturnsErrOpenNoneDispatchable(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, not yet complete).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // blocker

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
	}, edges, OriginDiscovered)

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Errorf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_ClaimedIssue_FailedBlockerDoesNotCascade verifies that
// when Origin is OriginClaimed (the single-issue path), an in-batch blocker
// reaching failed state does NOT cascade-fail the claimed issue. The issue is
// already on in-progress, so cascading would produce a double-labeled state.
func TestDrainMaxJobs_ClaimedIssue_FailedBlockerDoesNotCascade(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is on in-progress (claimed); its blocker #3 has failed.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	// The claimed path returns nil (writes blocked marker path internally),
	// not ErrOpenNoneDispatchable and not a cascade-fail.
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, OriginClaimed); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	// Issue #1 must NOT have been failed — it's on in-progress, not dispatchable.
	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("claimed issue 1 must NOT be cascade-failed; labels=%v", iss1.Labels)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
}
