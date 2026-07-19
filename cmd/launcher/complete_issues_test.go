package main

import (
	"bytes"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// TestDiscoverCompletionIssues_ReturnsDispatchableIssues verifies the
// completion helper lists only issues carrying the tracker's Dispatchable
// label — the same candidate set `dispatch`/`preview`/`recover` discovery
// uses (issue #556) — and ignores issues in another state.
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

// TestDiscoverCompletionIssues_TrackerError_ReturnsEmpty verifies a tracker
// error (offline, auth failure, malformed repo slug) degrades to zero
// candidates instead of surfacing the error — a shell mid-<TAB> has nowhere
// to show it (issue #556 acceptance: "never an error, never a hang").
func TestDiscoverCompletionIssues_TrackerError_ReturnsEmpty(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.ListIssuesErr = forge.ErrAuthFailure

	got := discoverCompletionIssues(f, time.Second)

	if len(got) != 0 {
		t.Errorf("discoverCompletionIssues() = %+v, want empty", got)
	}
}

// slowTracker is a forge.IssueTracker whose ListIssues blocks until delay
// elapses, standing in for an offline/slow gh or Jira query — production
// adapters shell out with no context, so discoverCompletionIssues must bound
// the wait itself rather than trust the adapter to.
type slowTracker struct {
	forge.IssueTracker
	delay time.Duration
}

func (s slowTracker) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	time.Sleep(s.delay)
	return []forge.Issue{{Number: "99", Title: "too slow to matter"}}, nil
}

// TestDiscoverCompletionIssues_SlowTracker_BoundedByTimeout verifies a
// tracker query that outlives timeout returns empty within roughly timeout,
// not the full query duration — the bounded-wait requirement so a slow or
// hung query can't wedge an interactive shell's tab-completion (issue #556).
func TestDiscoverCompletionIssues_SlowTracker_BoundedByTimeout(t *testing.T) {
	// A wide gap between timeout and delay, and a generous ceiling well
	// under delay, so ordinary scheduler jitter under a loaded CI runner
	// can't flip this from a timing artifact rather than a real bug.
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

// TestPrintCompletionIssues_TabSeparatedNumberAndTitle verifies the
// `__complete-issues` stdout contract: one `<number>\t<title>` line per
// candidate, in discovery order — fish's `complete -a` auto-splits a
// tab-separated candidate into value and description, so this exact format
// is what lets the fish renderer (issue #556) pass the raw output straight
// through with no shell-side parsing.
func TestPrintCompletionIssues_TabSeparatedNumberAndTitle(t *testing.T) {
	f := forge.NewFake(testDispatchLabels)
	f.SetIssue(forge.Issue{Number: "12", Title: "Fix the thing", State: forge.IssueOpen, Labels: []string{"ready-for-agent"}})

	var buf bytes.Buffer
	printCompletionIssues(&buf, f, time.Second)

	if got, want := buf.String(), "12\tFix the thing\n"; got != want {
		t.Errorf("printCompletionIssues() wrote %q, want %q", got, want)
	}
}
