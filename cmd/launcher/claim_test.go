package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// When the workflow has already claimed the issue (label == inProgressLabel),
// claimIssue must not issue a redundant transition.
func TestClaimIssue_SkipsWhenAlreadyClaimed(t *testing.T) {
	c := baseConfig()
	c.label = c.inProgressLabel
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "7", Labels: []string{c.inProgressLabel}})

	claimIssue(c, fc, "7")

	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("expected no TransitionState calls when already claimed, got %+v", fc.TransitionStateCalls)
	}
}

// When discovery runs off the trigger label, claimIssue transitions the issue
// to InProgress so the failure path (InProgress → Failed) stays reachable.
func TestClaimIssue_TransitionsWhenTriggered(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "7", Labels: []string{c.label}})

	claimIssue(c, fc, "7")

	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("expected exactly one TransitionState call, got %+v", fc.TransitionStateCalls)
	}
	got := fc.TransitionStateCalls[0]
	if got.Num != "7" || got.To != forge.InProgress {
		t.Errorf("want Num=7 To=InProgress, got %+v", got)
	}
}
