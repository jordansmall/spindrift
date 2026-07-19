package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/waves"
)

// With ISSUE_NUMBER set, discovery must target exactly that issue — never a
// different one that happens to share the in-progress label (e.g. a run
// stranded by an earlier crash).
func TestDiscoverIssues_ByNumber(t *testing.T) {
	c := baseConfig()
	c.label = c.inProgressLabel
	c.issueNumber = "152"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "152", Title: "the claimed one", Labels: []string{c.inProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "99", Title: "a stranded run", Labels: []string{c.inProgressLabel}})

	issues, origin, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if origin != waves.OriginClaimed {
		t.Errorf("origin = %v, want OriginClaimed", origin)
	}
	if len(issues) != 1 {
		t.Fatalf("expected exactly one issue, got %+v", issues)
	}
	if issues[0].number != "152" || issues[0].title != "the claimed one" {
		t.Errorf("got %+v, want {number:152 title:the claimed one}", issues[0])
	}
}

// Without ISSUE_NUMBER, discovery falls back to querying every open issue that
// carries the discovery label.
func TestDiscoverIssues_ByLabel(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Title: "ready", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Title: "not ready", Labels: []string{"backlog"}})

	issues, origin, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if origin != waves.OriginDiscovered {
		t.Errorf("origin = %v, want OriginDiscovered", origin)
	}
	if len(issues) != 1 || issues[0].number != "1" {
		t.Fatalf("expected only issue #1 by label, got %+v", issues)
	}
}

// Issues must come back oldest-first (ascending number) regardless of the
// order they are inserted into the fake store.
func TestDiscoverIssues_OldestFirst(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	for _, n := range []string{"3", "1", "2"} {
		fc.SetIssue(forge.Issue{Number: n, Title: "issue " + n, Labels: []string{c.label}})
	}

	issues, _, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d: %+v", len(issues), issues)
	}
	want := []string{"1", "2", "3"}
	for i, iss := range issues {
		if iss.number != want[i] {
			t.Errorf("position %d: got #%s, want #%s", i, iss.number, want[i])
		}
	}
}

// logDiscoveryPoll's first call always announces the baseline query — the
// #1645 invariant a continuous run's very first discover must preserve —
// regardless of what the seen set already holds.
func TestLogDiscoveryPoll_First_AlwaysAnnounces(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"
	seen := map[string]bool{}

	out := testutil.CaptureStdout(t, func() {
		logDiscoveryPoll(c, []issue{{number: "1"}}, true, seen)
	})

	if !strings.Contains(out, "==> querying open 'ready-for-agent' issues in owner/repo") {
		t.Errorf("got %q, want it to contain the baseline querying-open line", out)
	}
}

// A repeated poll that surfaces no issue numbers beyond what's already in
// seen must stay silent — the steady-state case this issue (#1666) exists
// to quiet.
func TestLogDiscoveryPoll_RepeatNoNewIssues_Silent(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"
	seen := map[string]bool{"1": true}

	out := testutil.CaptureStdout(t, func() {
		logDiscoveryPoll(c, []issue{{number: "1"}}, false, seen)
	})

	if out != "" {
		t.Errorf("got %q, want no output for a poll with no new issues", out)
	}
}

// A poll that surfaces a previously-unseen issue number must announce and
// name it, so an operator watching the log can tell what changed.
func TestLogDiscoveryPoll_NewIssueAppears_NamesIt(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.repoSlug = "owner/repo"
	seen := map[string]bool{"1": true}

	out := testutil.CaptureStdout(t, func() {
		logDiscoveryPoll(c, []issue{{number: "1"}, {number: "2"}}, false, seen)
	})

	if !strings.Contains(out, "#2") {
		t.Errorf("got %q, want it to name newly-seen issue #2", out)
	}
	if strings.Contains(out, "#1") {
		t.Errorf("got %q, want it to not re-name already-seen issue #1", out)
	}
}
