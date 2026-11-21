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

// Budgets caps a local day's butler activity across every enabled Chore,
// gating whether a run may start, against totals from the Ledger alone. A zero
// field means no limit, except where noted.
type Budgets struct {
	// MaxSweepsPerDay caps claims (runs started).
	MaxSweepsPerDay int
	// MaxFindingsPerDay caps findings filed.
	MaxFindingsPerDay int
	// MaxFindingsPerSweep caps one sweep's filings (see Budgets.Room for why
	// it gates starting). With MaxFindingsPerDay 0 it never gates a start and
	// only settle's per-sweep cap applies; this field at 0 with
	// MaxFindingsPerDay set still caps the sweep at the day's remaining
	// headroom.
	MaxFindingsPerSweep int
	// DailyTokenCeiling caps usage.Usage.TotalTokens.
	DailyTokenCeiling int
	// MaxPromotionsPerDay caps auto-promotions. 0 means promotion is off, not
	// unlimited, so it is never a start gate (see Room.Promotions).
	MaxPromotionsPerDay int
	// MaxPatchesPerDay caps patches (ADR 0057). 0 means the patch rung is off,
	// not unlimited, so it is never a start gate (see Room.Patches).
	MaxPatchesPerDay int
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
	// TooFewRecords means a records-scoped Chore (ADR 0062) has fewer new
	// settled Dispatch Records than its minimum, or none at all.
	TooFewRecords
)

// String gives the short human text the cmd layer prints as
// "chore X not due: <reason>".
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
	case TooFewRecords:
		return "too few new settled records"
	default:
		return "unknown"
	}
}

// Check decides, with no I/O, whether a Chore is due to run now: its interval
// has elapsed since its last Done, there is something to scan, no live claim
// holds it, and room still allows starting. recent is the caller's
// ledger.Backend.History(chore, now.Add(-cfg.Every)), newest first; Check
// re-checks each entry's age, so a caller that over-fetches doesn't change
// the result.
func Check(tip ledger.Tip, recent []ledger.Entry, head string, now time.Time, room Room, cfg DueConfig) NotDue {
	if r := checkGates(tip, recent, now, room, cfg); r != Due {
		return r
	}

	// Fully rotated (cursor back at the top) with no new commits since the
	// last sweep.
	if tip.State.LastSwept == head && tip.State.Cursor == "" {
		return NothingToScan
	}

	return Due
}

// CheckRecords is Check for a records-scoped Chore (ADR 0062): the
// something-to-scan test counts new settled Dispatch Records instead of
// comparing the tree head. newSettled == 0 is never due, whatever minRecords
// is: a missing or empty store arrives as 0, and a Chore with nothing to read
// must not run.
func CheckRecords(tip ledger.Tip, recent []ledger.Entry, newSettled, minRecords int, now time.Time, room Room, cfg DueConfig) NotDue {
	if r := checkGates(tip, recent, now, room, cfg); r != Due {
		return r
	}
	if newSettled <= 0 || newSettled < minRecords {
		return TooFewRecords
	}
	return Due
}

// checkGates is the claim, interval and budget prefix Check and CheckRecords
// share: Due when none blocks.
func checkGates(tip ledger.Tip, recent []ledger.Entry, now time.Time, room Room, cfg DueConfig) NotDue {
	// A stale claim is taken over, so it falls through to the checks below;
	// Claim carries LastSwept/Cursor forward, so they still see the last Done.
	if tip.State.Phase == ledger.Claimed && !tip.State.StaleClaim(now, cfg.ClaimTimeout) {
		return LiveClaim
	}

	if _, ok := newestDoneWithin(recent, now, cfg.Every); ok {
		return IntervalNotElapsed
	}

	return room.Reason
}

// newestDoneWithin is the interval rule Check and NextDue share: the time of
// the newest Done entry in recent younger than every, and whether one exists.
// A zero every means no interval, so none exists.
func newestDoneWithin(recent []ledger.Entry, now time.Time, every time.Duration) (time.Time, bool) {
	if every <= 0 {
		return time.Time{}, false
	}
	var newest time.Time
	var found bool
	for _, e := range recent {
		if e.State.Phase == ledger.Done && now.Sub(e.At) < every && (!found || e.At.After(newest)) {
			newest, found = e.At, true
		}
	}
	return newest, found
}

// NextDue maps Check's reason to the earliest instant it lifts, and whether it
// lifts only when the branch head moves. Due yields (now, false). Daily
// budgets lift at the next midnight in now's Location, matching
// ledger.DayTotals, so the caller passes now in the policy zone. A live claim
// lifts one nanosecond past the timeout, StaleClaim being strictly-after. A
// live claim or unelapsed interval masks a spent budget in Check, so the lift
// is then the later of its own and the next midnight. NothingToScan lifts only
// on a head move: (zero, true). A Claimed tip with no ClaimedBy is never
// stale: (zero, false).
func NextDue(tip ledger.Tip, recent []ledger.Entry, head string, now time.Time, room Room, cfg DueConfig) (at time.Time, onTipMove bool) {
	return nextDue(Check(tip, recent, head, now, room, cfg), tip, recent, now, room, cfg)
}

// RecordsRecheck is how often a records-scoped Chore short of Records polls the
// store. Records settle on no event the daemon watches (a failed or unmerged
// Dispatch moves no tip), so a tip move cannot lift TooFewRecords.
const RecordsRecheck = time.Hour

// NextDueRecords is NextDue for CheckRecords. TooFewRecords lifts at a dated
// re-check RecordsRecheck from now, never on a tip move.
func NextDueRecords(tip ledger.Tip, recent []ledger.Entry, newSettled, minRecords int, now time.Time, room Room, cfg DueConfig) (at time.Time, onTipMove bool) {
	return nextDue(CheckRecords(tip, recent, newSettled, minRecords, now, room, cfg), tip, recent, now, room, cfg)
}

func nextDue(reason NotDue, tip ledger.Tip, recent []ledger.Entry, now time.Time, room Room, cfg DueConfig) (at time.Time, onTipMove bool) {
	// laterOfBudget holds a lift back to the next midnight when the budget is
	// spent too, since a re-check any earlier would only meet that budget.
	laterOfBudget := func(lift time.Time) time.Time {
		if room.Reason == Due {
			return lift
		}
		if _, next := ledger.DayBounds(now); next.After(lift) {
			return next
		}
		return lift
	}
	switch reason {
	case LiveClaim:
		if tip.State.ClaimedBy == nil {
			return time.Time{}, false
		}
		return laterOfBudget(tip.State.ClaimedBy.Start.Add(cfg.ClaimTimeout + time.Nanosecond)), false
	case IntervalNotElapsed:
		newest, _ := newestDoneWithin(recent, now, cfg.Every)
		return laterOfBudget(newest.Add(cfg.Every)), false
	case NothingToScan:
		return time.Time{}, true
	case TooFewRecords:
		return laterOfBudget(now.Add(RecordsRecheck)), false
	case SweepBudgetSpent, FindingBudgetSpent, SweepFindingsExceedHeadroom, TokenCeilingReached:
		_, next := ledger.DayBounds(now)
		return next, false
	default:
		return now, false
	}
}
