package chore_test

import (
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/usage"
)

func TestCheck(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}

	tests := []struct {
		name   string
		tip    ledger.Tip
		recent []ledger.Entry
		head   string
		room   chore.Room
		cfg    chore.DueConfig
		want   chore.NotDue
	}{
		{
			name: "empty ledger is due",
			tip:  ledger.Tip{},
			head: "h1",
			cfg:  cfg,
			want: chore.Due,
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
			want: chore.Due,
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
			want: chore.Due,
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
			want: chore.LiveClaim,
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
			want: chore.Due,
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
			want: chore.LiveClaim,
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
			want: chore.IntervalNotElapsed,
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
			want: chore.Due,
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
			want: chore.Due,
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
			cfg:  chore.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "fully rotated with head unchanged: nothing to scan",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "", Phase: ledger.Done,
			}},
			head: "head1",
			cfg:  chore.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: chore.NothingToScan,
		},
		{
			name: "fully rotated but head moved on: due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "", Phase: ledger.Done,
			}},
			head: "head2",
			cfg:  chore.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "mid-rotation cursor with lastSwept == head is due",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "head1", Cursor: "some/path", Phase: ledger.Done,
			}},
			head: "head1",
			cfg:  chore.DueConfig{Every: 0, ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "sweep budget spent",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{MaxSweepsPerDay: 3}.Room(ledger.Totals{Claims: 3}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.SweepBudgetSpent,
		},
		{
			name: "sweep budget just under limit is due",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{MaxSweepsPerDay: 3}.Room(ledger.Totals{Claims: 2}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "finding budget spent",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{MaxFindingsPerDay: 5}.Room(ledger.Totals{Filed: 5}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.FindingBudgetSpent,
		},
		{
			name: "finding budget just under limit is due",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{MaxFindingsPerDay: 5}.Room(ledger.Totals{Filed: 4}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "a full sweep would exceed today's finding headroom",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{
				MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3,
			}.Room(ledger.Totals{Filed: 8}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.SweepFindingsExceedHeadroom,
		},
		{
			name: "headroom exactly covers a full sweep is due",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{
				MaxFindingsPerDay: 10, MaxFindingsPerSweep: 3,
			}.Room(ledger.Totals{Filed: 7}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "daily token ceiling reached",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{DailyTokenCeiling: 1000}.Room(ledger.Totals{Usage: usage.Usage{InputTokens: 1000}}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.TokenCeilingReached,
		},
		{
			name: "just under the daily token ceiling is due",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{DailyTokenCeiling: 1000}.Room(ledger.Totals{Usage: usage.Usage{InputTokens: 999}}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.Due,
		},
		{
			name: "zero budgets mean unlimited despite huge totals",
			tip:  ledger.Tip{},
			head: "h1",
			room: chore.Budgets{}.Room(ledger.Totals{
				Claims: 1_000_000, Filed: 1_000_000,
				Usage: usage.Usage{InputTokens: 1_000_000},
			}),
			cfg:  chore.DueConfig{ClaimTimeout: time.Hour},
			want: chore.Due,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chore.Check(tt.tip, tt.recent, tt.head, now, tt.room, tt.cfg)
			if got != tt.want {
				t.Errorf("Check() = %d (%s), want %d (%s)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestNotDueString(t *testing.T) {
	tests := []struct {
		reason chore.NotDue
		want   string
	}{
		{chore.Due, "due"},
		{chore.LiveClaim, "claimed by another run"},
		{chore.IntervalNotElapsed, "interval not elapsed"},
		{chore.NothingToScan, "nothing to scan"},
		{chore.SweepBudgetSpent, "daily sweep budget spent"},
		{chore.FindingBudgetSpent, "daily finding budget spent"},
		{chore.SweepFindingsExceedHeadroom, "a full sweep's findings would exceed today's finding budget"},
		{chore.TokenCeilingReached, "daily token ceiling reached"},
		{chore.NotDue(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.reason.String(); got != tt.want {
			t.Errorf("NotDue(%d).String() = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestBudgetsValidate(t *testing.T) {
	tests := []struct {
		name    string
		b       chore.Budgets
		wantErr string
	}{
		{
			name: "zero budgets valid",
			b:    chore.Budgets{},
		},
		{
			name: "sweep under day valid",
			b:    chore.Budgets{MaxFindingsPerSweep: 5, MaxFindingsPerDay: 10},
		},
		{
			name: "sweep equal to day valid",
			b:    chore.Budgets{MaxFindingsPerSweep: 5, MaxFindingsPerDay: 5},
		},
		{
			name: "sweep only (no day cap) valid",
			b:    chore.Budgets{MaxFindingsPerSweep: 5},
		},
		{
			name:    "sweep exceeds day rejected",
			b:       chore.Budgets{MaxFindingsPerSweep: 6, MaxFindingsPerDay: 5},
			wantErr: "BUTLER_MAX_FINDINGS_PER_SWEEP (6) exceeds BUTLER_MAX_FINDINGS_PER_DAY (5); no run could ever start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.b.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
