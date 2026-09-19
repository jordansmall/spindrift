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

func TestDrainMaxJobs_SkipsBlockedDispatchesNext(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
		{Number: "2", Title: "unblocked issue"},
	}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// Drain treats a ## Touches overlap with an InProgress issue the same way it
// treats an unmet declared blocker: skip without waiting, dispatch the next
// candidate.
func TestDrainMaxJobs_SkipsTouchOverlapDispatchesNext(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "overlapping issue"},
		{Number: "2", Title: "clean issue"},
	}, map[string][]string{}, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// agent-failed is recoverable (agent-recover retries it), so an issue whose
// in-batch blocker failed must wait across retries rather than be
// cascade-failed itself (#1984, incident #1972).
func TestDrainMaxJobs_HoldsDependentWhenBlockerFails(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "dependent"},
			{Number: "2", Title: "unblocked"},
		}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must not gain %q when blocker failed; labels=%v", c.FailedLabel, iss1.Labels)
	}
	if !containsLabel(iss1.Labels, label) {
		t.Errorf("issue 1 must keep %q while held; labels=%v", label, iss1.Labels)
	}
	if !strings.Contains(out, "~~ #1 blocked by #3; skipping") {
		t.Errorf("output must hold with the standard blocked-skip line; got:\n%s", out)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// NewPlan's forge.SortByPriority call can put a Critical dependent ahead of
// its Low blocker (#2281). The blocker gate is blind to list position, so the
// dependent is still skipped and the blocker dispatches on its own turn.
func TestDrainMaxJobs_PriorityOrderDoesNotBypassBlocker(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 0

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	edges := map[string][]string{"2": {"1"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	out := testutil.CaptureStdout(t, func() {
		// This order mirrors the priority sort: the Critical dependent (#2)
		// ahead of its Low blocker (#1).
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "2", Title: "dependent", Priority: forge.PriorityCritical},
			{Number: "1", Title: "blocker", Priority: forge.PriorityLow},
		}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if !strings.Contains(out, "~~ #2 blocked by #1; skipping") {
		t.Errorf("output must hold #2 with the standard blocked-skip line; got:\n%s", out)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "1" {
		t.Errorf("dispatched issue: got %q, want \"1\" (the ready blocker)", fr.RunCalls[0].Issue)
	}
}

// More unblocked issues follow the one that trips the cap, so this pins the
// labeled break exiting the for loop and not just the switch.
func TestDrainMaxJobs_MaxJobsCapHonored(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 3
	c.MaxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
		{Number: "3", Title: "third"},
	}, map[string][]string{}, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (maxJobs=1 must cap dispatch)", len(fr.RunCalls))
	}
}

// Issues left over when MAX_JOBS caps a wave are past the cap and ready for
// the next invocation, so the remaining-count message must not call them
// blocked or deferred.
func TestDrainMaxJobs_PrintsRemainingCountAfterCapNotFalselyBlocked(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 3
	c.MaxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "first"},
			{Number: "2", Title: "second"},
			{Number: "3", Title: "third"},
		}, map[string][]string{}, nil, nil, OriginDiscovered, claimer); err != nil {
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

// ADR 0019: MAX_JOBS=0 is an uncapped drain batch, not "dispatch nothing", so
// zero must drain every unblocked issue in one wave.
func TestDrainMaxJobs_ZeroMeansUncapped(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 3
	c.MaxJobs = 0

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
		{Number: "3", Title: "third"},
	}, map[string][]string{}, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3 (MaxJobs=0 must drain every unblocked issue)", len(fr.RunCalls))
	}
}

// Someone running a bare dispatch must never be left believing the queue
// drained, so a partial wave reports how many issues remain and names
// re-running dispatch as the way to continue (ADR 0019).
func TestDrainMaxJobs_PrintsRemainingCountAfterPartialWave(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 0

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	edges := map[string][]string{"2": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "unblocked"},
			{Number: "2", Title: "dependent"},
		}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
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

// Open dispatchable issues that are all blocked return
// ErrOpenNoneDispatchable so a driving loop stops instead of hot-looping.
func TestDrainMaxJobs_ReturnsErrOpenNoneDispatchable(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, not yet complete).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
	}, edges, nil, nil, OriginDiscovered, claimer)

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Errorf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
	}
}

// Regression test for #524: dispatch 12 15, where #15 is blocked by in-list,
// unmerged #12, dispatches only #12 and leaves #15 unclaimed. Selective
// dispatch bypasses the label gate, so re-discovery cannot pick up the
// remainder and the output must print the exact re-run command instead.
func TestDrainMaxJobs_Selective_PartialWave_PrintsRemainingAndRerunCommand(t *testing.T) {
	c := baseConfig()
	label := "ready-for-agent"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "12", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "15", Labels: []string{label}})

	fr := runner.NewFake()

	edges := map[string][]string{"15": {"12"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "12", Title: "blocker"},
			{Number: "15", Title: "dependent"},
		}, edges, nil, nil, OriginSelective, claimer); err != nil {
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
	if containsLabel(iss15.Labels, testInProgressLabel) {
		t.Errorf("issue 15 must not be claimed while its blocker is unmet; labels=%v", iss15.Labels)
	}

	if !strings.Contains(out, "15") {
		t.Errorf("output must name the remaining issue #15; got:\n%s", out)
	}
	if !strings.Contains(out, "spindrift dispatch --yes 15") {
		t.Errorf("output must print the exact re-run command; got:\n%s", out)
	}
}

// Regression test for #524: with everything overlap-deferred, selective
// dispatch exits 3 (ErrOpenNoneDispatchable) and still prints the re-run hint
// rather than waiting in-process.
func TestDrainMaxJobs_Selective_ZeroSelected_ExitsWithRerunHint(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	var runErr error
	out := testutil.CaptureStdout(t, func() {
		runErr = drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "10", Title: "candidate"},
		}, map[string][]string{}, nil, nil, OriginSelective, claimer)
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

// The blocked-skip line names the unready blockers, comma-joined, instead of
// the generic "a blocker is not 'agent-complete'" message.
func TestDrainMaxJobs_BlockedLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by both #3 and #4 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "4", State: "OPEN"})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3", "4"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "blocked issue"},
		}, edges, nil, nil, OriginDiscovered, claimer)
		if !errors.Is(err, ErrOpenNoneDispatchable) {
			t.Fatalf("drainMaxJobs: got %v, want ErrOpenNoneDispatchable", err)
		}
	})

	if !strings.Contains(out, "~~ #1 blocked by #3, #4; skipping") {
		t.Errorf("output must name the unready blockers; got:\n%s", out)
	}
}

// Reproduces the #1972 incident on the batch drain path: a blocker fails, is
// retried via agent-recover, fails again, then recovers. The dependent must
// stay held through every failed round and dispatch once the blocker reaches
// a satisfied state.
func TestDrainMaxJobs_Issue1972_HeldAcrossBlockerRetries(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	// Round 2 stands for agent-recover retrying #3 and failing again.
	// ErrOpenNoneDispatchable here means the whole batch is held, not a
	// failure.
	for round := 1; round <= 2; round++ {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "dependent"},
		}, edges, nil, nil, OriginDiscovered, claimer); err != ErrOpenNoneDispatchable {
			t.Fatalf("round %d: drainMaxJobs: got %v, want ErrOpenNoneDispatchable", round, err)
		}
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must never gain %q across retries; labels=%v", c.FailedLabel, iss1.Labels)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 before the blocker recovers", len(fr.RunCalls))
	}

	// Round 3: #3 recovers, so closing it satisfies the blocker.
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}, State: "CLOSED"})
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "dependent"},
	}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("round 3: drainMaxJobs: %v", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("round 3: RunCalls: got %v, want a single dispatch of #1", fr.RunCalls)
	}
}

// The release workflow interpolates the blocked-claim marker verbatim into
// its comment, so the OriginClaimed path must annotate the blocker source
// (native relationship vs body-text parsing) like preview and the
// blocked-skip notice do.
func TestDrainMaxJobs_ClaimedIssue_MarkerAnnotatesSource(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.MaxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is claimed; its blocker #3 is open, so unmet.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}
	sources := Sources{"1": {"3": forge.DepSourceNative}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, sources, nil, OriginClaimed, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".spindrift", "logs", blockedMarker))
	if err != nil {
		t.Fatalf("reading blocked marker: %v", err)
	}
	if got := string(b); got != "#3 (native)" {
		t.Errorf("blocked marker = %q, want %q", got, "#3 (native)")
	}
}

// On the OriginClaimed single-issue path, a failed in-batch blocker must not
// cascade-fail the claimed issue: it already carries in-progress, so
// cascading would leave it double-labeled.
func TestDrainMaxJobs_ClaimedIssue_FailedBlockerDoesNotCascade(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.MaxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	// The claimed path writes the blocked marker and returns nil, not
	// ErrOpenNoneDispatchable and not a cascade-fail.
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, nil, nil, OriginClaimed, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

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

// An issue in NewReadiness's failed set had its own DepsOf call error, which
// in edges alone looks identical to confirmed zero blockers, so drain holds it
// for a later invocation instead of dispatching or cascade-failing it (#1103,
// mirroring the console's Queue.Discover hold in #752).
func TestDrainMaxJobs_HoldsDepsOfCheckFailedIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.MaxJobs = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()

	failed := map[string]bool{"1": true}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "1", Title: "deps-of-failed issue"},
			{Number: "2", Title: "clean issue"},
		}, map[string][]string{}, nil, failed, OriginDiscovered, claimer); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}

	if !strings.Contains(out, "#1") || !strings.Contains(out, "retry") {
		t.Errorf("output must name #1 and explain it will retry; got:\n%s", out)
	}
}

// When the OriginClaimed path's own DepsOf call failed (#1103), an empty
// edges[num] must never read as confirmed zero blockers, so drain writes
// .spindrift/logs/blocked.txt and the release workflow reverts the claim and
// retries later.
func TestDrainMaxJobs_ClaimedIssue_DepsOfFailedWritesRetryMarker(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.MaxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})

	fr := runner.NewFake()
	failed := map[string]bool{"1": true}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, map[string][]string{}, nil, failed, OriginClaimed, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".spindrift", "logs", blockedMarker))
	if err != nil {
		t.Fatalf("reading blocked marker: %v", err)
	}
	if !strings.Contains(string(b), "retry") {
		t.Errorf("blocked marker = %q, want it to explain the claim will retry", string(b))
	}

	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (must not dispatch on a DepsOf check failure)", len(fr.RunCalls))
	}
}
