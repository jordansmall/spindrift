package butler_test

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/usage"
)

func TestCheck(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cfg := butler.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}

	tests := []struct {
		name   string
		tip    ledger.Tip
		recent []ledger.Entry
		head   string
		today  ledger.Totals
		cfg    butler.DueConfig
		want   butler.NotDue
	}{
		{
			name: "empty ledger is due",
			tip:  ledger.Tip{},
			head: "h1",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "done long ago with new head is due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Done,
			}},
			recent: []ledger.Entry{
				{At: now.Add(-2 * time.Hour), State: ledger.State{Phase: ledger.Done}},
			},
			head: "new",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "stale claim is taken over: due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old",
				Phase:     ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: now.Add(-2 * time.Hour)},
			}},
			head: "new",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "claim exactly at timeout is still live",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old",
				Phase:     ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: now.Add(-cfg.ClaimTimeout)},
			}},
			head: "new",
			cfg:  cfg,
			want: butler.LiveClaim,
		},
		{
			name: "claim past timeout is stale: due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old",
				Phase:     ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: now.Add(-cfg.ClaimTimeout - time.Second)},
			}},
			head: "new",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "claim well within timeout is live",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old",
				Phase:     ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: now.Add(-time.Minute)},
			}},
			head: "new",
			cfg:  cfg,
			want: butler.LiveClaim,
		},
		{
			name: "done inside the interval window blocks: not elapsed",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Done,
			}},
			recent: []ledger.Entry{
				{At: now.Add(-30 * time.Minute), State: ledger.State{Phase: ledger.Done}},
			},
			head: "new",
			cfg:  cfg,
			want: butler.IntervalNotElapsed,
		},
		{
			name: "done at exactly Every ago is due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Done,
			}},
			recent: []ledger.Entry{
				{At: now.Add(-cfg.Every), State: ledger.State{Phase: ledger.Done}},
			},
			head: "new",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "old done entries outside the window are ignored",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Done,
			}},
			recent: []ledger.Entry{
				// Would-be caller over-fetch: older than now-Every, must not
				// block even though it's present in recent.
				{At: now.Add(-2 * time.Hour), State: ledger.State{Phase: ledger.Done}},
			},
			head: "new",
			cfg:  cfg,
			want: butler.Due,
		},
		{
			name: "every zero disables the interval check",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Done,
			}},
			recent: []ledger.Entry{
				{At: now.Add(-time.Second), State: ledger.State{Phase: ledger.Done}},
			},
			head: "new",
			cfg:  butler.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: butler.Due,
		},
		{
			name: "fully rotated with head unchanged: nothing to scan",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "", Phase: ledger.Done,
			}},
			head: "head1",
			cfg:  butler.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: butler.NothingToScan,
		},
		{
			name: "fully rotated but head moved on: due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "", Phase: ledger.Done,
			}},
			head: "head2",
			cfg:  butler.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: butler.Due,
		},
		{
			name: "mid-rotation cursor with lastSwept == head is due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "some/path", Phase: ledger.Done,
			}},
			head: "head1",
			cfg:  butler.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: butler.Due,
		},
		{
			name:  "sweep budget spent",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Claims: 3},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{MaxSweepsPerDay: 3}},
			want:  butler.SweepBudgetSpent,
		},
		{
			name:  "sweep budget just under limit is due",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Claims: 2},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{MaxSweepsPerDay: 3}},
			want:  butler.Due,
		},
		{
			name:  "finding budget spent",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Filed: 5},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{MaxFindingsPerDay: 5}},
			want:  butler.FindingBudgetSpent,
		},
		{
			name:  "finding budget just under limit is due",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Filed: 4},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{MaxFindingsPerDay: 5}},
			want:  butler.Due,
		},
		{
			name:  "a full sweep would exceed today's finding headroom",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Filed: 8},
			cfg: butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{
				MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3,
			}},
			want: butler.SweepFindingsExceedHeadroom,
		},
		{
			name:  "headroom exactly covers a full sweep is due",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Filed: 7},
			cfg: butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{
				MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3,
			}},
			want: butler.Due,
		},
		{
			name:  "daily token ceiling reached",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Usage: usage.Usage{InputTokens: 1000}},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{DailyTokenCeiling: 1000}},
			want:  butler.TokenCeilingReached,
		},
		{
			name:  "just under the daily token ceiling is due",
			tip:   ledger.Tip{},
			head:  "h1",
			today: ledger.Totals{Usage: usage.Usage{InputTokens: 999}},
			cfg:   butler.DueConfig{ClaimTimeout: time.Hour, Budgets: butler.Budgets{DailyTokenCeiling: 1000}},
			want:  butler.Due,
		},
		{
			name: "zero budgets mean unlimited despite huge totals",
			tip:  ledger.Tip{},
			head: "h1",
			today: ledger.Totals{
				Claims: 1_000_000, Filed: 1_000_000,
				Usage: usage.Usage{InputTokens: 1_000_000},
			},
			cfg:  butler.DueConfig{ClaimTimeout: time.Hour},
			want: butler.Due,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := butler.Check(tt.tip, tt.recent, tt.head, now, tt.today, tt.cfg)
			if got != tt.want {
				t.Errorf("Check() = %d (%s), want %d (%s)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestNotDueString(t *testing.T) {
	tests := []struct {
		reason butler.NotDue
		want   string
	}{
		{butler.Due, "due"},
		{butler.LiveClaim, "claimed by another run"},
		{butler.IntervalNotElapsed, "interval not elapsed"},
		{butler.NothingToScan, "nothing to scan"},
		{butler.SweepBudgetSpent, "daily sweep budget spent"},
		{butler.FindingBudgetSpent, "daily finding budget spent"},
		{butler.SweepFindingsExceedHeadroom, "a full sweep's findings would exceed today's finding budget"},
		{butler.TokenCeilingReached, "daily token ceiling reached"},
		{butler.NotDue(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.reason.String(); got != tt.want {
			t.Errorf("NotDue(%d).String() = %q, want %q", tt.reason, got, tt.want)
		}
	}
}
