package chore

import (
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/ledger"
)

// DueConfig bounds Check's two time-based rules, both per-Chore. The budget
// gate is a Room, computed once per Sweep rather than carried here.
type DueConfig struct {
	// Every is the interval that must elapse since the last Done commit
	// before a Chore is due again. Zero means no interval: a Chore with no
	// live claim and something to scan is always due.
	Every time.Duration
	// ClaimTimeout is the age past which a live claim is treated as stale
	// (ledger.State.StaleClaim) and taken over rather than left blocking.
	ClaimTimeout time.Duration
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
	// day (see Room's doc for the rationale); when MaxFindingsPerDay is 0
	// this field never gates starting a run, and only settle's own
	// per-sweep cap applies. When this field is 0 but MaxFindingsPerDay is
	// set, the sweep is still capped at the day's remaining headroom
	// (Room.Findings): 0 means no per-sweep limit, not an uncapped run.
	MaxFindingsPerSweep int
	// DailyTokenCeiling caps a local day's total token usage
	// (usage.Usage.TotalTokens), across every Chore.
	DailyTokenCeiling int
	// MaxPromotionsPerDay caps how many findings a local day may
	// auto-promote, across every Chore. 0 means promotion is off, not
	// unlimited -- unlike this struct's other fields, so it is never a start
	// gate (see Room.Promotions).
	MaxPromotionsPerDay int
}

// Validate rejects a Budgets combination that could never let a run start:
// a per-sweep cap above the day cap would trip Room's
// SweepFindingsExceedHeadroom at Filed=0 every time.
func (b Budgets) Validate() error {
	if b.MaxFindingsPerSweep > 0 && b.MaxFindingsPerDay > 0 && b.MaxFindingsPerSweep > b.MaxFindingsPerDay {
		return fmt.Errorf("butler: BUTLER_MAX_FINDINGS_PER_SWEEP (%d) exceeds BUTLER_MAX_FINDINGS_PER_DAY (%d); no run could ever start", b.MaxFindingsPerSweep, b.MaxFindingsPerDay)
	}
	return nil
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
	// Budgets.Room's rationale).
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
// holds it, and room (today's cross-Chore budget headroom, computed once per
// Sweep by Budgets.Room) still allows starting. It is a pure function of tip
// (the Ledger's current head), recent (the caller's
// ledger.Backend.History(chore, now.Add(-cfg.Every)), newest first), head
// (the repo's current commit), now, room, and cfg — no I/O.
//
// recent is expected to already be windowed to now.Add(-cfg.Every), but Check
// re-checks each entry's age itself (now.Sub(e.At) < cfg.Every) rather than
// trusting the caller's window precisely, so a caller that over-fetches
// doesn't change the result.
func Check(tip ledger.Tip, recent []ledger.Entry, head string, now time.Time, room Room, cfg DueConfig) NotDue {
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

	if room.Reason != Due {
		return room.Reason
	}

	// Fully rotated (cursor back at the top) with no new commits since the
	// last sweep: nothing new to scan.
	if tip.State.LastSwept == head && tip.State.Cursor == "" {
		return NothingToScan
	}

	return Due
}
