package main

import (
	"bytes"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// The completion helper must list only issues carrying the tracker's
// Dispatchable label, the same candidate set that dispatch, preview and
// recover discovery use (issue #556).
func TestDiscoverCompletionIssues_ReturnsDispatchableIssues(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.SetIssue(forge.Issue{Number: "12", Title: "Fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "13", Title: "In progress already", State: forge.IssueOpen, Labels: []string{"agent-in-progress"}})

	got := discoverCompletionIssues(f, time.Second)

	want := []issue{{number: "12", title: "Fix the thing"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("discoverCompletionIssues() = %+v, want %+v", got, want)
	}
}

// The completion path has never printed priority (printCompletionIssues
// writes number and title only), so issue #2925 pins the drop to zero as
// deliberate rather than an accident of an unread field.
func TestDiscoverCompletionIssues_DropsPriority(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.SetIssue(forge.Issue{Number: "12", Title: "Fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent", "agent-priority-high"}})

	got := discoverCompletionIssues(f, time.Second)

	want := []issue{{number: "12", title: "Fix the thing", priority: forge.PriorityNormal}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("discoverCompletionIssues() = %+v, want %+v (priority dropped to zero value)", got, want)
	}
}

// discoverCompletionIssues must turn a tracker error into zero candidates
// rather than return it, because a shell mid-<TAB> has nowhere to show it
// (issue #556 acceptance: "never an error, never a hang").
func TestDiscoverCompletionIssues_TrackerError_ReturnsEmpty(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.ListIssuesErr = forge.ErrAuthFailure

	got := discoverCompletionIssues(f, time.Second)

	if len(got) != 0 {
		t.Errorf("discoverCompletionIssues() = %+v, want empty", got)
	}
}

// slowTracker's ListIssues blocks for delay, standing in for a slow gh or
// Jira query. Production adapters shell out with no context, so
// discoverCompletionIssues has to bound the wait itself.
type slowTracker struct {
	forge.IssueTracker
	delay time.Duration
}

func (s slowTracker) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	time.Sleep(s.delay)
	return []forge.Issue{{Number: "99", Title: "too slow to matter"}}, nil
}

// A query that outlives timeout must return empty in roughly timeout, not
// the full query duration, so a hung tracker cannot stall an interactive
// shell's tab-completion (issue #556).
func TestDiscoverCompletionIssues_SlowTracker_BoundedByTimeout(t *testing.T) {
	// timeout and delay are far apart and the ceiling is well under delay,
	// so scheduler jitter on a loaded CI runner does not fail this test.
	slow := slowTracker{delay: 2 * time.Second}
	timeout := 30 * time.Millisecond
	ceiling := 500 * time.Millisecond

	start := time.Now()
	got := discoverCompletionIssues(slow, timeout)
	elapsed := time.Since(start)

	if len(got) != 0 {
		t.Errorf("discoverCompletionIssues() = %+v, want empty", got)
	}
	if elapsed >= ceiling {
		t.Errorf("discoverCompletionIssues() took %s, want well under the %s tracker delay", elapsed, slow.delay)
	}
}

// The `__complete-issues` stdout contract is one `<number>\t<title>` line
// per candidate, in discovery order. fish's `complete -a` splits a
// tab-separated candidate into value and description, so the fish renderer
// (issue #556) passes this output through with no shell-side parsing.
func TestPrintCompletionIssues_TabSeparatedNumberAndTitle(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.SetIssue(forge.Issue{Number: "12", Title: "Fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	var buf bytes.Buffer
	printCompletionIssues(&buf, f, time.Second)

	if got, want := buf.String(), "12\tFix the thing\n"; got != want {
		t.Errorf("printCompletionIssues() wrote %q, want %q", got, want)
	}
}
