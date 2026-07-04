package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestFake_CheckStateScript verifies that CheckState pops scripted states in
// order, returning NONE once the queue is exhausted.
func TestFake_CheckStateScript(t *testing.T) {
	f := forge.NewFake()
	const url = "https://github.com/owner/repo/pull/1"
	f.SetCheckStates(url, []forge.RollupState{forge.StatePending, forge.StateSuccess})

	s1, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s1 != forge.StatePending {
		t.Fatalf("poll 1: want PENDING, got %q", s1)
	}

	s2, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s2 != forge.StateSuccess {
		t.Fatalf("poll 2: want SUCCESS, got %q", s2)
	}

	// Queue exhausted — expect NONE.
	s3, err := f.CheckState(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s3 != forge.StateNone {
		t.Fatalf("poll 3 (exhausted): want NONE, got %q", s3)
	}
}

// TestFake_SwapLabel verifies that SwapLabel records calls and mutates Labels.
func TestFake_SwapLabel(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	if err := f.SwapLabel("42", "agent-in-progress", "ready-for-agent"); err != nil {
		t.Fatalf("SwapLabel: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "agent-in-progress" {
		t.Fatalf("want [agent-in-progress], got %v", iss.Labels)
	}
	if len(f.SwapCalls) != 1 {
		t.Fatalf("want 1 SwapCall, got %d", len(f.SwapCalls))
	}
}

// TestFake_OpenPRForBranch verifies the branch→PR lookup.
func TestFake_OpenPRForBranch(t *testing.T) {
	f := forge.NewFake()
	f.SetPR("agent/issue-7", forge.PR{URL: "https://github.com/o/r/pull/99", IsDraft: false})

	pr, ok, err := f.OpenPRForBranch("agent/issue-7")
	if err != nil || !ok {
		t.Fatalf("want (pr,true,nil); got ok=%v err=%v", ok, err)
	}
	if pr.URL != "https://github.com/o/r/pull/99" {
		t.Fatalf("wrong URL: %q", pr.URL)
	}

	_, ok2, err2 := f.OpenPRForBranch("no-such-branch")
	if err2 != nil || ok2 {
		t.Fatalf("want (_, false, nil) for missing branch; got ok=%v err=%v", ok2, err2)
	}
}
