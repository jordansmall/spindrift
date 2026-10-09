// Package ledger holds a Chore's Ledger (ADR 0056): one ref under
// refs/spindrift/butler/ pointing at a chain of commits on no branch. Each
// commit's tree holds one state.json document; its parent is the previous
// state commit. The chain records claim/done handoffs, detects a claim left
// stale by a crashed run, and totals a local day's usage and filings by
// walking the chain — without ever touching refs/heads/.
package ledger

import (
	"errors"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/usage"
)

// Phase is a Ledger state's lifecycle stage.
type Phase string

const (
	// Claimed marks a state committed by Claim: a worker holds the Chore.
	Claimed Phase = "claimed"
	// Done marks a state committed by Finish: the claim is over.
	Done Phase = "done"
)

// ClaimedBy identifies the worker holding a Claimed state.
type ClaimedBy struct {
	Host  string    `json:"host"`
	Slot  int       `json:"slot"`
	Start time.Time `json:"start"`
}

// Snapshot names the digest a tuning sweep was served: the sweep's Record ID
// and the digest's SHA-256, never the digest (ADR 0062).
type Snapshot struct {
	Sweep  string `json:"sweep"`
	SHA256 string `json:"sha256"`
}

// Drop is one finding settle refused to file, with the reason.
type Drop struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// State is one commit's state.json: the full state of a Chore's Ledger at
// that point in the chain.
type State struct {
	// LastSwept and Cursor carry a sweep's position forward across claims, so
	// a takeover of a crashed claim resumes rather than restarts (Claim
	// copies both from the prior tip).
	LastSwept string     `json:"lastSwept,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
	Phase     Phase      `json:"phase"`
	ClaimedBy *ClaimedBy `json:"claimedBy,omitempty"`
	Filed     []string   `json:"filed,omitempty"`
	Promoted  []string   `json:"promoted,omitempty"`
	Dropped   int        `json:"dropped,omitempty"`
	Patched   []string   `json:"patched,omitempty"` // PR URLs the run opened (ADR 0057).
	// Snapshot references the tuning Chore's digest snapshot (ADR 0062); the
	// digest itself stays in the host's Dispatch Records store, since a hosted
	// forge's Ledger ref is readable by anyone who can read the repo.
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	// Drops are the findings settle refused to file, and why (issue #4952);
	// each is also counted in Dropped.
	Drops []Drop      `json:"drops,omitempty"`
	Usage usage.Usage `json:"usage"`
	// Reserved and ReservedPatches are set only on a reservation commit (see
	// Reserve): promotion and patch (ADR 0057) slots a Claimed-phase entry
	// holds against the day's budget before its Done lands.
	Reserved        int `json:"reserved,omitempty"`
	ReservedPatches int `json:"reservedPatches,omitempty"`
}

// StaleClaim reports whether s is a claim old enough that the worker holding
// it likely crashed, so the next reader should take it over rather than wait.
// A claim is stale only strictly past timeout, never at exactly timeout; a
// Done state is never stale.
func (s State) StaleClaim(now time.Time, timeout time.Duration) bool {
	return s.Phase == Claimed && s.ClaimedBy != nil && now.Sub(s.ClaimedBy.Start) > timeout
}

// Tip is a Ledger's current head: a state commit's sha and its decoded
// State. Commit == "" means the Ledger is empty (no commit yet).
type Tip struct {
	Commit string
	State  State
}

// Entry is one state commit in a Chore's Ledger chain, as returned by
// Backend.History.
type Entry struct {
	Commit string
	At     time.Time
	State  State
}

// ErrLostRace is returned by Backend.Append (via Claim or Finish) when the
// Ledger's ref moved since the caller's Tip was read, so its compare-and-swap
// found a different old value than expected.
var ErrLostRace = errors.New("ledger: tip moved since it was read")

// Reader is the read-only half of Backend: what DayTotals/DayTotalsAll need,
// without exposing Append.
type Reader interface {
	// Read returns chore's current tip, or a zero Tip (Commit == "") if the
	// Ledger has no commit yet.
	Read(chore string) (Tip, error)
	// History returns every state commit in chore's chain whose commit date
	// is at or after since, newest first. Returns (nil, nil) if the Ledger
	// has no commit yet.
	History(chore string, since time.Time) ([]Entry, error)
}

// Backend is the storage adapter a Chore's Ledger is built on. The local
// forge implements it directly against its own bare Accumulation repo
// (Local); a hosted forge implements it against a client's working clone,
// pushed to the remote (Remote).
type Backend interface {
	Reader
	// Append commits s as chore's new tip, parented on old (no parent if old
	// == ""), and moves chore's ref from old to the new commit only if the
	// ref still equals old (old == "" requires the ref not to exist yet). at
	// becomes the commit's author and committer date. Returns ErrLostRace
	// (wrapped or bare; test with errors.Is) if the compare-and-swap loses.
	Append(chore, old string, s State, at time.Time) (string, error)
}

// Claim appends a Claimed state on top of tip, conditional on tip being
// unchanged, carrying tip.State's LastSwept and Cursor forward so a takeover
// of a crashed claim resumes rather than restarts a sweep. It does not
// itself refuse to claim over a live (non-stale) claim; that check belongs to
// the caller.
func Claim(b Backend, chore string, tip Tip, by ClaimedBy) (Tip, error) {
	s := State{
		LastSwept: tip.State.LastSwept,
		Cursor:    tip.State.Cursor,
		Phase:     Claimed,
		ClaimedBy: &by,
	}
	commit, err := b.Append(chore, tip.Commit, s, by.Start)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Commit: commit, State: s}, nil
}

// Finish appends s as chore's new tip on top of claim, conditional on claim
// being unchanged, forcing s.Phase to Done and s.ClaimedBy to nil regardless
// of what the caller set.
func Finish(b Backend, chore string, claim Tip, s State, at time.Time) (Tip, error) {
	s.Phase = Done
	s.ClaimedBy = nil
	commit, err := b.Append(chore, claim.Commit, s, at)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Commit: commit, State: s}, nil
}

// Reservation is how many slots of each day budget Reserve holds. It is
// named fields rather than two positional ints so a transposed call cannot
// compile; chore.Room can't serve, since chore imports ledger.
type Reservation struct {
	Promotions int
	Patches    int
}

// Reserve appends a reservation on top of claim, conditional on claim being
// unchanged: a copy of claim.State (Phase, ClaimedBy, LastSwept, and Cursor
// carried unchanged, so StaleClaim and a live-claim check still see a normal
// claim) with Reserved and ReservedPatches set from r. settle calls this
// before promoting or patching, so the slots are counted by DayTotals even
// if the Finish that follows never lands (a CAS loss or a push error). The
// caller Finishes on top of the returned Tip.
func Reserve(b Backend, chore string, claim Tip, r Reservation, at time.Time) (Tip, error) {
	s := claim.State
	s.Reserved = r.Promotions
	s.ReservedPatches = r.Patches
	commit, err := b.Append(chore, claim.Commit, s, at)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Commit: commit, State: s}, nil
}

// Totals sums a Chore's Ledger activity over a local day: budgets (ADR 0056
// "Budgets") gate starting only, so Claims — runs started — is what they
// check against, while Filed/Promoted/Dropped/Patched/Usage are the day's
// completed work. Promoted and Patched also include any in-flight or
// lost-race reservation (see Reserve and DayTotals), so an unfinished Finish
// still counts against the day's budget.
type Totals struct {
	Claims   int
	Filed    int
	Promoted int
	Dropped  int
	Patched  int
	Usage    usage.Usage
}

// DayTotals totals chore's Ledger over the local day containing now —
// midnight to midnight in now.Location(), not a rolling 24h window, so a DST
// transition doesn't shift the boundary. There is no second store: it walks
// History rather than keeping a running total. A run that spans midnight is
// split across its two commits: it counts toward Claims on the day its
// Claimed commit was made, and toward Filed/Promoted/Dropped/Patched/Usage on
// the (possibly later) day its Done commit lands.
//
// A Claimed entry with Reserved > 0 or ReservedPatches > 0 is a reservation
// (see Reserve), not a claim: it never adds to Claims. It adds Reserved to
// Promoted and ReservedPatches to Patched unless its child in the chain (the
// next-newer entry) is a Done — that Done's own Promoted/Patched is the
// accurate count, so counting both would double-count a successful run. A
// reservation with no child yet, or whose child is a takeover Claim (its own
// Finish having been lost to the same race Reserve guards against), counts
// Reserved and ReservedPatches. One finding may hold both: a patch candidate
// also keeps a promotion slot for its fallback.
func DayTotals(b Reader, chore string, now time.Time) (Totals, error) {
	midnight, next := DayBounds(now)

	// History's since is only a fetch bound; the [midnight, next) window is
	// owned here. entries is newest first, so entries[i-1] is entries[i]'s
	// child — kept indexed (not filtered) so an in-window reservation can
	// still look up its child's phase.
	entries, err := b.History(chore, midnight)
	if err != nil {
		return Totals{}, err
	}

	var t Totals
	for i, e := range entries {
		if e.At.Before(midnight) || !e.At.Before(next) {
			continue
		}
		switch e.State.Phase {
		case Claimed:
			if e.State.Reserved > 0 || e.State.ReservedPatches > 0 {
				if i > 0 && entries[i-1].State.Phase == Done {
					continue
				}
				t.Promoted += e.State.Reserved
				t.Patched += e.State.ReservedPatches
				continue
			}
			t.Claims++
		case Done:
			t.Filed += len(e.State.Filed)
			t.Promoted += len(e.State.Promoted)
			t.Dropped += e.State.Dropped
			t.Patched += len(e.State.Patched)
			t.Usage = t.Usage.Add(e.State.Usage)
		}
	}
	return t, nil
}

// add returns the field-wise sum of t and o, for DayTotalsAll folding one
// Chore's Totals into a running cross-Chore total.
func (t Totals) add(o Totals) Totals {
	return Totals{
		Claims:   t.Claims + o.Claims,
		Filed:    t.Filed + o.Filed,
		Promoted: t.Promoted + o.Promoted,
		Dropped:  t.Dropped + o.Dropped,
		Patched:  t.Patched + o.Patched,
		Usage:    t.Usage.Add(o.Usage),
	}
}

// DayTotalsAll sums DayTotals across chores for the local day containing
// now. Budgets (ADR 0056) are global across every enabled Chore, not
// per-Chore, so the due check needs this cross-Chore total rather than any
// single Chore's DayTotals.
func DayTotalsAll(b Reader, chores []string, now time.Time) (Totals, error) {
	var sum Totals
	for _, chore := range chores {
		t, err := DayTotals(b, chore, now)
		if err != nil {
			return Totals{}, fmt.Errorf("chore %s: %w", chore, err)
		}
		sum = sum.add(t)
	}
	return sum, nil
}

// DayBounds returns the [midnight, next) window of the local calendar day
// containing now, in now's Location, so midnight <= now < next always. Every
// consumer of the day boundary (DayTotals, chore.NextDue) must agree on it.
// Where a DST jump skips 00:00, the day starts at the first instant that
// exists (e.g. 01:00).
func DayBounds(now time.Time) (midnight, next time.Time) {
	y, m, d := now.Date()
	loc := now.Location()
	return dayStart(y, m, d, loc), dayStart(y, m, d+1, loc)
}

// dayStart is the first instant of local date y-m-d in loc. time.Date
// normalizes a nonexistent 00:00 backward into the previous day; the real day
// then starts at the zone transition that skipped it.
func dayStart(y int, m time.Month, d int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)
	// Noon is the day's anchor: d may overflow the month (d+1), and noon
	// normalizes it the same way without ever landing in a skipped hour.
	if t.YearDay() == time.Date(y, m, d, 12, 0, 0, 0, loc).YearDay() {
		return t
	}
	_, end := t.ZoneBounds()
	return end
}
