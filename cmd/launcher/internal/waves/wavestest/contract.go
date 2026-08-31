// Package wavestest is the executable contract for waves.Queue (issue
// #2937, spec #2919): one shared test suite every adapter -- the headless
// adapter (over the forge fake), the Console adapter, and waves.Fake itself
// -- runs against its own harness, so semantic drift between them fails CI
// instead of resting on doc-comment discipline (mirrors
// forge/forgetest/contract.go's shape for forge.IssueTracker).
//
// This is a sibling package to waves, not a waves_test file, because the
// Console adapter's own test (package console) already imports waves --
// putting the contract inside waves's own test files would make that
// import cycle back through wavestest.
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
	// backing store -- e.g. transitioning it to Dispatchable on a
	// forge.IssueTracker, or registering it on a session queue -- so
	// Claim/Pending have real state to observe rather than an arbitrary
	// issue number no backing store has ever heard of.
	SeedDispatchable(num string)
}

// SideEffectObserver is implemented by a Harness whose backing Queue can
// report how many real claim transitions and Discover calls have happened
// below the Queue interface itself. Optional, not part of Harness, because
// this class of leak -- an implementation reaching for its own internal
// collaborator fields directly, bypassing Queue.Claim()/Queue.Discover() --
// can never be observed by decorating the Queue interface from outside, so
// only a harness whose own Queue() construction wires in the counting can
// support it. Every harness in this package implements it: headlessHarness
// counts forge.Fake.TransitionStateCalls, the waves.Fake harness counts
// Fake's own ClaimCalls/DiscoverCalls fields directly, and consoleHarness
// counts its own discover closure's invocations -- ClaimTransitions is a
// constant 0 there since Console's Claim is a documented permanent no-op
// with no backing collaborator a leak could transition.
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

// testClaimIdempotence pins that claiming the same already-claimed issue a
// second time is always safe: no panic, no corrupt state, and -- uniformly
// across every adapter -- no error either. The headless adapter's Claim
// delegates to LabelClaimer, whose TransitionState is documented
// best-effort (fake_tracker.go, mirroring the real GitHub adapter's `gh
// issue edit --add-label/--remove-label`, #1985): it swaps labels
// unconditionally and never checks that num currently carries the "from"
// label, so a second claim re-applies the same swap and still returns nil,
// same as the Console adapter's permanent no-op and waves.Fake's
// unconditional call-recorder Claim. Claimer's own "stale listing racing a
// concurrent claimant" failure mode (queue.go) is a different, genuine
// backend error (e.g. a network failure), not a double-transition
// rejection -- no adapter here manufactures one from a bare repeat claim.
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

// testPendingQuietness pins Pending's documented contract (queue.go): "a
// quiet count: no claim, no discovery side effect, safe to call purely for
// a report." It runs two tiers of check. Unconditionally, on every harness:
// calling Pending repeatedly must return a stable count, and a subsequent
// genuine Claim call must still succeed exactly as it would have if Pending
// had never been called -- but on a harness whose Queue is a thin wrapper
// (e.g. a constant pending closure), these two checks alone can pass even
// when Pending secretly claims and discovers underneath, because nothing
// here observes the wrapped closure's own call count. So, gated on h
// implementing SideEffectObserver: the harness's own claim-transition and
// Discover call counts, sampled immediately before and after the two
// Pending() calls, must be unchanged -- this is the only tier that can catch
// a leak below the Queue interface itself (an implementation reaching into
// its own collaborator fields directly), since decorating Queue from outside
// can't see it.
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
