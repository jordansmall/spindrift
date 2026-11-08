package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/chore"
)

// rewaitAfterForcePush's gateResult-to-error mapping names gateTerminal and
// gateRedRetry explicitly rather than folding them into a catch-all default
// (issue #1175).
func TestInternalSeam_RewaitGateResultErr_ExplicitCases(t *testing.T) {
	cases := []struct {
		name    string
		g       gateResult
		wantErr error
		wantNil bool
		notRed  bool // must not wrap errForcePushRed: only genuine red earns a fix pass
	}{
		{name: "green", g: gateGreen, wantNil: true},
		{name: "terminal", g: gateTerminal, wantErr: errLandingNeverGreen, notRed: true},
		{name: "red-retry", g: gateRedRetry, wantErr: errLandingNeverGreen},
		{name: "red-retry marks the red cause", g: gateRedRetry, wantErr: errForcePushRed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := rewaitGateResultErr(tc.g, "", testPR)
			if tc.wantNil {
				if err != nil {
					t.Errorf("rewaitGateResultErr(%v) = %v, want nil", tc.g, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("rewaitGateResultErr(%v) = %v, want wrapping %v", tc.g, err, tc.wantErr)
			}
			if tc.notRed && errors.Is(err, errForcePushRed) {
				t.Errorf("rewaitGateResultErr(%v) = %v, must not wrap errForcePushRed", tc.g, err)
			}
		})
	}
}

// A gateResult outside the four known variants panics rather than silently
// mapping to errLandingNeverGreen, so a new variant must be handled explicitly
// (issue #1175).
func TestInternalSeam_RewaitGateResultErr_UnhandledVariantPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("rewaitGateResultErr(unknown variant) did not panic")
		}
	}()
	rewaitGateResultErr(gateResult(99), "", testPR)
}

const tuningBody = "finding text\n<!-- spindrift-dedup: foo, chore=tuning -->"

// withCatalogRows swaps the catalog settle consults for the test's duration.
func withCatalogRows(t *testing.T, rows ...chore.Row) {
	t.Helper()
	orig := catalogRows
	catalogRows = func() []chore.Row { return rows }
	t.Cleanup(func() { catalogRows = orig })
}

var (
	syntheticHold = chore.Row{Name: "synthetic", FindingLabel: "agent-synthetic-finding", MergeByHand: true}
	syntheticFree = chore.Row{Name: "freebie", FindingLabel: "agent-freebie-finding"}
)

// With several merge-by-hand rows the unreadable issue's Chore is unknown, so
// the precaution comment names them all.
func TestInternalSeam_HoldCheckErrorComment_JoinsEveryRow(t *testing.T) {
	other := chore.Row{Name: "other", FindingLabel: "agent-other-finding", MergeByHand: true}
	want := "synthetic/other provenance: could not read the closing issue (boom) — downgrading to manual as a precaution; review and merge by hand"
	if got := holdCheckErrorComment([]chore.Row{syntheticHold, other}, errors.New("boom")); got != want {
		t.Errorf("holdCheckErrorComment = %q, want %q", got, want)
	}
}
