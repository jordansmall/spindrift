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
	entry := func(filed int, tokens int) []ledger.Entry {
		return []ledger.Entry{{
			At: now,
			State: ledger.State{
				Phase: ledger.Done,
				Filed: make([]string, filed),
				Usage: usage.Usage{InputTokens: tokens},
			},
		}}
	}
	b := fakeBackend{history: map[string][]ledger.Entry{
		"a": entry(2, 10),
		"b": entry(3, 20),
	}}

	got, err := ledger.DayTotalsAll(b, []string{"a", "b"}, now)
	if err != nil {
		t.Fatalf("DayTotalsAll: %v", err)
	}
	want := ledger.Totals{Filed: 5, Usage: usage.Usage{InputTokens: 30}}
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
