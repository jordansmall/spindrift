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

func TestNextDue(t *testing.T) {
	est := time.FixedZone("UTC-5", -5*3600)
	// 22:00 local on the 27th is already 03:00 UTC on the 28th.
	evening := time.Date(2026, 9, 27, 22, 0, 0, 0, est)
	nextLocalMidnight := time.Date(2026, 9, 28, 0, 0, 0, 0, est)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: 2 * time.Hour}
	doneTip := ledger.Tip{Commit: "c1", State: ledger.State{LastSwept: "old", Phase: ledger.Done}}
	claimStart := now.Add(-30 * time.Minute)
	newestDone := now.Add(-10 * time.Minute)

	tests := []struct {
		name       string
		tip        ledger.Tip
		recent     []ledger.Entry
		head       string
		now        time.Time
		room       chore.Room
		wantAt     time.Time
		wantOnMove bool
	}{
		{
			name:   "due returns now",
			tip:    doneTip,
			head:   "new",
			now:    now,
			wantAt: now,
		},
		{
			name: "live claim lifts one ns past the claim timeout",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: claimStart},
			}},
			head:   "new",
			now:    now,
			wantAt: claimStart.Add(cfg.ClaimTimeout + time.Nanosecond),
		},
		{
			name: "interval not elapsed lifts at newest in-window done plus Every",
			tip:  doneTip,
			recent: []ledger.Entry{
				{At: now.Add(-40 * time.Minute), State: ledger.State{Phase: ledger.Done}},
				{At: newestDone, State: ledger.State{Phase: ledger.Done}},
				{At: now.Add(-20 * time.Minute), State: ledger.State{Phase: ledger.Done}},
				{At: now.Add(-5 * time.Minute), State: ledger.State{Phase: ledger.Claimed}},
			},
			head:   "new",
			now:    now,
			wantAt: newestDone.Add(cfg.Every),
		},
		{
			name:       "nothing to scan lifts on a tip move",
			tip:        ledger.Tip{Commit: "c1", State: ledger.State{LastSwept: "h1", Phase: ledger.Done}},
			head:       "h1",
			now:        now,
			wantOnMove: true,
		},
		{
			name:   "sweep budget spent lifts at local midnight",
			tip:    doneTip,
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.SweepBudgetSpent},
			wantAt: nextLocalMidnight,
		},
		{
			name:   "finding budget spent lifts at local midnight",
			tip:    doneTip,
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.FindingBudgetSpent},
			wantAt: nextLocalMidnight,
		},
		{
			name:   "sweep findings exceed headroom lifts at local midnight",
			tip:    doneTip,
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.SweepFindingsExceedHeadroom},
			wantAt: nextLocalMidnight,
		},
		{
			name:   "token ceiling reached lifts at local midnight",
			tip:    doneTip,
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.TokenCeilingReached},
			wantAt: nextLocalMidnight,
		},
		{
			name: "interval lift before midnight with a spent budget lifts at midnight",
			tip:  doneTip,
			recent: []ledger.Entry{
				{At: evening.Add(-10 * time.Minute), State: ledger.State{Phase: ledger.Done}},
			},
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.SweepBudgetSpent},
			wantAt: nextLocalMidnight,
		},
		{
			name: "interval lift after midnight with a spent budget stays the interval lift",
			tip:  doneTip,
			recent: []ledger.Entry{
				{At: evening.Add(90 * time.Minute).Add(-10 * time.Minute), State: ledger.State{Phase: ledger.Done}},
			},
			head:   "new",
			now:    evening.Add(90 * time.Minute),
			room:   chore.Room{Reason: chore.SweepBudgetSpent},
			wantAt: evening.Add(90 * time.Minute).Add(-10 * time.Minute).Add(cfg.Every),
		},
		{
			name: "live claim lift before midnight with a spent budget lifts at midnight",
			tip: ledger.Tip{Commit: "c1", State: ledger.State{
				LastSwept: "old", Phase: ledger.Claimed,
				ClaimedBy: &ledger.ClaimedBy{Start: evening.Add(-30 * time.Minute)},
			}},
			head:   "new",
			now:    evening,
			room:   chore.Room{Reason: chore.FindingBudgetSpent},
			wantAt: nextLocalMidnight,
		},
	}
	// Santiago's DST starts at midnight, so 2026-09-06 has no 00:00: the budget
	// day must still end after now, at the next day's first instant.
	if santiago, err := time.LoadLocation("America/Santiago"); err != nil {
		t.Logf("skipping DST row: %v", err)
	} else {
		for _, c := range []struct {
			name string
			now  time.Time
		}{
			{"midday", time.Date(2026, 9, 6, 12, 0, 0, 0, santiago)},
			{"late evening", time.Date(2026, 9, 6, 23, 30, 0, 0, santiago)},
		} {
			row := tests[0]
			row.name = "budget lift across a DST jump that skips midnight, " + c.name
			row.now = c.now
			row.room = chore.Room{Reason: chore.SweepBudgetSpent}
			row.wantAt = time.Date(2026, 9, 7, 0, 0, 0, 0, time.FixedZone("-03", -3*3600))
			tests = append(tests, row)
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at, onMove := chore.NextDue(tt.tip, tt.recent, tt.head, tt.now, tt.room, cfg)
			if !at.Equal(tt.wantAt) || onMove != tt.wantOnMove {
				t.Fatalf("NextDue = (%v, %v), want (%v, %v)", at, onMove, tt.wantAt, tt.wantOnMove)
			}
			if tt.wantOnMove || at.IsZero() {
				return
			}
			// The instant is the earliest the reason lifts: Check at it no
			// longer blocks on that reason, one ns earlier still does.
			before := chore.Check(tt.tip, tt.recent, tt.head, tt.now, tt.room, cfg)
			if before == chore.Due {
				return
			}
			if tt.room.Reason != chore.Due {
				if at.Location() != tt.now.Location() || !at.After(tt.now) {
					t.Fatalf("budget lift %v is not the next local midnight of %v", at, tt.now)
				}
				return
			}
			if got := chore.Check(tt.tip, tt.recent, tt.head, at, tt.room, cfg); got == before {
				t.Errorf("Check at lift instant = %v, want the reason lifted", got)
			}
			if got := chore.Check(tt.tip, tt.recent, tt.head, at.Add(-time.Nanosecond), tt.room, cfg); got != before {
				t.Errorf("Check 1ns before lift instant = %v, want %v", got, before)
			}
		})
	}
}
