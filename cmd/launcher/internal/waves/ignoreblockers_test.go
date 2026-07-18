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

// TestDrainMaxJobs_IgnoreBlockers_DispatchesDespiteUnmetBlocker verifies that
// Config.IgnoreBlockers (the research dispatch kind, ADR 0022: research
// lands no code, so it is never held on an unmerged dependency) dispatches
// an issue even though its declared blocker is unmet.
func TestDrainMaxJobs_IgnoreBlockers_DispatchesDespiteUnmetBlocker(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, no complete label) — would normally
	// hold #1 for a later invocation.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "blocked issue"},
	}, edges, nil, nil, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (blocker edges must not gate research)", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "1" {
		t.Errorf("dispatched issue: got %q, want \"1\"", fr.RunCalls[0].Issue)
	}
}

// TestDrainMaxJobs_IgnoreBlockers_FailedBlockerDoesNotCascade verifies that
// Config.IgnoreBlockers also suppresses the cascade-fail path: a batch
// sibling reaching FailedLabel never fails a research dependent.
func TestDrainMaxJobs_IgnoreBlockers_FailedBlockerDoesNotCascade(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "dependent issue"},
	}, edges, nil, nil, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (a failed blocker must not cascade-fail research)", len(fr.RunCalls))
	}
}

// TestDrainMaxJobs_Selective_RerunHint_UsesConfigVerb verifies that a
// research selective wave's rerun hint names `spindrift research`, not a
// hardcoded `spindrift dispatch` — the operator must be told the verb that
// actually carries the remainder into the next invocation (ADR 0022).
func TestDrainMaxJobs_Selective_RerunHint_UsesConfigVerb(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-research"
	c.MaxParallel = 1
	c.MaxJobs = 1
	c.Verb = "research"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "15", Labels: []string{c.Label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := testutil.CaptureStdout(t, func() {
		if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
			{Number: "10", Title: "first"},
			{Number: "15", Title: "second"},
		}, nil, nil, nil, OriginSelective); err != nil {
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

// TestDrainMaxJobs_IgnoreBlockers_ClaimedIssueWritesNoBlockedMarker verifies
// that the OriginClaimed single-issue path never writes logs/blocked.txt
// when IgnoreBlockers is set — research is dispatched instead of held.
func TestDrainMaxJobs_IgnoreBlockers_ClaimedIssueWritesNoBlockedMarker(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-research"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "claimed issue"},
	}, edges, nil, nil, OriginClaimed); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "logs", blockedMarker)); !os.IsNotExist(err) {
		t.Errorf("blocked marker must not be written when IgnoreBlockers is set; stat err = %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
}
