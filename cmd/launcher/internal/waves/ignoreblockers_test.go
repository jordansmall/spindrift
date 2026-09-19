package waves

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// ADR 0022: research lands no code, so an unmerged dependency never holds
// it. IgnoreBlockers dispatches an issue whose blocker is unmet.
func TestDrainMaxJobs_IgnoreBlockers_DispatchesDespiteUnmetBlocker(t *testing.T) {
	c := baseConfig()
	label := "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	// Issue #3 is open and carries no complete label, so an ordinary wave
	// would hold #1 for a later invocation.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
	}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (blocker edges must not gate research)", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "1" {
		t.Errorf("dispatched issue: got %q, want \"1\"", fr.RunCalls[0].Issue)
	}
}

// IgnoreBlockers also suppresses the cascade-fail path: a batch sibling
// reaching FailedLabel never fails a research dependent.
func TestDrainMaxJobs_IgnoreBlockers_FailedBlockerDoesNotCascade(t *testing.T) {
	c := baseConfig()
	label := "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "dependent issue"},
	}, edges, nil, nil, OriginDiscovered, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (a failed blocker must not cascade-fail research)", len(fr.RunCalls))
	}
}

// A research wave's rerun hint must name `spindrift research`, not a
// hardcoded `spindrift dispatch`: the operator needs the verb that actually
// carries the remainder into the next invocation (ADR 0022).
func TestDrainMaxJobs_Selective_RerunHint_UsesConfigVerb(t *testing.T) {
	c := baseConfig()
	label := "agent-research"
	c.MaxParallel = 1
	c.MaxJobs = 1
	c.Verb = "research"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "15", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "10", Title: "first"},
			{Number: "15", Title: "second"},
		}, nil, nil, nil, OriginSelective, claimer); err != nil {
			t.Fatalf("drainMaxJobs: %v", err)
		}
	})

	if !strings.Contains(out, "spindrift research --yes 15") {
		t.Errorf("output must print the research re-run command; got:\n%s", out)
	}
	if strings.Contains(out, "spindrift dispatch") {
		t.Errorf("output must not print the dispatch re-run command for a research wave; got:\n%s", out)
	}
}

// Under IgnoreBlockers the OriginClaimed single-issue path dispatches rather
// than holding, so it must write no blocked marker.
func TestDrainMaxJobs_IgnoreBlockers_ClaimedIssueWritesNoBlockedMarker(t *testing.T) {
	c := baseConfig()
	label := "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, nil, nil, OriginClaimed, claimer); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".spindrift", "logs", blockedMarker)); !os.IsNotExist(err) {
		t.Errorf("blocked marker must not be written when IgnoreBlockers is set; stat err = %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
}
