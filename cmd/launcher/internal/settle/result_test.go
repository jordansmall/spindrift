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

// TestGateTerminalReasonRegistration_NamesGuard verifies the dedicated
// registration-guard timeout reason names the guard explicitly, distinct
// from the generic ci-timeout deadline-reached message — so a caller (and a
// human reading the failedLabel comment) can tell "the registration guard
// never cleared" apart from "CI just never finished" (issues #1652/#2475).
func TestGateTerminalReasonRegistration_NamesGuard(t *testing.T) {
	got := gateTerminalReasonRegistration(300)
	if !strings.HasPrefix(got, "ci-timeout:") {
		t.Errorf("gateTerminalReasonRegistration(300) = %q, want prefix %q", got, "ci-timeout:")
	}
	if !strings.Contains(got, "registration guard") {
		t.Errorf("gateTerminalReasonRegistration(300) = %q, want it to name the registration guard", got)
	}
	want := "ci-timeout: registration guard never cleared after 300s"
	if got != want {
		t.Errorf("gateTerminalReasonRegistration(300) = %q, want %q", got, want)
	}
}

// TestGateTerminalReason_DiffersFromRegistrationVariant verifies the two
// ci-timeout flavours produce different reason strings for the same
// deadline — the generic CI-watch-deadline reason from gateTerminalReason
// must never collide with the dedicated registration-guard reason from
// gateTerminalReasonRegistration, since selfHealGate's failedLabel comment
// (#2476) relies on the text to tell the two timeout causes apart.
func TestGateTerminalReason_DiffersFromRegistrationVariant(t *testing.T) {
	const deadline = 300
	generic := gateTerminalReason(nil, deadline)
	registration := gateTerminalReasonRegistration(deadline)
	if generic == registration {
		t.Errorf("gateTerminalReason(nil, %d) and gateTerminalReasonRegistration(%d) produced the same reason %q, want different reasons for the two timeout flavours", deadline, deadline, generic)
	}
}
