package main

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/waves"
)

// With ISSUE_NUMBER set, discovery must target exactly that issue, never a
// different one that happens to share the in-progress label. Issue #99 in the
// fixture is such a decoy, stranded by an earlier crash.
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

// Priority (ADR 0040, issue #2281) must survive the conversion chain from
// forge.Issue through the local issue type to waves.Issue, so that NewPlan's
// priority sort can read it.
func TestDiscoverIssues_PriorityPropagatesToWaveIssues(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Title: "critical one", Labels: []string{c.label, "agent-priority-critical"}})

	issues, _, err := discoverIssues(c, fc)
	if err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].priority != forge.PriorityCritical {
		t.Fatalf("discoverIssues priority = %+v, want PriorityCritical", issues)
	}

	waveIssues := toWaveIssues(issues)
	if len(waveIssues) != 1 || waveIssues[0].Priority != forge.PriorityCritical {
		t.Fatalf("toWaveIssues priority = %+v, want PriorityCritical", waveIssues)
	}
}

// TestQueryOpenIssues covers the cross-family in-progress filter added for
// issue #3541 (a work dispatch must skip an issue a researcher already has
// in flight, and vice versa) plus the no-op cases that prove the filter
// stays out of the way otherwise.
func TestQueryOpenIssues(t *testing.T) {
	cases := []struct {
		name  string
		setup func() (config, *forge.Fake)
		want  []string
	}{
		{
			// The two families must never run on the same issue at once,
			// while an unrelated sibling issue is unaffected.
			name: "work skips research in progress",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c = applyDispatchKind(c, dispatchKindWork)
				c.label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "researcher has this one", Labels: []string{c.label, "agent-research-in-progress"}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.label}})
				return c, fc
			},
			want: []string{"2"},
		},
		{
			// Uses the configured work in-progress label rather than a
			// hardcoded "agent-in-progress" — proves applyDispatchKind
			// captures c.inProgressLabel before swapping it for the
			// research family's.
			name: "research skips work in progress",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c.inProgressLabel = "custom-work-in-progress"
				c = applyDispatchKind(c, dispatchKindResearch)
				fc := forge.NewFake(forge.ResearchDispatchLabels())
				fc.SetIssue(forge.Issue{Number: "1", Title: "worker has this one", Labels: []string{c.label, "custom-work-in-progress"}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.label}})
				return c, fc
			},
			want: []string{"2"},
		},
		{
			// Work-only operation — no research labels defined anywhere —
			// must discover normally: the filter is a no-op when no issue
			// carries the label it checks for.
			name: "work only, no research labels anywhere",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c = applyDispatchKind(c, dispatchKindWork)
				c.label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "one", Labels: []string{c.label}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "two", Labels: []string{c.label}})
				return c, fc
			},
			want: []string{"1", "2"},
		},
		{
			// An issue carrying the *same* family's in-progress label
			// (rather than the other family's) is excluded already,
			// upstream, by the dispatchable-label ListIssues query — this
			// filter has no opinion on it one way or the other.
			name: "same family in progress excluded upstream not by this filter",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c = applyDispatchKind(c, dispatchKindWork)
				c.label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "already claimed by a worker", Labels: []string{c.inProgressLabel}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.label}})
				return c, fc
			},
			want: []string{"2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fc := tc.setup()
			issues, err := queryOpenIssues(c, fc)
			if err != nil {
				t.Fatalf("queryOpenIssues: %v", err)
			}
			got := make([]string, len(issues))
			for i, iss := range issues {
				got[i] = iss.number
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The first call always announces the baseline query, whatever the seen set
// already holds. A continuous run's very first discover must preserve that
// (#1645).
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

// A repeated poll that finds no issue numbers beyond what seen already holds
// must stay silent. This is the steady-state noise #1666 quieted.
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

// A poll must name a previously unseen issue number, so an operator watching
// the log can tell what changed, and must not re-name the ones already seen.
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
