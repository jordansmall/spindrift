package butler

import (
	"time"

	"spindrift.dev/launcher/internal/ledger"
)

// DueConfig bounds Check's two time-based rules, both per-Chore.
type DueConfig struct {
	// Every is the interval that must elapse since the last Done commit
	// before a Chore is due again. Zero means no interval: a Chore with no
	// live claim and something to scan is always due.
	Every time.Duration
	// ClaimTimeout is the age past which a live claim is treated as stale
	// (ledger.State.StaleClaim) and taken over rather than left blocking.
	ClaimTimeout time.Duration
}

// NotDue names why Check found a Chore not due to run. The zero value, Due,
// means nothing blocks it.
type NotDue int

const (
	// Due is the zero value: no reason blocks the run.
	Due NotDue = iota
	// LiveClaim means another run already holds the Chore and its claim
	// hasn't gone stale.
	LiveClaim
	// IntervalNotElapsed means a Done commit inside the Every window
	// already swept the Chore.
	IntervalNotElapsed
	// NothingToScan means the Chore is fully rotated through the tree
	// (Cursor back at the top) and head hasn't moved since the last sweep.
	NothingToScan
)

// String gives the short human text the cmd layer prints as
// "chore X not due: <reason>". Due's own text is only used if a caller
// prints it unconditionally rather than gating on it first.
func (r NotDue) String() string {
	switch r {
	case Due:
		return "due"
	case LiveClaim:
		return "claimed by another run"
	case IntervalNotElapsed:
		return "interval not elapsed"
	case NothingToScan:
		return "nothing to scan"
	default:
		return "unknown"
	}
}

// Check decides whether a Chore is due to run right now: its interval has
// elapsed since its last Done, there is something to scan, and no live claim
// holds it. It is a pure function of tip (the Ledger's current head), recent
// (the caller's ledger.Backend.History(chore, now.Add(-cfg.Every)), newest
// first), head (the repo's current commit), now, and cfg — no I/O.
//
// recent is expected to already be windowed to now.Add(-cfg.Every), but Check
// re-checks each entry's age itself (now.Sub(e.At) < cfg.Every) rather than
// trusting the caller's window precisely, so a caller that over-fetches
// doesn't change the result.
func Check(tip ledger.Tip, recent []ledger.Entry, head string, now time.Time, cfg DueConfig) NotDue {
	// A live (non-stale) claim blocks the run outright; a stale one is
	// taken over, so it falls through to the checks below rather than
	// blocking. Claim carries LastSwept/Cursor forward from the tip it was
	// taken on, so those checks still see the last Done's position.
	if tip.State.Phase == ledger.Claimed && !tip.State.StaleClaim(now, cfg.ClaimTimeout) {
		return LiveClaim
	}

	if cfg.Every > 0 {
		for _, e := range recent {
			if e.State.Phase == ledger.Done && now.Sub(e.At) < cfg.Every {
				return IntervalNotElapsed
			}
		}
	}

	// Fully rotated (cursor back at the top) with no new commits since the
	// last sweep: nothing new to scan.
	if tip.State.LastSwept == head && tip.State.Cursor == "" {
		return NothingToScan
	}

	return Due
}
