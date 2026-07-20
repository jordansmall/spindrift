package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestDrainMaxJobs_PreResolved_DispatchesDespiteUnmetBlocker verifies that
// Config.PreResolved (#1547) — the caller vouches it already resolved this
// batch's blocker readiness through the Readiness query seam — disables the
// engine's own blocker gate, the explicit replacement for the pre-#1547
// empty-edges trick.
func TestDrainMaxJobs_PreResolved_DispatchesDespiteUnmetBlocker(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	c.PreResolved = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	// A real, unmet blocker edge — PreResolved must skip evaluating it
	// entirely, not merely tolerate an empty edges map.
	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := drainMaxJobs(c, fc, fc, dir, f, s, []Issue{
		{Number: "1", Title: "pre-resolved issue"},
	}, edges, nil, nil, OriginDiscovered); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (PreResolved must skip the blocker gate)", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "1" {
		t.Errorf("dispatched issue: got %q, want \"1\"", fr.RunCalls[0].Issue)
	}
}

// TestNextReady_PreResolved_ReturnsIssueDespiteUnmetBlocker verifies the
// same Config.PreResolved (#1547) contract on the continuous-refill
// selection path: Console's Queue.Discover already resolved this pick's own
// readiness, so nextReady must not re-evaluate edges at all.
func TestNextReady_PreResolved_ReturnsIssueDespiteUnmetBlocker(t *testing.T) {
	c := baseConfig()
	c.PreResolved = true

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	edges := map[string][]string{"1": {"3"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "pre-resolved issue"},
	}, edges, nil, nil, nil)

	if !ok {
		t.Fatalf("nextReady: got ok=false, want ok=true (PreResolved must skip the blocker gate)")
	}
	if iss.Number != "1" {
		t.Errorf("nextReady issue: got #%s, want #1", iss.Number)
	}
}
