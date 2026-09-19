package forge

import (
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// The Verdict constants must derive from the generated
// outcome.ResearchStatuses vocabulary, not restate it as independent string
// literals. forge_test covers the exported half (outcome_status_parity_test.go);
// this file is in package forge because blockedVerdict is unexported.
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
