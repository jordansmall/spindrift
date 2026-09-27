// Package ledger holds a Chore's Ledger (ADR 0056): one ref under
// refs/spindrift/butler/ pointing at a chain of commits on no branch. Each
// commit's tree holds one state.json document; its parent is the previous
// state commit. The chain records claim/done handoffs (and, in a later
// slice, a stale-claim check and a day's usage/filing totals) without ever
// touching refs/heads/.
package ledger

import (
	"errors"
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
}

// Tip is a Ledger's current head: a state commit's sha and its decoded
// State. Commit == "" means the Ledger is empty (no commit yet).
type Tip struct {
	Commit string
	State  State
}

// ErrLostRace is returned by Backend.Append (via Claim or Finish) when the
// Ledger's ref moved since the caller's Tip was read, so its compare-and-swap
// found a different old value than expected.
var ErrLostRace = errors.New("ledger: tip moved since it was read")

// Backend is the storage adapter a Chore's Ledger is built on. The local
// forge implements it directly against its own bare Accumulation repo
// (Local); a future push-to-remote backend implements it against a client's
// working clone.
type Backend interface {
	// Read returns chore's current tip, or a zero Tip (Commit == "") if the
	// Ledger has no commit yet.
	Read(chore string) (Tip, error)
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
