package settle

import (
	"errors"
	"strings"
	"testing"
)

// gateTerminal is the safe zero value: a caller that never assigns a result
// gets the non-retriable case, never green.
func TestGateResult_ZeroValueIsTerminal(t *testing.T) {
	var g gateResult
	if g != gateTerminal {
		t.Errorf("zero value gateResult = %v, want gateTerminal", g)
	}
}

// An unset landing result must never read as merged or manual.
func TestLandingResult_ZeroValueIsFailed(t *testing.T) {
	var l landingResult
	if l != landingFailed {
		t.Errorf("zero value landingResult = %v, want landingFailed", l)
	}
}

// The deadline argument is non-zero on purpose: stateErr takes priority
// because it fires before any deadline check.
func TestGateTerminalReason_CheckStateError(t *testing.T) {
	err := errors.New("boom")
	got := gateTerminalReason(err, 300)
	want := "ci-check-error: boom"
	if got != want {
		t.Errorf("gateTerminalReason(err, 300) = %q, want %q", got, want)
	}
}

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

// The reason names the guard so a reader can tell "the registration guard
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

// The two ci-timeout reasons must not collide for the same deadline:
// selfHealGate's failedLabel comment (#2476) reads the text to tell the two
// timeout causes apart.
func TestGateTerminalReason_DiffersFromRegistrationVariant(t *testing.T) {
	const deadline = 300
	generic := gateTerminalReason(nil, deadline)
	registration := gateTerminalReasonRegistration(deadline)
	if generic == registration {
		t.Errorf("gateTerminalReason(nil, %d) and gateTerminalReasonRegistration(%d) produced the same reason %q, want different reasons for the two timeout flavours", deadline, deadline, generic)
	}
}
