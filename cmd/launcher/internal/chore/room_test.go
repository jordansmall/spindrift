package chore_test

import (
	"testing"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/usage"
)

func TestBudgetsRoom(t *testing.T) {
	tests := []struct {
		name  string
		b     chore.Budgets
		today ledger.Totals
		want  chore.Room
	}{
		{
			name: "zero budgets are unlimited and zero-valued",
			b:    chore.Budgets{},
			today: ledger.Totals{
				Claims: 5, Filed: 5, Promoted: 5,
				Usage: usage.Usage{InputTokens: 5},
			},
			want: chore.Room{Reason: chore.Due},
		},
		{
			name:  "sweeps remaining",
			b:     chore.Budgets{MaxSweepsPerDay: 5},
			today: ledger.Totals{Claims: 2},
			want:  chore.Room{Sweeps: 3, Reason: chore.Due},
		},
		{
			name:  "sweeps spent",
			b:     chore.Budgets{MaxSweepsPerDay: 5},
			today: ledger.Totals{Claims: 5},
			want:  chore.Room{Sweeps: 0, Reason: chore.SweepBudgetSpent},
		},
		{
			name:  "sweeps floored at zero past the limit",
			b:     chore.Budgets{MaxSweepsPerDay: 5},
			today: ledger.Totals{Claims: 9},
			want:  chore.Room{Sweeps: 0, Reason: chore.SweepBudgetSpent},
		},
		{
			name:  "tokens remaining",
			b:     chore.Budgets{DailyTokenCeiling: 1000},
			today: ledger.Totals{Usage: usage.Usage{InputTokens: 300}},
			want:  chore.Room{Tokens: 700, Reason: chore.Due},
		},
		{
			name:  "tokens reached",
			b:     chore.Budgets{DailyTokenCeiling: 1000},
			today: ledger.Totals{Usage: usage.Usage{InputTokens: 1000}},
			want:  chore.Room{Tokens: 0, Reason: chore.TokenCeilingReached},
		},
		{
			name:  "findings: day budget only",
			b:     chore.Budgets{MaxFindingsPerDay: 10},
			today: ledger.Totals{Filed: 4},
			want:  chore.Room{Findings: 6, Reason: chore.Due},
		},
		{
			name:  "findings: day budget spent",
			b:     chore.Budgets{MaxFindingsPerDay: 10},
			today: ledger.Totals{Filed: 10},
			want:  chore.Room{Findings: 0, Reason: chore.FindingBudgetSpent},
		},
		{
			name:  "findings: sweep budget only",
			b:     chore.Budgets{MaxFindingsPerSweep: 3},
			today: ledger.Totals{},
			want:  chore.Room{Findings: 3, Reason: chore.Due},
		},
		{
			name:  "findings: min of sweep and day headroom, sweep smaller",
			b:     chore.Budgets{MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3},
			today: ledger.Totals{Filed: 2},
			want:  chore.Room{Findings: 3, Reason: chore.Due},
		},
		{
			name:  "findings: min of sweep and day headroom, headroom smaller",
			b:     chore.Budgets{MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3},
			today: ledger.Totals{Filed: 8},
			want:  chore.Room{Findings: 2, Reason: chore.SweepFindingsExceedHeadroom},
		},
		{
			name:  "promotions remaining",
			b:     chore.Budgets{MaxPromotionsPerDay: 5},
			today: ledger.Totals{Promoted: 2},
			want:  chore.Room{Promotions: 3, Reason: chore.Due},
		},
		{
			name:  "promotions off (zero) never gates Reason",
			b:     chore.Budgets{MaxPromotionsPerDay: 0},
			today: ledger.Totals{Promoted: 1_000_000},
			want:  chore.Room{Promotions: 0, Reason: chore.Due},
		},
		{
			name:  "promotions floored at zero past the limit",
			b:     chore.Budgets{MaxPromotionsPerDay: 5},
			today: ledger.Totals{Promoted: 9},
			want:  chore.Room{Promotions: 0, Reason: chore.Due},
		},
		{
			name:  "patches remaining",
			b:     chore.Budgets{MaxPatchesPerDay: 5},
			today: ledger.Totals{Patched: 2},
			want:  chore.Room{Patches: 3, Reason: chore.Due},
		},
		{
			name:  "patches off (zero) never gates Reason",
			b:     chore.Budgets{MaxPatchesPerDay: 0},
			today: ledger.Totals{Patched: 1_000_000},
			want:  chore.Room{Patches: 0, Reason: chore.Due},
		},
		{
			name:  "patches floored at zero past the limit",
			b:     chore.Budgets{MaxPatchesPerDay: 5},
			today: ledger.Totals{Patched: 9},
			want:  chore.Room{Patches: 0, Reason: chore.Due},
		},
		{
			name: "first firing gate wins: sweep before finding",
			b: chore.Budgets{
				MaxSweepsPerDay: 1, MaxFindingsPerDay: 1,
			},
			today: ledger.Totals{Claims: 1, Filed: 1},
			want:  chore.Room{Sweeps: 0, Findings: 0, Reason: chore.SweepBudgetSpent},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.b.Room(tt.today)
			if got != tt.want {
				t.Errorf("Room() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
