// Package wavestest is the executable contract for waves.Queue: one shared
// test suite every adapter -- headless (over the forge fake), Console, and
// waves.Fake itself -- runs against its own harness, so semantic drift between
// them fails CI instead of resting on doc-comment discipline.
//
// A sibling package to waves, not a waves_test file, because the Console
// adapter's own test (package console) already imports waves -- putting the
// contract inside waves's own test files would make that import cycle back
// through wavestest.
package wavestest

import (
	"testing"

	"spindrift.dev/launcher/internal/waves"
)

// Harness lets RunQueueContract drive a waves.Queue implementation without
// knowing which adapter backs it.
type Harness interface {
	// Queue returns the Queue under test.
	Queue() waves.Queue
	// SeedDispatchable makes num available to claim on the harness's own
	// backing store, so Claim/Pending have real state to observe rather than
	// an arbitrary issue number no backing store has heard of.
	SeedDispatchable(num string)
}

// SideEffectObserver is implemented by a Harness whose backing Queue can report
// how many real claim transitions and Discover calls have happened below the
// Queue interface itself. Optional, not part of Harness, because this class of
// leak -- an implementation reaching for its own collaborator fields directly,
// bypassing Queue.Claim()/Queue.Discover() -- can never be observed by
// decorating the Queue interface from outside, so only a harness whose own
// Queue() construction wires in the counting can support it. Every harness in
// this package implements it; consoleHarness reports a constant 0
// ClaimTransitions, since Console's Claim is a documented permanent no-op.
type SideEffectObserver interface {
	ClaimTransitions() int
	DiscoverCalls() int
}

// RunQueueContract runs the shared Queue conformance suite against h. Every
// adapter's own test file calls this, backed by its own Harness.
func RunQueueContract(t *testing.T, h Harness) {
	t.Run("ClaimIdempotence", func(t *testing.T) { testClaimIdempotence(t, h) })
	t.Run("PendingQuietness", func(t *testing.T) { testPendingQuietness(t, h) })
}

// testClaimIdempotence pins that claiming an already-claimed issue a second
// time is always safe: no panic, no corrupt state, and -- uniformly across
// every adapter -- no error either. The headless adapter's Claim delegates to
// LabelClaimer, whose TransitionState is documented best-effort (mirroring the
// real GitHub adapter's `gh issue edit --add-label/--remove-label`): it swaps
// labels unconditionally without checking the "from" label, so a repeat claim
// re-applies the same swap and still returns nil. Claimer's own "stale listing
// racing a concurrent claimant" failure is a genuine backend error, not a
// double-transition rejection, and no adapter manufactures one from a bare
// repeat claim.
func testClaimIdempotence(t *testing.T, h Harness) {
	q := h.Queue()
	h.SeedDispatchable("1")

	if err := q.Claim("1"); err != nil {
		t.Fatalf("first Claim(1): got %v, want nil (issue is freshly seeded as dispatchable)", err)
	}
	if err := q.Claim("1"); err != nil {
		t.Fatalf("second Claim(1) on already-claimed work: got %v, want nil (Claim is idempotent on every adapter)", err)
	}
}

// testPendingQuietness pins Pending's documented contract (queue.go): "a quiet
// count: no claim, no discovery side effect, safe to call purely for a
// report." Two tiers. Unconditionally: repeated Pending calls return a stable
// count, and a subsequent genuine Claim still succeeds. On a harness whose
// Queue is a thin wrapper those two can pass even when Pending secretly claims
// and discovers underneath, so gated on h implementing SideEffectObserver, the
// harness's own claim-transition and Discover counts must be unchanged across
// the two Pending() calls -- the only tier that can catch a leak below the
// Queue interface itself.
func testPendingQuietness(t *testing.T, h Harness) {
	q := h.Queue()
	h.SeedDispatchable("2")

	observer, observable := h.(SideEffectObserver)
	var claimsBefore, discoversBefore int
	if observable {
		claimsBefore = observer.ClaimTransitions()
		discoversBefore = observer.DiscoverCalls()
	}

	n1, err := q.Pending()
	if err != nil {
		t.Fatalf("first Pending(): got %v, want nil", err)
	}
	n2, err := q.Pending()
	if err != nil {
		t.Fatalf("second Pending(): got %v, want nil", err)
	}
	if n1 != n2 {
		t.Fatalf("Pending() = %d then %d, want a stable count across repeated calls with no claim/discovery in between", n1, n2)
	}

	if observable {
		if got := observer.ClaimTransitions(); got != claimsBefore {
			t.Fatalf("Pending() leaked a claim transition: ClaimTransitions() = %d, want unchanged %d", got, claimsBefore)
		}
		if got := observer.DiscoverCalls(); got != discoversBefore {
			t.Fatalf("Pending() leaked a Discover call: DiscoverCalls() = %d, want unchanged %d", got, discoversBefore)
		}
	}

	if err := q.Claim("2"); err != nil {
		t.Fatalf("Claim(2) after two Pending() calls: got %v, want nil (Pending must not have already claimed it)", err)
	}
}
