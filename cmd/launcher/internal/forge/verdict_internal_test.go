package forge

import (
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// TestVerdictConstantsDeriveFromOutcomeStatuses locks in that the Verdict
// constants (and the unexported blockedVerdict escape hatch) are declared in
// terms of the generated outcome.ResearchStatuses vocabulary rather than
// restating it as independent string literals. The exported half of this
// invariant is also reachable from forge_test (see
// outcome_status_parity_test.go); blockedVerdict is unexported, so this file
// lives in package forge to reach it directly.
func TestVerdictConstantsDeriveFromOutcomeStatuses(t *testing.T) {
	if got, want := Recommend, Verdict(outcome.StatusRecommend); got != want {
		t.Errorf("Recommend = %q, want %q (outcome.StatusRecommend)", got, want)
	}
	if got, want := Reject, Verdict(outcome.StatusReject); got != want {
		t.Errorf("Reject = %q, want %q (outcome.StatusReject)", got, want)
	}
	if got, want := Unclear, Verdict(outcome.StatusUnclear); got != want {
		t.Errorf("Unclear = %q, want %q (outcome.StatusUnclear)", got, want)
	}
	if got, want := blockedVerdict, outcome.StatusBlocked; got != want {
		t.Errorf("blockedVerdict = %q, want %q (outcome.StatusBlocked)", got, want)
	}
}
