package chore

import "spindrift.dev/launcher/internal/ledger"

// Room is a day's remaining budget headroom, snapshotted once per Sweep. A
// concurrent run landing after the snapshot (two Chores on separate slots,
// two runs settling at once) is not seen, so every field is a soft cap
// (ADR 0056).
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
	// Zero means promotion is off, not unlimited; it never sets Reason.
	Promotions int
	// Patches is how many more patches today may still land. Zero means the
	// patch rung is off, not unlimited; it never sets Reason.
	Patches int
	// Tokens is how many more tokens today may still spend. Zero with
	// Reason == Due means DailyTokenCeiling is 0 (unlimited).
	Tokens int
	// Reason is the first budget gate that fires (Check's ordering), or Due
	// if none does.
	Reason NotDue
}

// Room computes today's headroom from b and today's totals. A full sweep could
// file MaxFindingsPerSweep, so when the day's remaining headroom is less, the
// sweep isn't started at all (SweepFindingsExceedHeadroom) rather than file a
// partial batch.
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

	if b.MaxPatchesPerDay > 0 {
		r.Patches = max(b.MaxPatchesPerDay-today.Patched, 0)
	}

	return r
}
