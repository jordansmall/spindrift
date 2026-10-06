package ledger_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/usage"
)

// fakeBackend is a minimal in-memory ledger.Backend for exercising
// DayTotalsAll's own summing and error-wrapping logic without a real git
// round-trip per chore; DayTotals' midnight-boundary behavior is already
// covered by ledgertest's contract suite against the real backends.
type fakeBackend struct {
	history map[string][]ledger.Entry
	errFor  string
}

func (f fakeBackend) Read(chore string) (ledger.Tip, error) { return ledger.Tip{}, nil }

func (f fakeBackend) Append(chore, old string, s ledger.State, at time.Time) (string, error) {
	return "", nil
}

func (f fakeBackend) History(chore string, since time.Time) ([]ledger.Entry, error) {
	if chore == f.errFor {
		return nil, errFakeHistory
	}
	return f.history[chore], nil
}

var errFakeHistory = errors.New("fake history failure")

func TestDayTotalsAll(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	entry := func(filed, patched, tokens int) []ledger.Entry {
		return []ledger.Entry{{
			At: now,
			State: ledger.State{
				Phase:   ledger.Done,
				Filed:   make([]string, filed),
				Patched: make([]string, patched),
				Usage:   usage.Usage{InputTokens: tokens},
			},
		}}
	}
	b := fakeBackend{history: map[string][]ledger.Entry{
		"a": entry(2, 1, 10),
		"b": entry(3, 4, 20),
	}}

	got, err := ledger.DayTotalsAll(b, []string{"a", "b"}, now)
	if err != nil {
		t.Fatalf("DayTotalsAll: %v", err)
	}
	want := ledger.Totals{Filed: 5, Patched: 5, Usage: usage.Usage{InputTokens: 30}}
	if got != want {
		t.Fatalf("DayTotalsAll() = %+v, want %+v", got, want)
	}
}

func TestDayTotalsAllEmpty(t *testing.T) {
	got, err := ledger.DayTotalsAll(fakeBackend{}, nil, time.Now())
	if err != nil {
		t.Fatalf("DayTotalsAll: %v", err)
	}
	if got != (ledger.Totals{}) {
		t.Fatalf("DayTotalsAll(nil chores) = %+v, want zero value", got)
	}
}

func TestDayTotalsAllErrorWrapsChoreName(t *testing.T) {
	b := fakeBackend{errFor: "bad-chore"}
	_, err := ledger.DayTotalsAll(b, []string{"good", "bad-chore"}, time.Now())
	if err == nil {
		t.Fatal("DayTotalsAll: want error, got nil")
	}
	if !strings.Contains(err.Error(), "bad-chore") {
		t.Fatalf("DayTotalsAll error %q, want it to name the failing chore", err)
	}
}

func TestDayBounds(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// Santiago's DST starts 2026-09-06: local 00:00 does not exist and the
	// clocks jump from 23:59:59 -04 to 01:00 -03.
	m4, m3 := time.FixedZone("-04", -4*3600), time.FixedZone("-03", -3*3600)
	tests := []struct {
		name         string
		now          time.Time
		wantMidnight time.Time
		wantNext     time.Time
	}{
		{
			name:         "UTC",
			now:          time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC),
			wantMidnight: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
			wantNext:     time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name:         "day before the skipped midnight",
			now:          time.Date(2026, 9, 5, 23, 30, 0, 0, santiago),
			wantMidnight: time.Date(2026, 9, 5, 0, 0, 0, 0, m4),
			wantNext:     time.Date(2026, 9, 6, 1, 0, 0, 0, m3),
		},
		{
			name:         "early on the day with no midnight",
			now:          time.Date(2026, 9, 6, 1, 30, 0, 0, santiago),
			wantMidnight: time.Date(2026, 9, 6, 1, 0, 0, 0, m3),
			wantNext:     time.Date(2026, 9, 7, 0, 0, 0, 0, m3),
		},
		{
			name:         "late on the day with no midnight",
			now:          time.Date(2026, 9, 6, 23, 30, 0, 0, santiago),
			wantMidnight: time.Date(2026, 9, 6, 1, 0, 0, 0, m3),
			wantNext:     time.Date(2026, 9, 7, 0, 0, 0, 0, m3),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			midnight, next := ledger.DayBounds(tt.now)
			if !midnight.Equal(tt.wantMidnight) || !next.Equal(tt.wantNext) {
				t.Errorf("DayBounds = [%v, %v), want [%v, %v)", midnight, next, tt.wantMidnight, tt.wantNext)
			}
			if tt.now.Before(midnight) || !tt.now.Before(next) {
				t.Errorf("window [%v, %v) does not contain now %v", midnight, next, tt.now)
			}
		})
	}
}
