// Package ledgertest is the executable contract for ledger.Backend: every
// backend (the local forge's Local, and the hosted-forge push backend
// Remote) runs this one suite against its own harness, so drift between
// backends fails CI.
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
// storage adapter it is. Each Backend()/Rival() call opens a fresh handle
// that sees the store as of that call (a Remote syncs its mirror once, at
// construction), and a handle's Append must be given a tip read through that
// same handle or returned by it -- reading through one handle and Appending
// through another built earlier can hand it a tip its own mirror has never
// seen.
type Harness interface {
	// Backend returns the handle under test.
	Backend() ledger.Backend
	// Rival returns a second, independent handle on the same Ledger store
	// (for Local, another Local over the same repo; for Remote, a second
	// scratch clone against the same remote), so a test can simulate two
	// workers racing to claim the same Chore. Call it at the point the
	// rival's "run" starts -- after any writes it must see -- not up front.
	Rival() ledger.Backend
	// Branches snapshots every refs/heads/* in the underlying store, sha
	// keyed by ref name, so a case can assert the Ledger never moves one. It
	// fails t on a git error rather than returning nil, so a plumbing
	// failure can't silently pass as "no branches moved".
	Branches(t *testing.T) map[string]string
}

// RunContract runs the shared ledger.Backend conformance suite against a
// fresh Harness per case.
func RunContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Run("EmptyLedger", func(t *testing.T) { testEmptyLedger(t, newHarness(t)) })
	t.Run("Claim", func(t *testing.T) { testClaim(t, newHarness(t)) })
	t.Run("LostRace", func(t *testing.T) { testLostRace(t, newHarness(t)) })
	t.Run("StaleMirror", func(t *testing.T) { testStaleMirror(t, newHarness(t)) })
	t.Run("LostRaceOnEmpty", func(t *testing.T) { testLostRaceOnEmpty(t, newHarness(t)) })
	t.Run("DoneOverClaim", func(t *testing.T) { testDoneOverClaim(t, newHarness(t)) })
	t.Run("StaleClaim", func(t *testing.T) { testStaleClaim(t, newHarness(t)) })
	t.Run("DayTotals", func(t *testing.T) { testDayTotals(t, newHarness(t)) })
	t.Run("Reserve", func(t *testing.T) { testReserve(t, newHarness(t)) })
}

// requireNoBranchesMoved fails t if h's refs/heads/* snapshot at call time
// differs from before, the check every case runs to prove a Ledger write
// never lands on a branch.
func requireNoBranchesMoved(t *testing.T, h Harness, before map[string]string) {
	t.Helper()
	after := h.Branches(t)
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
	before := h.Branches(t)
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
	before := h.Branches(t)
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
	before := h.Branches(t)
	backend := h.Backend()
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

	// Rival is obtained only now, after the seed writes it must see: its
	// handle's view is fixed as of this call.
	rival := h.Rival()

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

// testStaleMirror covers the shape testLostRace doesn't: the loser's own
// view (not just a freshly-read tip handed to it) is out of date when it
// tries to Claim. backend reads tip before rival exists, so rival wins the
// Claim it makes right after; backend then Claims against that very tip --
// stale the moment rival won -- and must lose the race rather than
// succeeding against a view of the store that no longer matches. A Remote's
// mirror only refreshes on that loss, so this is also the case that proves
// the loser's next Read reflects the winner rather than repeating its own
// stale view.
func testStaleMirror(t *testing.T, h Harness) {
	before := h.Branches(t)
	const chore = "chore-stale-mirror"

	backend := h.Backend()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed, err := ledger.Claim(backend, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "seed", Start: start})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	seedDone, err := ledger.Finish(backend, chore, seed, ledger.State{}, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("seed Finish: %v", err)
	}

	tip, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read before race: %v", err)
	}
	if !reflect.DeepEqual(tip, seedDone) {
		t.Fatalf("Read before race: got %+v, want the seeded tip %+v", tip, seedDone)
	}

	rival := h.Rival()
	rivalClaim, err := ledger.Claim(rival, chore, tip, ledger.ClaimedBy{Host: "rival", Start: start.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("rival Claim: %v", err)
	}

	_, err = ledger.Claim(backend, chore, tip, ledger.ClaimedBy{Host: "loser", Start: start.Add(2 * time.Minute)})
	if !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("stale-mirror Claim: got err %v, want errors.Is(err, ledger.ErrLostRace)", err)
	}

	got, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after losing on a stale mirror: %v", err)
	}
	if !reflect.DeepEqual(got, rivalClaim) {
		t.Fatalf("Read after losing on a stale mirror: got %+v, want the rival's claim %+v", got, rivalClaim)
	}

	requireNoBranchesMoved(t, h, before)
}

// testLostRaceOnEmpty races two first claims on a Ledger with no commit yet
// (two Daemons' first sweep of a Chore): a backend treating old == "" as an
// unconditional create would let both win.
func testLostRaceOnEmpty(t *testing.T, h Harness) {
	before := h.Branches(t)
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
	before := h.Branches(t)
	backend := h.Backend()
	const chore = "chore-done"

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim, err := ledger.Claim(backend, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "worker", Start: start})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	finishState := ledger.State{
		Filed:   []string{"issue-1", "issue-2"},
		Patched: []string{"https://example.com/pull/1"},
		Usage:   usage.Usage{InputTokens: 100},
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
	if len(done.State.Patched) != 1 {
		t.Fatalf("Finish: got Patched %v, want the 1 given entry", done.State.Patched)
	}

	got, err := backend.Read(chore)
	if err != nil {
		t.Fatalf("Read after Finish: %v", err)
	}
	if !reflect.DeepEqual(got, done) {
		t.Fatalf("Read after Finish: got %+v, want %+v", got, done)
	}

	// A Finish against a stale claim tip (someone else already re-claimed
	// the chore) must lose the race rather than clobber the re-claim. rival
	// is obtained only now, after done, so its handle's view includes it.
	rival := h.Rival()
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

func testStaleClaim(t *testing.T, h Harness) {
	before := h.Branches(t)
	b := h.Backend()
	const chore = "chore-stale"

	seedStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedClaim, err := ledger.Claim(b, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "seed", Start: seedStart})
	if err != nil {
		t.Fatalf("seed Claim: %v", err)
	}
	doneState := ledger.State{LastSwept: "2026-01-01", Cursor: "issue-7"}
	done, err := ledger.Finish(b, chore, seedClaim, doneState, seedStart.Add(time.Minute))
	if err != nil {
		t.Fatalf("seed Finish: %v", err)
	}
	if done.State.StaleClaim(seedStart.Add(24*time.Hour), time.Hour) {
		t.Fatal("a Done tip must never be reported stale")
	}

	timeout := 10 * time.Minute
	t0 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	by := ledger.ClaimedBy{Host: "crashed", Start: t0}
	claim, err := ledger.Claim(b, chore, done, by)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	got, err := b.Read(chore)
	if err != nil {
		t.Fatalf("Read after Claim: %v", err)
	}
	if got.State.ClaimedBy == nil || !got.State.ClaimedBy.Start.Equal(t0) {
		t.Fatalf("ClaimedBy.Start did not round-trip through state.json: got %+v, want Start %v", got.State.ClaimedBy, t0)
	}

	if got.State.StaleClaim(t0.Add(timeout), timeout) {
		t.Fatal("StaleClaim at exactly the timeout: got true, want false (strictly older only)")
	}
	if !got.State.StaleClaim(t0.Add(timeout).Add(time.Second), timeout) {
		t.Fatal("StaleClaim past the timeout: got false, want true")
	}

	// A Rival takes over the stale claim, carrying LastSwept/Cursor forward
	// from the done state that preceded the crashed claim. rival is obtained
	// only now, after the crashed claim, so its handle's view includes it.
	rival := h.Rival()
	takeover, err := ledger.Claim(rival, chore, claim, ledger.ClaimedBy{Host: "rescuer", Start: t0.Add(timeout).Add(time.Second)})
	if err != nil {
		t.Fatalf("takeover Claim: %v", err)
	}
	if takeover.State.LastSwept != doneState.LastSwept || takeover.State.Cursor != doneState.Cursor {
		t.Fatalf("takeover: got LastSwept/Cursor %q/%q, want %q/%q",
			takeover.State.LastSwept, takeover.State.Cursor, doneState.LastSwept, doneState.Cursor)
	}

	requireNoBranchesMoved(t, h, before)
}

func testDayTotals(t *testing.T, h Harness) {
	before := h.Branches(t)
	b := h.Backend()

	empty, err := ledger.DayTotals(b, "chore-day-empty", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DayTotals on empty ledger: %v", err)
	}
	if !reflect.DeepEqual(empty, ledger.Totals{}) {
		t.Fatalf("DayTotals on empty ledger: got %+v, want zero value", empty)
	}

	// A fixed non-UTC zone so the local day's boundary differs from the UTC
	// day's, and "today" vs "yesterday" below actually exercises that.
	loc := time.FixedZone("X", -7*3600)
	const chore = "chore-day"

	// Yesterday-local, just before local midnight: this lands on the same
	// UTC calendar day as today's runs below, so including it would only be
	// possible by bucketing on the UTC day rather than the local one.
	yesterdayClaimStart := time.Date(2026, 1, 1, 23, 50, 0, 0, loc)
	yesterdayClaim, err := ledger.Claim(b, chore, ledger.Tip{}, ledger.ClaimedBy{Host: "yesterday", Start: yesterdayClaimStart})
	if err != nil {
		t.Fatalf("yesterday Claim: %v", err)
	}
	yesterdayDone, err := ledger.Finish(b, chore, yesterdayClaim, ledger.State{
		Filed:   []string{"should-not-count"},
		Patched: []string{"should-not-count"},
		Usage:   usage.Usage{InputTokens: 999},
	}, yesterdayClaimStart.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("yesterday Finish: %v", err)
	}

	run1ClaimStart := time.Date(2026, 1, 2, 9, 0, 0, 0, loc)
	run1Claim, err := ledger.Claim(b, chore, yesterdayDone, ledger.ClaimedBy{Host: "run1", Start: run1ClaimStart})
	if err != nil {
		t.Fatalf("run1 Claim: %v", err)
	}
	run1Done, err := ledger.Finish(b, chore, run1Claim, ledger.State{
		Filed:    []string{"a", "b"},
		Promoted: []string{"x"},
		Dropped:  2,
		Patched:  []string{"p1"},
		Usage: usage.Usage{
			InputTokens:              10,
			OutputTokens:             5,
			CacheReadInputTokens:     1,
			CacheCreationInputTokens: 1,
			TotalCostUSD:             0.5,
			DurationMs:               1000,
			DurationApiMs:            800,
			NumTurns:                 3,
		},
	}, run1ClaimStart.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("run1 Finish: %v", err)
	}

	run2ClaimStart := time.Date(2026, 1, 2, 10, 0, 0, 0, loc)
	run2Claim, err := ledger.Claim(b, chore, run1Done, ledger.ClaimedBy{Host: "run2", Start: run2ClaimStart})
	if err != nil {
		t.Fatalf("run2 Claim: %v", err)
	}
	run2Done, err := ledger.Finish(b, chore, run2Claim, ledger.State{
		Filed:    []string{"c"},
		Promoted: []string{"y", "z"},
		Dropped:  1,
		Patched:  []string{"p2", "p3"},
		Usage: usage.Usage{
			InputTokens:              7,
			OutputTokens:             3,
			CacheReadInputTokens:     0,
			CacheCreationInputTokens: 2,
			TotalCostUSD:             0.25,
			DurationMs:               500,
			DurationApiMs:            400,
			NumTurns:                 2,
		},
	}, run2ClaimStart.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("run2 Finish: %v", err)
	}

	// A crashed run: claimed today, not finished until tomorrow-local (below).
	crashedStart := time.Date(2026, 1, 2, 11, 0, 0, 0, loc)
	crashedClaim, err := ledger.Claim(b, chore, run2Done, ledger.ClaimedBy{Host: "crashed", Start: crashedStart})
	if err != nil {
		t.Fatalf("crashed Claim: %v", err)
	}

	// Tomorrow-local commits, from exactly the next local midnight on (a
	// clock-ahead host's, say): none may count toward today.
	tomorrowFinishStart := time.Date(2026, 1, 3, 0, 0, 0, 0, loc)
	tomorrowDone, err := ledger.Finish(b, chore, crashedClaim, ledger.State{
		Filed:    []string{"should-not-count-tomorrow"},
		Promoted: []string{"should-not-count-tomorrow"},
		Dropped:  99,
		Patched:  []string{"should-not-count-tomorrow"},
		Usage:    usage.Usage{InputTokens: 999},
	}, tomorrowFinishStart)
	if err != nil {
		t.Fatalf("tomorrow Finish of crashed claim: %v", err)
	}

	tomorrowClaimStart := time.Date(2026, 1, 3, 1, 0, 0, 0, loc)
	tomorrowClaim, err := ledger.Claim(b, chore, tomorrowDone, ledger.ClaimedBy{Host: "tomorrow", Start: tomorrowClaimStart})
	if err != nil {
		t.Fatalf("tomorrow Claim: %v", err)
	}
	last, err := ledger.Finish(b, chore, tomorrowClaim, ledger.State{
		Filed:    []string{"should-not-count-tomorrow-2"},
		Promoted: []string{"should-not-count-tomorrow-2"},
		Dropped:  50,
		Patched:  []string{"should-not-count-tomorrow-2"},
		Usage:    usage.Usage{InputTokens: 500},
	}, tomorrowClaimStart.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("tomorrow Finish: %v", err)
	}

	now := time.Date(2026, 1, 2, 12, 0, 0, 0, loc)
	got, err := ledger.DayTotals(b, chore, now)
	if err != nil {
		t.Fatalf("DayTotals: %v", err)
	}
	want := ledger.Totals{
		Claims:   3,
		Filed:    3,
		Promoted: 3,
		Dropped:  3,
		Patched:  3,
		Usage: usage.Usage{
			InputTokens:              17,
			OutputTokens:             8,
			CacheReadInputTokens:     1,
			CacheCreationInputTokens: 3,
			TotalCostUSD:             0.75,
			DurationMs:               1500,
			DurationApiMs:            1200,
			NumTurns:                 5,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DayTotals: got %+v, want %+v", got, want)
	}

	// History must return the chain newest first (Backend's doc'd contract);
	// this chore's chain has 10 commits by now, so walk the whole thing.
	history, err := b.History(chore, yesterdayClaimStart)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	wantOrder := []string{
		last.Commit, tomorrowClaim.Commit, tomorrowDone.Commit, crashedClaim.Commit,
		run2Done.Commit, run2Claim.Commit, run1Done.Commit, run1Claim.Commit,
		yesterdayDone.Commit, yesterdayClaim.Commit,
	}
	if len(history) != len(wantOrder) {
		t.Fatalf("History: got %d entries, want %d", len(history), len(wantOrder))
	}
	for i, e := range history {
		if e.Commit != wantOrder[i] {
			t.Fatalf("History[%d]: got commit %s, want %s (newest first)", i, e.Commit, wantOrder[i])
		}
	}

	requireNoBranchesMoved(t, h, before)
}

// testReserve exercises ledger.Reserve directly: a reservation counts toward
// DayTotals' Promoted unless a Done lands directly on top of it, it is
// itself never a Claim, and it obeys the same compare-and-swap as Claim and
// Finish.
func testReserve(t *testing.T, h Harness) {
	before := h.Branches(t)
	b := h.Backend()
	day := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	t.Run("FinishedNoDoubleCount", func(t *testing.T) {
		const chore = "chore-reserve-finished"
		by := ledger.ClaimedBy{Host: "w", Start: day}
		claim, err := ledger.Claim(b, chore, ledger.Tip{}, by)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		reserved, err := ledger.Reserve(b, chore, claim, 2, day.Add(time.Minute))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if _, err := ledger.Finish(b, chore, reserved, ledger.State{
			Promoted: []string{"url"},
		}, day.Add(2*time.Minute)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		got, err := ledger.DayTotals(b, chore, day)
		if err != nil {
			t.Fatalf("DayTotals: %v", err)
		}
		if got.Promoted != 1 {
			t.Fatalf("DayTotals.Promoted: got %d, want 1 (no double count)", got.Promoted)
		}
		if got.Claims != 1 {
			t.Fatalf("DayTotals.Claims: got %d, want 1 (reservation is not a claim)", got.Claims)
		}
	})

	t.Run("InFlight", func(t *testing.T) {
		const chore = "chore-reserve-inflight"
		by := ledger.ClaimedBy{Host: "w", Start: day}
		claim, err := ledger.Claim(b, chore, ledger.Tip{}, by)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if _, err := ledger.Reserve(b, chore, claim, 2, day.Add(time.Minute)); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		got, err := ledger.DayTotals(b, chore, day)
		if err != nil {
			t.Fatalf("DayTotals: %v", err)
		}
		if got.Promoted != 2 {
			t.Fatalf("DayTotals.Promoted: got %d, want 2 (in-flight reservation counts)", got.Promoted)
		}
		if got.Claims != 1 {
			t.Fatalf("DayTotals.Claims: got %d, want 1", got.Claims)
		}
	})

	t.Run("TakeoverCounts", func(t *testing.T) {
		const chore = "chore-reserve-takeover"
		by := ledger.ClaimedBy{Host: "w", Start: day}
		claim, err := ledger.Claim(b, chore, ledger.Tip{}, by)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		reserved, err := ledger.Reserve(b, chore, claim, 2, day.Add(time.Minute))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		takeover, err := ledger.Claim(b, chore, reserved, ledger.ClaimedBy{Host: "w2", Start: day.Add(2 * time.Minute)})
		if err != nil {
			t.Fatalf("takeover Claim: %v", err)
		}
		if _, err := ledger.Finish(b, chore, takeover, ledger.State{
			Promoted: []string{},
		}, day.Add(3*time.Minute)); err != nil {
			t.Fatalf("takeover Finish: %v", err)
		}
		got, err := ledger.DayTotals(b, chore, day)
		if err != nil {
			t.Fatalf("DayTotals: %v", err)
		}
		if got.Promoted != 2 {
			t.Fatalf("DayTotals.Promoted: got %d, want 2 (lost reservation still counts)", got.Promoted)
		}
		if got.Claims != 2 {
			t.Fatalf("DayTotals.Claims: got %d, want 2 (original claim + takeover claim)", got.Claims)
		}
	})

	t.Run("LostRace", func(t *testing.T) {
		const chore = "chore-reserve-lostrace"
		by := ledger.ClaimedBy{Host: "w", Start: day}
		claim, err := ledger.Claim(b, chore, ledger.Tip{}, by)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		// Move the tip out from under claim.
		if _, err := ledger.Finish(b, chore, claim, ledger.State{}, day.Add(time.Minute)); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if _, err := ledger.Reserve(b, chore, claim, 2, day.Add(2*time.Minute)); !errors.Is(err, ledger.ErrLostRace) {
			t.Fatalf("Reserve on stale tip: got err %v, want ErrLostRace", err)
		}
	})

	t.Run("CarriesClaimFields", func(t *testing.T) {
		const chore = "chore-reserve-carry"
		by := ledger.ClaimedBy{Host: "w", Slot: 4, Start: day}
		claim, err := ledger.Claim(b, chore, ledger.Tip{}, by)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		claim.State.LastSwept = "swept-marker"
		claim.State.Cursor = "cursor-marker"
		bumped, err := ledger.Finish(b, chore, claim, ledger.State{
			LastSwept: "swept-marker",
			Cursor:    "cursor-marker",
		}, day.Add(time.Minute))
		if err != nil {
			t.Fatalf("Finish (to plant LastSwept/Cursor): %v", err)
		}
		claim2, err := ledger.Claim(b, chore, bumped, ledger.ClaimedBy{Host: "w2", Slot: 5, Start: day.Add(2 * time.Minute)})
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		reserved, err := ledger.Reserve(b, chore, claim2, 3, day.Add(3*time.Minute))
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if reserved.State.Phase != ledger.Claimed {
			t.Fatalf("Reserve: got Phase %q, want %q", reserved.State.Phase, ledger.Claimed)
		}
		if reserved.State.ClaimedBy == nil || *reserved.State.ClaimedBy != *claim2.State.ClaimedBy {
			t.Fatalf("Reserve: got ClaimedBy %+v, want %+v", reserved.State.ClaimedBy, claim2.State.ClaimedBy)
		}
		if reserved.State.LastSwept != "swept-marker" {
			t.Fatalf("Reserve: got LastSwept %q, want %q", reserved.State.LastSwept, "swept-marker")
		}
		if reserved.State.Cursor != "cursor-marker" {
			t.Fatalf("Reserve: got Cursor %q, want %q", reserved.State.Cursor, "cursor-marker")
		}
		if reserved.State.Reserved != 3 {
			t.Fatalf("Reserve: got Reserved %d, want 3", reserved.State.Reserved)
		}
	})

	requireNoBranchesMoved(t, h, before)
}
