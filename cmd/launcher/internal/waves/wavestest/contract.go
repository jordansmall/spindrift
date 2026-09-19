// Package wavestest is the executable contract for waves.Queue (issue #2937,
// spec #2919): one shared suite every adapter runs against its own harness, so
// semantic drift between them fails CI. It is a sibling package to waves, not a
// waves_test file, because the Console adapter's own test imports waves, which
// would cycle that import back through wavestest.
package wavestest

import (
	"testing"

	"spindrift.dev/launcher/internal/waves"
)

// Harness lets RunQueueContract drive a waves.Queue implementation without
// knowing which adapter backs it.
type Harness interface {
	Queue() waves.Queue
	// SeedDispatchable makes num available to claim on the harness's own
	// backing store, so Claim and Pending observe real state instead of an
	// issue number no backing store has heard of.
	SeedDispatchable(num string)
}

// SideEffectObserver is an optional Harness extension counting the claim
// transitions and Discover calls that happen below the Queue interface. Only a
// harness that wires in the counting can catch an implementation bypassing
// Queue.Claim or Queue.Discover, so Harness itself cannot require it.
type SideEffectObserver interface {
	ClaimTransitions() int
	DiscoverCalls() int
}

// RunQueueContract runs the shared Queue conformance suite against h.
func RunQueueContract(t *testing.T, h Harness) {
	t.Run("ClaimIdempotence", func(t *testing.T) { testClaimIdempotence(t, h) })
	t.Run("PendingQuietness", func(t *testing.T) { testPendingQuietness(t, h) })
}

// testClaimIdempotence pins that a repeat claim of an already-claimed issue is
// safe and returns nil on every adapter: LabelClaimer's TransitionState is
// best-effort (#1985), swapping labels without checking that num carries the
// "from" label. Claimer's stale-listing race (queue.go) is a genuine backend
// error, not a double-transition rejection a bare repeat claim can produce.
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

// testPendingQuietness pins Pending's contract (queue.go): a quiet count, no
// claim, no discovery side effect. The unconditional checks (a stable count,
// a later Claim still succeeding) pass even when a thin wrapper Queue claims
// and discovers underneath, so the SideEffectObserver tier, sampling counts
// around the two Pending calls, is the only one that catches such a leak.
func testPendingQuietness(t *testing.T, h Harness) {
	q := h.Queue()
	h.SeedDispatchable("2")

	observer, observable := h.(SideEffectObserver)
	var claimsBefore, discoversBefore int
	if observable {
		claimsBefore = observer.ClaimTransitions()
		discoversBefore = observer.DiscoverCalls()
	}

	claimed := make(map[string]bool)
	n1, err := q.Pending(claimed)
	if err != nil {
		t.Fatalf("first Pending(): got %v, want nil", err)
	}
	n2, err := q.Pending(claimed)
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
