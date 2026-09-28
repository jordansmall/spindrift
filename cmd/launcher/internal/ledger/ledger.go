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

// State is one commit's state.json: the full state of a Chore's Ledger at
// that point in the chain.
type State struct {
	// LastSwept and Cursor carry a sweep's position forward across claims, so
	// a takeover of a crashed claim resumes rather than restarts (Claim
	// copies both from the prior tip).
	LastSwept string      `json:"lastSwept,omitempty"`
	Cursor    string      `json:"cursor,omitempty"`
	Phase     Phase       `json:"phase"`
	ClaimedBy *ClaimedBy  `json:"claimedBy,omitempty"`
	Filed     []string    `json:"filed,omitempty"`
	Promoted  []string    `json:"promoted,omitempty"`
	Dropped   int         `json:"dropped,omitempty"`
	Usage     usage.Usage `json:"usage"`
	// Reserved is set only on a reservation commit (see Reserve): promotion
	// slots a Claimed-phase entry holds against the day's budget before its
	// Done lands.
	Reserved int `json:"reserved,omitempty"`
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

// Reader is the read-only half of Backend: what Snapshot returns, and what
// DayTotals/DayTotalsAll need, without exposing Append.
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

// Snapshot syncs a Remote once and returns its scratch repo as a Reader, so a
// batch of Read/History calls costs one fetch. The view is live, not a copy:
// it holds only until the next call that syncs the same Remote (Read,
// History, Append, or another Snapshot) moves the scratch refs. Any other
// Backend (Local, test fakes) is returned unchanged. The Reader method set
// leaves out the scratch Local's non-pushing Append — a caller needing a
// fresh view or a compare-and-swap must go back through the Backend.
func Snapshot(b Backend) (Reader, error) {
	if r, ok := b.(Remote); ok {
		if err := r.sync(); err != nil {
			return nil, err
		}
		return r.local(), nil
	}
	return b, nil
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

// Reserve appends a reservation on top of claim, conditional on claim being
// unchanged: a copy of claim.State (Phase, ClaimedBy, LastSwept, and Cursor
// carried unchanged, so StaleClaim and a live-claim check still see a normal
// claim) with Reserved set to n. settle calls this before promoting, so the
// slots are counted by DayTotals even if the Finish that follows never lands
// (a CAS loss or a push error). The caller Finishes on top of the returned
// Tip.
func Reserve(b Backend, chore string, claim Tip, n int, at time.Time) (Tip, error) {
	s := claim.State
	s.Reserved = n
	commit, err := b.Append(chore, claim.Commit, s, at)
	if err != nil {
		return Tip{}, err
	}
	return Tip{Commit: commit, State: s}, nil
}

// Totals sums a Chore's Ledger activity over a local day: budgets (ADR 0056
// "Budgets") gate starting only, so Claims — runs started — is what they
// check against, while Filed/Promoted/Dropped/Usage are the day's completed
// work. Promoted also includes any in-flight or lost-race reservation (see
// Reserve), so an unfinished Finish still counts against the day's budget.
type Totals struct {
	Claims   int
	Filed    int
	Promoted int
	Dropped  int
	Usage    usage.Usage
}

// DayTotals totals chore's Ledger over the local day containing now —
// midnight to midnight in now.Location(), not a rolling 24h window, so a DST
// transition doesn't shift the boundary. There is no second store: it walks
// History rather than keeping a running total. A run that spans midnight is
// split across its two commits: it counts toward Claims on the day its
// Claimed commit was made, and toward Filed/Promoted/Dropped/Usage on the
// (possibly later) day its Done commit lands.
//
// A Claimed entry with Reserved > 0 is a reservation (see Reserve), not a
// claim: it never adds to Claims. It adds Reserved to Promoted unless its
// child in the chain (the next-newer entry) is a Done — that Done's own
// Promoted is the accurate count, so counting both would double-count a
// successful run. A reservation with no child yet, or whose child is a
// takeover Claim (its own Finish having been lost to the same race Reserve
// guards against), counts Reserved.
func DayTotals(b Reader, chore string, now time.Time) (Totals, error) {
	loc := now.Location()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	next := midnight.AddDate(0, 0, 1)

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
			if e.State.Reserved > 0 {
				if i > 0 && entries[i-1].State.Phase == Done {
					continue
				}
				t.Promoted += e.State.Reserved
				continue
			}
			t.Claims++
		case Done:
			t.Filed += len(e.State.Filed)
			t.Promoted += len(e.State.Promoted)
			t.Dropped += e.State.Dropped
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
