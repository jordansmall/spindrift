package settle

import (
	"errors"
	"strings"
	"testing"
)

// TestGateResult_ZeroValueIsTerminal verifies the gateResult zero value is
// gateTerminal — the safe outcome (no label swap performed by the caller
// without an explicit assignment defaults to the non-retriable case, never
// to green).
func TestGateResult_ZeroValueIsTerminal(t *testing.T) {
	var g gateResult
	if g != gateTerminal {
		t.Errorf("zero value gateResult = %v, want gateTerminal", g)
	}
}

// TestLandingResult_ZeroValueIsFailed verifies the landingResult zero value
// is landingFailed — an unset landing result must never read as merged or
// manual.
func TestLandingResult_ZeroValueIsFailed(t *testing.T) {
	var l landingResult
	if l != landingFailed {
		t.Errorf("zero value landingResult = %v, want landingFailed", l)
	}
}

// TestGateTerminalReason_CheckStateError verifies a non-nil stateErr
// classifies as ci-check-error, regardless of the deadline value — the
// stateErr case takes priority since it fires before any deadline check.
func TestGateTerminalReason_CheckStateError(t *testing.T) {
	err := errors.New("boom")
	got := gateTerminalReason(err, 300)
	want := "ci-check-error: boom"
	if got != want {
		t.Errorf("gateTerminalReason(err, 300) = %q, want %q", got, want)
	}
}

// TestGateTerminalReason_DeadlineReached verifies a nil stateErr classifies
// as ci-timeout, carrying the deadline value in the message.
func TestGateTerminalReason_DeadlineReached(t *testing.T) {
	got := gateTerminalReason(nil, 300)
	if !strings.HasPrefix(got, "ci-timeout:") {
		t.Errorf("gateTerminalReason(nil, 300) = %q, want prefix %q", got, "ci-timeout:")
	}
	want := "ci-timeout: CI-watch deadline reached after 300s"
	if got != want {
		t.Errorf("gateTerminalReason(nil, 300) = %q, want %q", got, want)
	}
}
