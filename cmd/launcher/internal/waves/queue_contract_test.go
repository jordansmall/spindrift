// This file is package waves_test because wavestest imports waves, so an
// internal test file importing wavestest would create an import cycle.
package waves_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
	"spindrift.dev/launcher/internal/waves/wavestest"
)

// Mirrors testhelpers_test.go's testDispatchLabels, which is unexported and
// so unreachable from this external test package.
var contractDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// headlessHarness drives NewHeadlessQueue through a real LabelClaimer over
// fc, so the contract exercises the same Dispatchable to InProgress label
// transition production uses rather than a canned double.
type headlessHarness struct {
	fc    *forge.Fake
	label string
	// A pointer so the count survives the struct copy RunQueueContract makes
	// when it takes the harness by value.
	discoverCalls *int
}

func newHeadlessHarness() headlessHarness {
	return headlessHarness{
		fc:            forge.NewFake(contractDispatchLabels),
		label:         contractDispatchLabels.Dispatchable,
		discoverCalls: new(int),
	}
}

func (h headlessHarness) Queue() waves.Queue {
	discover := func() (waves.Batch, error) {
		*h.discoverCalls++
		return waves.Batch{}, nil
	}
	pending := func(map[string]bool) (int, error) { return 0, nil }
	claimer := waves.NewLabelClaimer(h.fc, h.label, contractDispatchLabels.InProgress)
	return waves.NewHeadlessQueue(discover, claimer, pending, "")
}

// These read forge.Fake's own call log, so a claim that bypasses Queue.Claim
// (Pending reaching into q.claimer directly) still shows up here.
func (h headlessHarness) ClaimTransitions() int { return len(h.fc.TransitionStateCalls) }
func (h headlessHarness) DiscoverCalls() int    { return *h.discoverCalls }

// SetIssue rather than TransitionState: forge.Fake's TransitionState is
// best-effort and never errors on a from-state mismatch, so only SetIssue
// deterministically lands num already carrying h.label.
func (h headlessHarness) SeedDispatchable(num string) {
	h.fc.SetIssue(forge.Issue{Number: num, Labels: []string{h.label}})
}

var _ wavestest.Harness = headlessHarness{}
var _ wavestest.SideEffectObserver = headlessHarness{}

// TestHeadlessQueue_QueueContract runs the shared Queue conformance suite
// (issue #2937) against NewHeadlessQueue's LabelClaimer-backed adapter.
func TestHeadlessQueue_QueueContract(t *testing.T) {
	wavestest.RunQueueContract(t, newHeadlessHarness())
}

// FakeQueue's Claim always returns f.ClaimErr regardless of prior claim
// state, so both calls in the contract's ClaimIdempotence check return nil,
// same as headlessHarness.
type fakeQueueHarness struct{ f *waves.FakeQueue }

func (h fakeQueueHarness) Queue() waves.Queue { return h.f }

// SeedDispatchable is a no-op: FakeQueue is a call-recorder with no
// dispatchable-state concept of its own for Claim/Pending to observe.
func (h fakeQueueHarness) SeedDispatchable(num string) {}

// These read FakeQueue's own call-recorder fields, so a Pending that appends
// to f.ClaimCalls or bumps f.DiscoverCalls without going through Claim or
// Discover shows up here.
func (h fakeQueueHarness) ClaimTransitions() int { return len(h.f.ClaimCalls) }
func (h fakeQueueHarness) DiscoverCalls() int    { return h.f.DiscoverCalls }

var _ wavestest.Harness = fakeQueueHarness{}
var _ wavestest.SideEffectObserver = fakeQueueHarness{}

// TestFakeQueue_QueueContract runs the shared Queue conformance suite (issue
// #2937) against waves.FakeQueue.
func TestFakeQueue_QueueContract(t *testing.T) {
	wavestest.RunQueueContract(t, fakeQueueHarness{f: waves.NewFakeQueue()})
}
