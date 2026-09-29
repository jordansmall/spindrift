package chore

import "spindrift.dev/launcher/internal/ledger"

// Room is a day's remaining budget headroom, computed once per Sweep from
// Budgets and today's ledger.Totals rather than re-derived per candidate
// Chore. It is a snapshot taken at Sweep start: a concurrent run's
// claims/filings/promotions landing after it (e.g. two Chores claimed on
// separate slots, or two runs settling at once) are not seen, so every field
// below is a soft cap, not a hard one (ADR 0056).
type Room struct {
	// Sweeps is how many more claims today may still make. Zero with
	// Reason == Due means MaxSweepsPerDay is 0 (unlimited).
	Sweeps int
	// Findings is how many findings this sweep may file: the smaller of the
	// positive limits among MaxFindingsPerSweep and today's remaining
	// MaxFindingsPerDay headroom. Zero with Reason == Due means neither
	// budget limits findings.
	Findings int
	// Promotions is how many more findings today may still auto-promote.
	// Unlike the other fields, zero here means promotion is off
	// (MaxPromotionsPerDay == 0), not unlimited; Promotions never sets
	// Reason since promotion isn't a start gate.
	Promotions int
	// Tokens is how many more tokens today may still spend. Zero with
	// Reason == Due means DailyTokenCeiling is 0 (unlimited).
	Tokens int
	// Reason is the first budget gate that fires (Check's ordering), or Due
	// if none does.
	Reason NotDue
}

// Room computes today's headroom from b and today's totals. Findings bounds
// a single run: a full sweep could file up to MaxFindingsPerSweep more, so
// if today's remaining headroom (MaxFindingsPerDay - today.Filed) is less
// than that, the sweep isn't started at all (SweepFindingsExceedHeadroom)
// rather than let it file a partial batch.
func (b Budgets) Room(today ledger.Totals) Room {
	r := Room{Reason: Due}

	if b.MaxSweepsPerDay > 0 {
		r.Sweeps = max(b.MaxSweepsPerDay-today.Claims, 0)
		if r.Reason == Due && today.Claims >= b.MaxSweepsPerDay {
			r.Reason = SweepBudgetSpent
		}
	}

	if b.MaxFindingsPerDay > 0 {
		dayHeadroom := max(b.MaxFindingsPerDay-today.Filed, 0)
		switch {
		case r.Reason == Due && today.Filed >= b.MaxFindingsPerDay:
			r.Reason = FindingBudgetSpent
		case r.Reason == Due && b.MaxFindingsPerSweep > 0 && dayHeadroom < b.MaxFindingsPerSweep:
			r.Reason = SweepFindingsExceedHeadroom
		}
		if b.MaxFindingsPerSweep > 0 {
			r.Findings = min(dayHeadroom, b.MaxFindingsPerSweep)
		} else {
			r.Findings = dayHeadroom
		}
	} else if b.MaxFindingsPerSweep > 0 {
		r.Findings = b.MaxFindingsPerSweep
	}

	if b.DailyTokenCeiling > 0 {
		r.Tokens = max(b.DailyTokenCeiling-today.Usage.TotalTokens(), 0)
		if r.Reason == Due && today.Usage.TotalTokens() >= b.DailyTokenCeiling {
			r.Reason = TokenCeilingReached
		}
	}

	if b.MaxPromotionsPerDay > 0 {
		r.Promotions = max(b.MaxPromotionsPerDay-today.Promoted, 0)
	}

	return r
}
