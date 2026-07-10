package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func TestBlockerReady_MergedPR(t *testing.T) {
	c := baseConfig()
	c.branchPrefix = "agent/issue-"

	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN"})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	fc.SetPRState("https://github.com/owner/repo/pull/99", forge.PRMerged)

	if !blockerReady(c, fc, "99") {
		t.Error("blockerReady: want true for merged PR, got false")
	}
}

func TestBlockerReady_OpenPRWithCompleteLabel(t *testing.T) {
	c := baseConfig()
	c.branchPrefix = "agent/issue-"

	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "99", State: "OPEN", Labels: []string{c.completeLabel}})
	fc.SetPR("agent/issue-99", forge.PR{URL: "https://github.com/owner/repo/pull/99"})
	// state defaults to OPEN when SetPR is called without SetPRState override

	if blockerReady(c, fc, "99") {
		t.Error("blockerReady: want false for open PR with agent-complete label, got true")
	}
}

func TestBlockerReady_ClosedIssueFallback(t *testing.T) {
	c := baseConfig()
	c.branchPrefix = "agent/issue-"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "99", State: "CLOSED"})
	// No PR registered — simulates human-handled work absorbed outside spindrift.

	if !blockerReady(c, fc, "99") {
		t.Error("blockerReady: want true for closed issue with no PR, got false")
	}
}
