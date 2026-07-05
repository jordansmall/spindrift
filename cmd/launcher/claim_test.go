package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// When the workflow has already claimed the issue (label == inProgressLabel),
// claimIssue must not issue a redundant swap that adds and removes the same
// label.
func TestClaimIssue_SkipsWhenAlreadyClaimed(t *testing.T) {
	c := baseConfig()
	c.label = c.inProgressLabel
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "7", Labels: []string{c.inProgressLabel}})

	claimIssue(c, fc, "7")

	if len(fc.SwapCalls) != 0 {
		t.Errorf("expected no SwapLabel calls when already claimed, got %+v", fc.SwapCalls)
	}
}

// When discovery runs off the trigger label, claimIssue swaps the issue onto the
// in-progress label so the failure path (agent-in-progress -> agent-failed)
// stays reachable.
func TestClaimIssue_SwapsWhenTriggered(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "7", Labels: []string{c.label}})

	claimIssue(c, fc, "7")

	if len(fc.SwapCalls) != 1 {
		t.Fatalf("expected exactly one SwapLabel call, got %+v", fc.SwapCalls)
	}
	got := fc.SwapCalls[0]
	if got.Add != c.inProgressLabel || got.Remove != c.label {
		t.Errorf("swap add=%q remove=%q, want add=%q remove=%q",
			got.Add, got.Remove, c.inProgressLabel, c.label)
	}
}
