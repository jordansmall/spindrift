// This file holds the minimal, target-shape demonstrations of driving
// RunContinuous through the FakeQueue (issue #2937): one dispatching case,
// one all-blocked case needing neither a *dispatch.Factory nor a
// settle.Settler. continuous_test.go's own scenario suite (slot-refill
// timing, stale-drain, rate-limit retry, and the rest) now drives through
// the same FakeQueue too, but belongs there, not here -- this file is for a
// new case that pins something about the Queue seam itself (a new adapter
// behavior, a new Batch field reaching RunContinuous), not a new
// RunContinuous scenario that happens to use the FakeQueue as its Queue.
package waves

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRunContinuous_ThroughFakeQueue_DispatchesDiscoveredIssue drives
// RunContinuous through the FakeQueue (issue #2937) with a genuinely
// dispatchable one-issue Batch, proving content that comes entirely through
// the FakeQueue's DiscoverReturn flows into a real claim (forge.Fake) and a
// real launch (runner.Fake via dispatch.Factory) -- not just the
// empty-batch short-circuit ErrOpenNoneDispatchable already covered by
// TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable
// (continuous_test.go).
func TestRunContinuous_ThroughFakeQueue_DispatchesDiscoveredIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1", Title: "one"}}}

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	fresh := func() (bool, bool, string) { return false, true, "" }

	err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh)

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
	// The claim itself now flows through the FakeQueue's own Claim, not
	// fc.TransitionState directly (issue #2938) -- refill calls
	// queue.Claim(iss.Number) before dispatch.
	if len(fake.ClaimCalls) != 1 || fake.ClaimCalls[0] != "1" {
		t.Fatalf("ClaimCalls: got %v, want [\"1\"] (queue claimed the issue before dispatch)", fake.ClaimCalls)
	}
	// fc.TransitionStateCalls carries only the post-run entry from settle
	// (the fake runner writes no real outcome file, so settle marks the
	// issue Failed via fc directly) -- the claim itself never touches fc
	// when claiming happens through the FakeQueue.
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("TransitionStateCalls: got %d, want 1 (the post-run Failed transition)", len(fc.TransitionStateCalls))
	}
	want := forge.TransitionStateCall{Num: "1", From: forge.InProgress, To: forge.Failed}
	if fc.TransitionStateCalls[0] != want {
		t.Fatalf("TransitionStateCalls[0]: got %+v, want %+v (post-run Failed transition for issue 1)", fc.TransitionStateCalls[0], want)
	}
}

// TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory drives
// RunContinuous through the FakeQueue (issue #2937) with a Batch that
// carries one issue but never becomes dispatchable (an unresolved blocker
// edge, mirroring TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable
// in continuous_test.go). It passes a literal nil for both the
// *dispatch.Factory and settle.Settler parameters, proving the seam needs
// neither when the FakeQueue's batch never produces anything dispatchable --
// satisfying the issue's AC4 ("no dispatch Factory, no settle") with a
// genuine, non-empty-batch scenario. This complements (does not replace)
// TestRunContinuous_ThroughFakeQueue_DispatchesDiscoveredIssue above, which
// constructs both because its issue actually launches.
func TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1", Title: "one"}},
		Edges:  map[string][]string{"1": {"2"}},
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if fake.DiscoverCalls == 0 {
		t.Fatalf("Discover: got 0 calls, want at least 1")
	}
}
