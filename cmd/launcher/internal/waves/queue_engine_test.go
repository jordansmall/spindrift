package waves

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRunContinuous_ThroughQueueFake_DispatchesDiscoveredIssue drives
// RunContinuous through the Fake Queue (issue #2937) with a genuinely
// dispatchable one-issue Batch, proving content that comes entirely through
// the Fake's DiscoverReturn flows into a real claim (forge.Fake) and a real
// launch (runner.Fake via dispatch.Factory) -- not just the empty-batch
// short-circuit ErrOpenNoneDispatchable already covered by
// TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable
// (continuous_test.go).
func TestRunContinuous_ThroughQueueFake_DispatchesDiscoveredIssue(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1", Title: "one"}}}

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	fresh := func() (bool, bool, string) { return false, true, "" }

	err := RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)

	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if fake.DiscoverCalls == 0 {
		t.Fatalf("Discover: got 0 calls, want at least 1")
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls[0].Issue: got %q, want %q", fr.RunCalls[0].Issue, "1")
	}
	// fc.TransitionStateCalls also carries a second, post-run entry from
	// settle (the fake runner writes no real outcome file, so settle marks
	// the issue Failed) -- that's settle's business, not the claim's. The
	// claim this test cares about is specifically the first call.
	if len(fc.TransitionStateCalls) < 1 {
		t.Fatalf("TransitionStateCalls: got %d, want at least 1 (the claim label transition)", len(fc.TransitionStateCalls))
	}
	want := forge.TransitionStateCall{Num: "1", From: forge.Dispatchable, To: forge.InProgress}
	if fc.TransitionStateCalls[0] != want {
		t.Fatalf("TransitionStateCalls[0]: got %+v, want %+v (claim transition for issue 1)", fc.TransitionStateCalls[0], want)
	}
}

// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory drives
// RunContinuous through the Fake Queue (issue #2937) with a Batch that
// carries one issue but never becomes dispatchable (an unresolved blocker
// edge, mirroring TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable
// in continuous_test.go). It passes a literal nil for both the
// *dispatch.Factory and settle.Settler parameters, proving the seam needs
// neither when the Fake's batch never produces anything dispatchable --
// satisfying the issue's AC4 ("no dispatch Factory, no settle") with a
// genuine, non-empty-batch scenario. This complements (does not replace)
// TestRunContinuous_ThroughQueueFake_DispatchesDiscoveredIssue above, which
// constructs both because its issue actually launches.
func TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1", Title: "one"}},
		Edges:  map[string][]string{"1": {"2"}},
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	err := RunContinuous(c, nil, fc, fc, tempLogDir(t), nil, nil, fake, fresh)

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if fake.DiscoverCalls == 0 {
		t.Fatalf("Discover: got 0 calls, want at least 1")
	}
}
