package chore

import (
	"time"

	"spindrift.dev/launcher/internal/ledger"
)

// DueConfig bounds Check's two time-based rules, both per-Chore, plus the
// Budgets that gate starting across every enabled Chore.
type DueConfig struct {
	// Every is the interval that must elapse since the last Done commit
	// before a Chore is due again. Zero means no interval: a Chore with no
	// live claim and something to scan is always due.
	Every time.Duration
	// ClaimTimeout is the age past which a live claim is treated as stale
	// (ledger.State.StaleClaim) and taken over rather than left blocking.
	ClaimTimeout time.Duration
	// Budgets caps how much a day's runs may claim and file, checked against
	// the day's ledger.Totals across every enabled Chore (ADR 0056).
	Budgets Budgets
}

// Budgets caps a local day's butler activity. A zero field means that
// dimension has no limit; the zero value of Budgets is unlimited. Budgets
// are global across every enabled Chore, not per Chore: they gate whether a
// run may start at all, and the totals they compare against come only from
// the Ledger (no second store).
type Budgets struct {
	// MaxSweepsPerDay caps how many claims (runs started) may happen in a
	// local day, across every Chore.
	MaxSweepsPerDay int
	// MaxFindingsPerDay caps how many findings may be filed in a local day,
	// across every Chore.
	MaxFindingsPerDay int
	// MaxFindingsPerSweep caps how many findings a single sweep may file.
	// Combined with MaxFindingsPerDay it bounds one run's contribution to the
	// day (see Check's headroom comment for the rationale); when
	// MaxFindingsPerDay is 0 this field never gates starting a run, and only
	// settle's own per-sweep cap applies.
	MaxFindingsPerSweep int
	// DailyTokenCeiling caps a local day's total token usage
	// (usage.Usage.TotalTokens), across every Chore.
	DailyTokenCeiling int
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
	// SweepBudgetSpent means today's Claims already reached
	// Budgets.MaxSweepsPerDay.
	SweepBudgetSpent
	// FindingBudgetSpent means today's Filed already reached
	// Budgets.MaxFindingsPerDay.
	FindingBudgetSpent
	// SweepFindingsExceedHeadroom means today's remaining finding headroom
	// (MaxFindingsPerDay - today.Filed) is less than MaxFindingsPerSweep (see
	// Check's headroom comment for the rationale).
	SweepFindingsExceedHeadroom
	// TokenCeilingReached means today's total token usage already reached
	// Budgets.DailyTokenCeiling.
	TokenCeilingReached
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
	case SweepBudgetSpent:
		return "daily sweep budget spent"
	case FindingBudgetSpent:
		return "daily finding budget spent"
	case SweepFindingsExceedHeadroom:
		return "a full sweep's findings would exceed today's finding budget"
	case TokenCeilingReached:
		return "daily token ceiling reached"
	default:
		return "unknown"
	}
}

// Check decides whether a Chore is due to run right now: its interval has
// elapsed since its last Done, there is something to scan, no live claim
// holds it, and today's cross-Chore Budgets (cfg.Budgets) still have
// headroom. It is a pure function of tip (the Ledger's current head), recent
// (the caller's ledger.Backend.History(chore, now.Add(-cfg.Every)), newest
// first), head (the repo's current commit), now, today (the day's totals
// across every enabled Chore, e.g. ledger.DayTotalsAll), and cfg — no I/O.
//
// recent is expected to already be windowed to now.Add(-cfg.Every), but Check
// re-checks each entry's age itself (now.Sub(e.At) < cfg.Every) rather than
// trusting the caller's window precisely, so a caller that over-fetches
// doesn't change the result.
func Check(tip ledger.Tip, recent []ledger.Entry, head string, now time.Time, today ledger.Totals, cfg DueConfig) NotDue {
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

	b := cfg.Budgets
	if b.MaxSweepsPerDay > 0 && today.Claims >= b.MaxSweepsPerDay {
		return SweepBudgetSpent
	}
	if b.MaxFindingsPerDay > 0 && today.Filed >= b.MaxFindingsPerDay {
		return FindingBudgetSpent
	}
	// A full sweep could file up to MaxFindingsPerSweep more; if today's
	// remaining headroom is less than that, don't start it. This bounds a
	// single run, not concurrent ones: a claim already in flight hasn't
	// landed its Filed count in today yet, so two Chores claimed on separate
	// slots at once can each pass this check and jointly overshoot
	// MaxFindingsPerDay. When MaxFindingsPerDay is 0 this check never fires;
	// MaxFindingsPerSweep then only caps at settle, not at start.
	if b.MaxFindingsPerDay > 0 && b.MaxFindingsPerSweep > 0 && b.MaxFindingsPerDay-today.Filed < b.MaxFindingsPerSweep {
		return SweepFindingsExceedHeadroom
	}
	if b.DailyTokenCeiling > 0 && today.Usage.TotalTokens() >= b.DailyTokenCeiling {
		return TokenCeilingReached
	}

	// Fully rotated (cursor back at the top) with no new commits since the
	// last sweep: nothing new to scan.
	if tip.State.LastSwept == head && tip.State.Cursor == "" {
		return NothingToScan
	}

	return Due
}
