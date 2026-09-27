// Package ledgertest is the executable contract for ledger.Backend: every
// backend (the local forge's Local, and a future push-to-remote backend)
// runs this one suite against its own harness, so drift between backends
// fails CI.
package ledgertest

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/usage"
)

// Harness lets RunContract drive a ledger.Backend without knowing which
// storage adapter it is.
type Harness interface {
	// Backend returns the handle under test.
	Backend() ledger.Backend
	// Rival returns a second, independent handle on the same Ledger store
	// (for Local, another Local over the same repo; for a future
	// push-to-remote backend, a second clone), so a test can simulate two
	// workers racing to claim the same Chore.
	Rival() ledger.Backend
	// Branches snapshots every refs/heads/* in the underlying store, sha
	// keyed by ref name, so a case can assert the Ledger never moves one.
	Branches() map[string]string
}

// RunContract runs the shared ledger.Backend conformance suite against a
// fresh Harness per case.
func RunContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Run("EmptyLedger", func(t *testing.T) { testEmptyLedger(t, newHarness(t)) })
	t.Run("Claim", func(t *testing.T) { testClaim(t, newHarness(t)) })
	t.Run("LostRace", func(t *testing.T) { testLostRace(t, newHarness(t)) })
	t.Run("LostRaceOnEmpty", func(t *testing.T) { testLostRaceOnEmpty(t, newHarness(t)) })
	t.Run("DoneOverClaim", func(t *testing.T) { testDoneOverClaim(t, newHarness(t)) })
}

// requireNoBranchesMoved fails t if h's refs/heads/* snapshot at call time
// differs from before, the check every case runs to prove a Ledger write
// never lands on a branch.
func requireNoBranchesMoved(t *testing.T, h Harness, before map[string]string) {
	t.Helper()
	after := h.Branches()
	if len(before) != len(after) {
		t.Fatalf("refs/heads/* count changed: before %v, after %v", before, after)
	}
	for ref, sha := range before {
		if after[ref] != sha {
			t.Fatalf("%s moved: before %s, after %s", ref, sha, after[ref])
		}
	}
}

func testEmptyLedger(t *testing.T, h Harness) {
	before := h.Branches()
	tip, err := h.Backend().Read("chore-empty")
	if err != nil {
		t.Fatalf("Read on empty ledger: %v", err)
	}
	if tip.Commit != "" {
		t.Fatalf("Read on empty ledger: got Commit %q, want \"\"", tip.Commit)
	}
	var zero ledger.State
	if !reflect.DeepEqual(tip.State, zero) {
		t.Fatalf("Read on empty ledger: got State %+v, want zero value", tip.State)
	}
	requireNoBranchesMoved(t, h, before)
}

func testClaim(t *testing.T, h Harness) {
	before := h.Branches()
	b := h.Backend()
	const chore = "chore-claim"

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	by := ledger.ClaimedBy{Host: "worker-1", Slot: 3, Start: start}
	tip, err := ledger.Claim(b, chore, ledger.Tip{}, by)
	if err != nil {
		t.Fatalf("Claim over empty ledger: %v", err)
	}
	if tip.Commit == "" {
		t.Fatal("Claim over empty ledger: got empty Commit")
	}
	if tip.State.Phase != ledger.Claimed {
		t.Fatalf("Claim: got Phase %q, want %q", tip.State.Phase, ledger.Claimed)
	}
	if tip.State.ClaimedBy == nil || *tip.State.ClaimedBy != by {
		t.Fatalf("Claim: got ClaimedBy %+v, want %+v", tip.State.ClaimedBy, by)
	}

	got, err := b.Read(chore)
	if err != nil {
		t.Fatalf("Read after Claim: %v", err)
	}
	if !reflect.DeepEqual(got, tip) {
		t.Fatalf("Read after Claim: got %+v, want %+v", got, tip)
	}

	// A claim over a prior done tip carries its LastSwept/Cursor forward, so
	// a takeover of a crashed claim resumes rather than restarts a sweep.
	doneState := ledger.State{LastSwept: "2026-01-01", Cursor: "issue-42"}
	doneTip, err := ledger.Finish(b, chore, tip, doneState, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("Finish before second Claim: %v", err)
	}
	takeover, err := ledger.Claim(b, chore, doneTip, ledger.ClaimedBy{Host: "worker-2", Slot: 0, Start: start.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if takeover.State.LastSwept != doneState.LastSwept || takeover.State.Cursor != doneState.Cursor {
		t.Fatalf("takeover Claim: got LastSwept/Cursor %q/%q, want %q/%q",
			takeover.State.LastSwept, takeover.State.Cursor, doneState.LastSwept, doneState.Cursor)
	}

	requireNoBranchesMoved(t, h, before)
}

func testLostRace(t *testing.T, h Harness) {
	before := h.Branches()
	backend := h.Backend()
	rival := h.Rival()
	const chore = "chore-race"

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed, err := ledger.Claim(backend, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "seed", Start: start})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	seedDone, err := ledger.Finish(backend, chore, seed, ledger.State{}, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("seed Finish: %v", err)
	}

	// Both sides read the same tip before either claims it.
	staleFromBackend, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read (backend) before race: %v", err)
	}
	staleFromRival, err := rival.Read(chore)
	if err != nil {
		t.Fatalf("Read (rival) before race: %v", err)
	}
	if !reflect.DeepEqual(staleFromBackend, seedDone) || !reflect.DeepEqual(staleFromRival, seedDone) {
		t.Fatalf("both sides must read the seeded tip before racing")
	}

	winner, err := ledger.Claim(backend, chore, staleFromBackend, ledger.ClaimedBy{Host: "winner", Start: start.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("winning Claim: %v", err)
	}

	_, err = ledger.Claim(rival, chore, staleFromRival, ledger.ClaimedBy{Host: "loser", Start: start.Add(2 * time.Minute)})
	if !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("losing Claim: got err %v, want errors.Is(err, ledger.ErrLostRace)", err)
	}

	got, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after race: %v", err)
	}
	if !reflect.DeepEqual(got, winner) {
		t.Fatalf("Read after race: got %+v, want the winner's tip %+v", got, winner)
	}

	requireNoBranchesMoved(t, h, before)
}

// testLostRaceOnEmpty races two first claims on a Ledger with no commit yet
// (two Daemons' first sweep of a Chore): a backend treating old == "" as an
// unconditional create would let both win.
func testLostRaceOnEmpty(t *testing.T, h Harness) {
	before := h.Branches()
	backend := h.Backend()
	rival := h.Rival()
	const chore = "chore-race-empty"

	staleFromBackend, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read (backend) before race: %v", err)
	}
	staleFromRival, err := rival.Read(chore)
	if err != nil {
		t.Fatalf("Read (rival) before race: %v", err)
	}
	if staleFromBackend.Commit != "" || staleFromRival.Commit != "" {
		t.Fatalf("both sides must read an empty tip before racing")
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	winner, err := ledger.Claim(backend, chore, staleFromBackend, ledger.ClaimedBy{Host: "winner", Start: start})
	if err != nil {
		t.Fatalf("winning Claim: %v", err)
	}

	_, err = ledger.Claim(rival, chore, staleFromRival, ledger.ClaimedBy{Host: "loser", Start: start})
	if !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("losing Claim: got err %v, want errors.Is(err, ledger.ErrLostRace)", err)
	}

	got, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after race: %v", err)
	}
	if !reflect.DeepEqual(got, winner) {
		t.Fatalf("Read after race: got %+v, want the winner's tip %+v", got, winner)
	}

	requireNoBranchesMoved(t, h, before)
}

func testDoneOverClaim(t *testing.T, h Harness) {
	before := h.Branches()
	backend := h.Backend()
	rival := h.Rival()
	const chore = "chore-done"

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "worker", Start: start})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	finishState := ledger.State{
		Filed: []string{"issue-1", "issue-2"},
		Usage: usage.Usage{InputTokens: 100},
	}
	done, err := ledger.Finish(backend, chore, claim, finishState, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if done.State.Phase != ledger.Done {
		t.Fatalf("Finish: got Phase %q, want %q", done.State.Phase, ledger.Done)
	}
	if done.State.ClaimedBy != nil {
		t.Fatalf("Finish: got ClaimedBy %+v, want nil", done.State.ClaimedBy)
	}
	if len(done.State.Filed) != 2 {
		t.Fatalf("Finish: got Filed %v, want the 2 given entries", done.State.Filed)
	}

	got, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after Finish: %v", err)
	}
	if !reflect.DeepEqual(got, done) {
		t.Fatalf("Read after Finish: got %+v, want %+v", got, done)
	}

	// A Finish against a stale claim tip (someone else already re-claimed
	// the chore) must lose the race rather than clobber the re-claim.
	rivalClaim, err := ledger.Claim(rival, chore, done, ledger.ClaimedBy{Host: "rival", Start: start.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("rival re-Claim: %v", err)
	}
	_, err = ledger.Finish(backend, chore, claim, ledger.State{}, start.Add(3*time.Minute))
	if !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("stale Finish: got err %v, want errors.Is(err, ledger.ErrLostRace)", err)
	}

	got, err = backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after stale Finish: %v", err)
	}
	if !reflect.DeepEqual(got, rivalClaim) {
		t.Fatalf("Read after stale Finish: got %+v, want the rival's claim %+v", got, rivalClaim)
	}

	requireNoBranchesMoved(t, h, before)
}
