package waves

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

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
	}, edges, nil, OriginDiscovered); err != nil {
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
	}, map[string][]string{}, nil, OriginDiscovered); err != nil {
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
	}, edges, nil, OriginDiscovered); err != nil {
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
	}, map[string][]string{}, nil, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (maxJobs=1 must cap dispatch)", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_PrintsRemainingCountAfterCapNotFalselyBlocked verifies
// that when MAX_JOBS caps a wave short of the full ready set, the remaining
// count message does not claim the leftover issues are "blocked or
// deferred" — they are simply past the cap, ready for the next invocation.
func TestDrainMaxJobs_PrintsRemainingCountAfterCapNotFalselyBlocked(t *testing.T) {
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

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "first"},
			{Number: "2", Title: "second"},
			{Number: "3", Title: "third"},
		}, map[string][]string{}, nil, OriginDiscovered); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if !strings.Contains(out, "2 issue(s) remain") {
		t.Errorf("output must report how many issues remain; got:\n%s", out)
	}
	if strings.Contains(out, "blocked or deferred") {
		t.Errorf("issues held back only by the MAX_JOBS cap are not blocked or deferred; got:\n%s", out)
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
	}, map[string][]string{}, nil, OriginDiscovered); err != nil {
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

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "unblocked"},
			{Number: "2", Title: "dependent"},
		}, edges, nil, OriginDiscovered); err != nil {
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
	}, edges, nil, OriginDiscovered)

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Errorf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_Selective_PartialWave_PrintsRemainingAndRerunCommand is
// the regression test for #524's acceptance criterion: `dispatch 12 15`
// where #15 is blocked by in-list, unmerged #12 dispatches #12 only in one
// invocation; #15 is not claimed; the output names #15 and the exact re-run
// command so the operator can carry the remainder themselves (selective
// dispatch bypasses the label gate, so re-discovery can't).
func TestDrainMaxJobs_Selective_PartialWave_PrintsRemainingAndRerunCommand(t *testing.T) {
	c := baseConfig()
	c.Label = "ready-for-agent"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "12", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "15", Labels: []string{c.Label}})

	fr := runner.NewFake()

	edges := map[string][]string{"15": {"12"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "12", Title: "blocker"},
			{Number: "15", Title: "dependent"},
		}, edges, nil, OriginSelective); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "12" {
		t.Fatalf("RunCalls: got %v, want exactly issue 12", fr.RunCalls)
	}

	iss15, err := fc.Issue("15")
	if err != nil {
		t.Fatalf("Issue(15): %v", err)
	}
	if containsLabel(iss15.Labels, c.InProgressLabel) {
		t.Errorf("issue 15 must not be claimed while its blocker is unmet; labels=%v", iss15.Labels)
	}

	if !strings.Contains(out, "15") {
		t.Errorf("output must name the remaining issue #15; got:\n%s", out)
	}
	if !strings.Contains(out, "spindrift dispatch --yes 15") {
		t.Errorf("output must print the exact re-run command; got:\n%s", out)
	}
}

// TestDrainMaxJobs_Selective_ZeroSelected_ExitsWithRerunHint is the
// regression test for #524's acceptance criterion: zero selected with
// issues held (everything overlap-deferred) exits 3 (ErrOpenNoneDispatchable)
// and still prints the re-run hint, rather than waiting in-process.
func TestDrainMaxJobs_Selective_ZeroSelected_ExitsWithRerunHint(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.Label},
	})
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

	var runErr error
	out := testutil.CaptureStdout(t, func() {
		runErr = drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "10", Title: "candidate"},
		}, map[string][]string{}, nil, OriginSelective)
	})

	if !errors.Is(runErr, ErrOpenNoneDispatchable) {
		t.Fatalf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", runErr)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
	if !strings.Contains(out, "spindrift dispatch --yes 10") {
		t.Errorf("output must print the exact re-run command; got:\n%s", out)
	}
}

// TestDrainMaxJobs_BlockedLineNamesBlockers verifies that the blocked-skip
// line names the specific unready blocker issue number(s), comma-joined,
// rather than the generic "a blocker is not 'agent-complete'" message.
func TestDrainMaxJobs_BlockedLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by both #3 and #4 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "4", State: "OPEN"})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3", "4"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := testutil.CaptureStdout(t, func() {
		err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "blocked issue"},
		}, edges, nil, OriginDiscovered)
		if !errors.Is(err, ErrOpenNoneDispatchable) {
			t.Fatalf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", err)
		}
	})

	if !strings.Contains(out, "~~ #1 blocked by #3, #4; skipping") {
		t.Errorf("output must name the unready blockers; got:\n%s", out)
	}
}

// TestDrainMaxJobs_FailedBlockerLineNamesBlockers verifies that the
// failed-blocker skip line names the specific failed blocker issue
// number(s), comma-joined, rather than the generic "a dependency failed"
// message.
func TestDrainMaxJobs_FailedBlockerLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2

	fc := forge.NewFake(dispatchLabels(c))
	// Issue #1 is blocked by both #3 and #4, which have already failed.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3", "4"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "dependent"},
		}, edges, nil, OriginDiscovered); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if !strings.Contains(out, "!! #1  status=blocker-failed  note=#3, #4 failed; skipping") {
		t.Errorf("output must name the failed blockers; got:\n%s", out)
	}
}

// TestDrainMaxJobs_ClaimedIssue_MarkerAnnotatesSource verifies that the
// blocked-claim marker drainMaxJobs writes for the OriginClaimed path
// carries the same source annotation (native relationship vs body-text
// parsing) as preview and the blocked-skip notice, since the release
// workflow interpolates this file's contents verbatim into its comment.
func TestDrainMaxJobs_ClaimedIssue_MarkerAnnotatesSource(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is claimed (in-progress); its blocker #3 is open (unmet, native-sourced).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}
	sources := Sources{"1": {"3": forge.DepSourceNative}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, sources, OriginClaimed); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "logs", blockedMarker))
	if err != nil {
		t.Fatalf("reading blocked marker: %v", err)
	}
	if got := string(b); got != "#3 (native)" {
		t.Errorf("blocked marker = %q, want %q", got, "#3 (native)")
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
	}, edges, nil, OriginClaimed); err != nil {
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
