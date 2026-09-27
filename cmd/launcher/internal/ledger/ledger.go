// Package ledger holds a Chore's Ledger (ADR 0056): one ref under
// refs/spindrift/butler/ pointing at a chain of commits on no branch. Each
// commit's tree holds one state.json document; its parent is the previous
// state commit. The chain records claim/done handoffs, detects a claim left
// stale by a crashed run, and totals a local day's usage and filings by
// walking the chain — without ever touching refs/heads/.
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
	// History returns every state commit in chore's chain whose commit date
	// is at or after since, newest first. Returns (nil, nil) if the Ledger
	// has no commit yet.
	History(chore string, since time.Time) ([]Entry, error)
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

// Totals sums a Chore's Ledger activity over a local day: budgets (ADR 0056
// "Budgets") gate starting only, so Claims — runs started — is what they
// check against, while Filed/Promoted/Dropped/Usage are the day's completed
// work.
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
func DayTotals(b Backend, chore string, now time.Time) (Totals, error) {
	loc := now.Location()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	next := midnight.AddDate(0, 0, 1)

	// History's since is only a fetch bound; the [midnight, next) window is
	// owned here.
	entries, err := b.History(chore, midnight)
	if err != nil {
		return Totals{}, err
	}

	var t Totals
	for _, e := range entries {
		if e.At.Before(midnight) || !e.At.Before(next) {
			continue
		}
		switch e.State.Phase {
		case Claimed:
			t.Claims++
		case Done:
			t.Filed += len(e.State.Filed)
			t.Promoted += len(e.State.Promoted)
			t.Dropped += e.State.Dropped
			t.Usage = addUsage(t.Usage, e.State.Usage)
		}
	}
	return t, nil
}

// addUsage sums two Usage snapshots field-by-field, for DayTotals folding a
// day's Done commits into one running total.
func addUsage(a, b usage.Usage) usage.Usage {
	return usage.Usage{
		InputTokens:              a.InputTokens + b.InputTokens,
		OutputTokens:             a.OutputTokens + b.OutputTokens,
		CacheReadInputTokens:     a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens: a.CacheCreationInputTokens + b.CacheCreationInputTokens,
		TotalCostUSD:             a.TotalCostUSD + b.TotalCostUSD,
		DurationMs:               a.DurationMs + b.DurationMs,
		DurationApiMs:            a.DurationApiMs + b.DurationApiMs,
		NumTurns:                 a.NumTurns + b.NumTurns,
	}
}
