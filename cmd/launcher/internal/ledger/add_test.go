package ledger

import (
	"testing"

	"spindrift.dev/launcher/internal/usage"
)

// TestTotalsAdd exercises add's field-wise sum directly; DayTotalsAll's own
// tests (ledger_test.go) exercise it indirectly through the exported path.
func TestTotalsAdd(t *testing.T) {
	a := Totals{
		Claims: 1, Filed: 2, Promoted: 3, Dropped: 4,
		Usage: usage.Usage{InputTokens: 5, OutputTokens: 6},
	}
	b := Totals{
		Claims: 10, Filed: 20, Promoted: 30, Dropped: 40,
		Usage: usage.Usage{InputTokens: 50, OutputTokens: 60},
	}
	got := a.add(b)
	want := Totals{
		Claims: 11, Filed: 22, Promoted: 33, Dropped: 44,
		Usage: usage.Usage{InputTokens: 55, OutputTokens: 66},
	}
	if got != want {
		t.Fatalf("add() = %+v, want %+v", got, want)
	}
}
