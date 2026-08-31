// This file is package waves_test: a harness that calls
// wavestest.RunQueueContract must import wavestest, which itself imports
// waves (see contract.go's own doc comment), so an internal (package waves)
// test file importing wavestest would import-cycle back through it.
package waves_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
	"spindrift.dev/launcher/internal/waves/wavestest"
)

// contractDispatchLabels mirrors the conventional lifecycle-label set this
// package's own internal tests hold as testhelpers_test.go's
// testDispatchLabels -- unexported, so unreachable from this external test
// package; inlined here instead of duplicated as an exported var nothing
// else needs.
var contractDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// headlessHarness is the wavestest.Harness for NewHeadlessQueue: Claim runs
// through a real LabelClaimer over fc, so Pending/Claim exercise the same
// Dispatchable->InProgress label transition the headless RunContinuous path
// uses in production, not a canned test double.
type headlessHarness struct {
	fc    *forge.Fake
	label string
	// discoverCalls is a pointer so it survives struct copies -- headlessHarness
	// is a plain value struct passed by value into wavestest.RunQueueContract.
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

// ClaimTransitions and DiscoverCalls implement wavestest.SideEffectObserver:
// h.fc.TransitionStateCalls is forge.Fake's own call log (fake_tracker.go),
// so a leaked claim below Queue.Claim() -- e.g. headlessQueue.Pending
// reaching into q.claimer directly -- shows up here even though it never
// went through the Claimer this harness handed to NewHeadlessQueue.
func (h headlessHarness) ClaimTransitions() int { return len(h.fc.TransitionStateCalls) }
func (h headlessHarness) DiscoverCalls() int    { return *h.discoverCalls }

// SeedDispatchable seeds num directly onto fc carrying h.label, mirroring
// how this package's other tests (e.g. continuous_test.go) build a
// dispatchable issue with SetIssue rather than a live Untriaged->Dispatchable
// TransitionState call -- forge.Fake's TransitionState is best-effort and
// never errors on a from-state mismatch, so SetIssue is the only way to
// deterministically land num already carrying h.label.
func (h headlessHarness) SeedDispatchable(num string) {
	h.fc.SetIssue(forge.Issue{Number: num, Labels: []string{h.label}})
}

var _ wavestest.Harness = headlessHarness{}
var _ wavestest.SideEffectObserver = headlessHarness{}

// TestHeadlessQueue_QueueContract runs the shared Queue conformance suite
// (issue #2937) against NewHeadlessQueue's own LabelClaimer-backed adapter.
func TestHeadlessQueue_QueueContract(t *testing.T) {
	wavestest.RunQueueContract(t, newHeadlessHarness())
}

// fakeHarness is the wavestest.Harness for waves.Fake itself. Fake's Claim
// always returns f.ClaimErr (nil by default) regardless of prior claim
// state, so both calls in the contract's ClaimIdempotence check return nil,
// same as headlessHarness.
type fakeHarness struct{ f *waves.Fake }

func (h fakeHarness) Queue() waves.Queue { return h.f }

// SeedDispatchable is a no-op: Fake is a call-recorder with no
// dispatchable-state concept of its own for Claim/Pending to observe.
func (h fakeHarness) SeedDispatchable(num string) {}

// ClaimTransitions and DiscoverCalls implement wavestest.SideEffectObserver
// directly off Fake's own call-recorder fields (queue_fake.go): a leak
// inside Fake's own Pending -- e.g. reaching past Queue.Claim()/Discover()
// to append to f.ClaimCalls or bump f.DiscoverCalls directly -- shows up
// here the same as it would in the fields any other test in this package
// already asserts against.
func (h fakeHarness) ClaimTransitions() int { return len(h.f.ClaimCalls) }
func (h fakeHarness) DiscoverCalls() int    { return h.f.DiscoverCalls }

var _ wavestest.Harness = fakeHarness{}
var _ wavestest.SideEffectObserver = fakeHarness{}

// TestFake_QueueContract runs the shared Queue conformance suite (issue
// #2937) against waves.Fake.
func TestFake_QueueContract(t *testing.T) {
	wavestest.RunQueueContract(t, fakeHarness{f: waves.NewFake()})
}
