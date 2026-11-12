package main

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
)

// With ISSUE_NUMBER set, discovery must target exactly that issue, never a
// different one that happens to share the in-progress label. Issue #99 in the
// fixture is such a decoy, stranded by an earlier crash.
func TestDiscoverIssues_ByNumber(t *testing.T) {
	c := baseConfig()
	c.Label = c.InProgressLabel
	c.IssueNumber = "152"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "152", Title: "the claimed one", Labels: []string{c.InProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "99", Title: "a stranded run", Labels: []string{c.InProgressLabel}})

	issues, _, origin, err := discoverIssues(c, fc, io.Discard)
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
	c.Label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Title: "ready", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Title: "not ready", Labels: []string{"backlog"}})

	issues, _, origin, err := discoverIssues(c, fc, io.Discard)
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
	c.Label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	for _, n := range []string{"3", "1", "2"} {
		fc.SetIssue(forge.Issue{Number: n, Title: "issue " + n, Labels: []string{c.Label}})
	}

	issues, _, _, err := discoverIssues(c, fc, io.Discard)
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
	c.Label = "ready-for-agent"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Title: "critical one", Labels: []string{c.Label, "agent-priority-critical"}})

	issues, _, _, err := discoverIssues(c, fc, io.Discard)
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
		name     string
		setup    func() (config, *forge.Fake)
		want     []string
		wantHeld []heldIssue
	}{
		{
			// The two families must never run on the same issue at once,
			// while an unrelated sibling issue is unaffected.
			name: "work skips research in progress",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c = applyDispatchKind(c, dispatchkind.Work)
				c.Label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "researcher has this one", Labels: []string{c.Label, "agent-research-in-progress"}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.Label}})
				return c, fc
			},
			want:     []string{"2"},
			wantHeld: []heldIssue{{"1", "agent-research-in-progress"}},
		},
		{
			// Uses the configured work in-progress label rather than a
			// hardcoded "agent-in-progress" — proves applyDispatchKind
			// captures c.InProgressLabel before swapping it for the
			// research family's.
			name: "research skips work in progress",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c.InProgressLabel = "custom-work-in-progress"
				c = applyDispatchKind(c, dispatchkind.Research)
				fc := forge.NewFake(forge.ResearchDispatchLabels())
				fc.SetIssue(forge.Issue{Number: "1", Title: "worker has this one", Labels: []string{c.Label, "custom-work-in-progress"}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.Label}})
				return c, fc
			},
			want:     []string{"2"},
			wantHeld: []heldIssue{{"1", "custom-work-in-progress"}},
		},
		{
			// The other-family filter can drop every listed issue, leaving
			// nothing to dispatch.
			name: "everything filtered yields nil",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				workInProgress := c.InProgressLabel
				c = applyDispatchKind(c, dispatchkind.Research)
				fc := forge.NewFake(forge.ResearchDispatchLabels())
				fc.SetIssue(forge.Issue{Number: "1", Title: "worker has this one", Labels: []string{c.Label, workInProgress}})
				return c, fc
			},
			want:     nil,
			wantHeld: []heldIssue{{"1", "agent-in-progress"}},
		},
		{
			// Work-only operation — no research labels defined anywhere —
			// must discover normally: the filter is a no-op when no issue
			// carries the label it checks for.
			name: "work only, no research labels anywhere",
			setup: func() (config, *forge.Fake) {
				c := baseConfig()
				c = applyDispatchKind(c, dispatchkind.Work)
				c.Label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "one", Labels: []string{c.Label}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "two", Labels: []string{c.Label}})
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
				c = applyDispatchKind(c, dispatchkind.Work)
				c.Label = "ready-for-agent"
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Title: "already claimed by a worker", Labels: []string{c.InProgressLabel}})
				fc.SetIssue(forge.Issue{Number: "2", Title: "free", Labels: []string{c.Label}})
				return c, fc
			},
			want: []string{"2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fc := tc.setup()
			issues, held, err := queryOpenIssues(c, fc)
			if err != nil {
				t.Fatalf("queryOpenIssues: %v", err)
			}
			if !reflect.DeepEqual(held, tc.wantHeld) {
				t.Fatalf("held = %+v, want %+v", held, tc.wantHeld)
			}
			var got []string
			for _, iss := range issues {
				got = append(got, iss.number)
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
	c.Label = "ready-for-agent"
	c.RepoSlug = "owner/repo"
	seen := map[string]bool{}

	var buf bytes.Buffer
	logDiscoveryPoll(c, []issue{{number: "1"}}, true, seen, &buf)
	out := buf.String()

	if !strings.Contains(out, "==> querying open 'ready-for-agent' issues in owner/repo") {
		t.Errorf("got %q, want it to contain the baseline querying-open line", out)
	}
}

// A repeated poll that finds no issue numbers beyond what seen already holds
// must stay silent. This is the steady-state noise #1666 quieted.
func TestLogDiscoveryPoll_RepeatNoNewIssues_Silent(t *testing.T) {
	c := baseConfig()
	c.Label = "ready-for-agent"
	c.RepoSlug = "owner/repo"
	seen := map[string]bool{"1": true}

	var buf bytes.Buffer
	logDiscoveryPoll(c, []issue{{number: "1"}}, false, seen, &buf)
	out := buf.String()

	if out != "" {
		t.Errorf("got %q, want no output for a poll with no new issues", out)
	}
}

// A poll must name a previously unseen issue number, so an operator watching
// the log can tell what changed, and must not re-name the ones already seen.
func TestLogDiscoveryPoll_NewIssueAppears_NamesIt(t *testing.T) {
	c := baseConfig()
	c.Label = "ready-for-agent"
	c.RepoSlug = "owner/repo"
	seen := map[string]bool{"1": true}

	var buf bytes.Buffer
	logDiscoveryPoll(c, []issue{{number: "1"}, {number: "2"}}, false, seen, &buf)
	out := buf.String()

	if !strings.Contains(out, "#2") {
		t.Errorf("got %q, want it to name newly-seen issue #2", out)
	}
	if strings.Contains(out, "#1") {
		t.Errorf("got %q, want it to not re-name already-seen issue #1", out)
	}
}

func TestEmptyQueueLine(t *testing.T) {
	cases := []struct {
		name string
		held []heldIssue
		want string
	}{
		{"none held", nil, "no open 'ready-for-agent' issues — nothing to do."},
		{"one held", []heldIssue{{"42", "agent-research-in-progress"}},
			"no dispatchable 'ready-for-agent' issues — #42 held by agent-research-in-progress."},
		{"several held", []heldIssue{{"42", "A"}, {"43", "B"}},
			"no dispatchable 'ready-for-agent' issues — #42 held by A, #43 held by B."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := emptyQueueLine("ready-for-agent", tc.held, "nothing to do"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogHeldIssues_AnnouncesEachIssueOncePerRun(t *testing.T) {
	seen := make(map[string]bool)
	a := heldIssue{number: "42", label: "agent-research-in-progress"}
	b := heldIssue{number: "43", label: "agent-research-in-progress"}

	var firstBuf bytes.Buffer
	logHeldIssues([]heldIssue{a}, seen, &firstBuf)
	first := firstBuf.String()
	if want := "==> #42 held by agent-research-in-progress — skipped\n"; first != want {
		t.Errorf("first call printed %q, want %q", first, want)
	}
	var repeatBuf bytes.Buffer
	logHeldIssues([]heldIssue{a}, seen, &repeatBuf)
	repeat := repeatBuf.String()
	if repeat != "" {
		t.Errorf("repeat call printed %q, want nothing", repeat)
	}
	var nextBuf bytes.Buffer
	logHeldIssues([]heldIssue{a, b}, seen, &nextBuf)
	next := nextBuf.String()
	if want := "==> #43 held by agent-research-in-progress — skipped\n"; next != want {
		t.Errorf("new-issue call printed %q, want %q", next, want)
	}
}

// With ISSUE_TRACKER=jira the discovery lines name the Jira project, not the
// Code Forge repo slug the query never touches (#6774).
func TestDiscovery_JiraNamesProjectNotRepoSlug(t *testing.T) {
	c := baseConfig()
	c.IssueTracker = "jira"
	c.Label = "ready-for-agent"
	c.RepoSlug = "owner/repo"
	c.RowKnobs = map[string]string{
		"JIRA_BASE_URL":    "https://example.atlassian.net",
		"JIRA_PROJECT_KEY": "BLKSMTH",
	}
	const scope = "Jira project BLKSMTH (https://example.atlassian.net)"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "BLKSMTH-12", Title: "t", Labels: []string{c.Label}})

	var buf bytes.Buffer
	if _, _, _, err := discoverIssues(c, fc, &buf); err != nil {
		t.Fatalf("discoverIssues: %v", err)
	}
	logDiscoveryPoll(c, []issue{{number: "1"}}, true, map[string]bool{}, &buf)
	logDiscoveryPoll(c, []issue{{number: "2"}}, false, map[string]bool{}, &buf)

	c.IssueNumber = "BLKSMTH-12"
	c.Label = c.InProgressLabel
	if _, _, _, err := discoverIssues(c, fc, &buf); err != nil {
		t.Fatalf("discoverIssues claimed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"==> querying open 'ready-for-agent' issues in " + scope + "\n",
		"==> querying open 'ready-for-agent' issues in " + scope + " — new: #2\n",
		"==> targeting claimed issue #BLKSMTH-12 in " + scope + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "owner/repo") {
		t.Errorf("output names the repo slug under jira:\n%s", out)
	}
}

// Forgejo and GitHub leave Strategy.IssueScope nil, so their discovery lines
// stay the repo slug; Jira with no project key falls back to it too.
func TestIssueScope_FallsBackToRepoSlug(t *testing.T) {
	for _, tc := range []struct {
		name, tracker string
		knobs         map[string]string
	}{
		{"github", "github", nil},
		{"forgejo", "forgejo", nil},
		{"jira without project key", "jira", map[string]string{"JIRA_BASE_URL": "https://example.atlassian.net"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.IssueTracker = tc.tracker
			c.RepoSlug = "owner/repo"
			c.RowKnobs = tc.knobs
			if got := issueScope(c); got != "owner/repo" {
				t.Errorf("issueScope = %q, want owner/repo", got)
			}
		})
	}
}

// The Signal socket's issue reader takes its scope from resolved config: the
// repo slug for github/forgejo, the project key for Jira, and none (never the
// slug) for a tracker without one. The scope is not exposed, so each case
// probes it: an own-scope ref reads, a foreign one is refused.
func TestIssueReader_ScopeFromConfig(t *testing.T) {
	for _, tc := range []struct {
		name, tracker string
		knobs         map[string]string
		own, foreign  string
		wantReader    bool
		wantWarn      bool
	}{
		{"github", "github", nil, "owner/repo#5", "other/repo#5", true, false},
		{"forgejo", "forgejo", nil, "owner/repo#5", "other/repo#5", true, false},
		{"jira with project key", "jira", map[string]string{"JIRA_PROJECT_KEY": "PROJ"}, "PROJ-5", "OTHER-5", true, false},
		{"jira without project key", "jira", nil, "", "", false, true},
		{"local", "local", nil, "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.IssueTracker = tc.tracker
			c.RepoSlug = "owner/repo"
			c.RowKnobs = tc.knobs
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: "5"})
			fc.SetIssue(forge.Issue{Number: "PROJ-5"})
			var warn bytes.Buffer
			r := issueReader(c, fc, &warn)
			if (r != nil) != tc.wantReader {
				t.Fatalf("issueReader non-nil = %v, want %v", r != nil, tc.wantReader)
			}
			if got := warn.Len() > 0; got != tc.wantWarn {
				t.Errorf("warn = %q, want a warning: %v", warn.String(), tc.wantWarn)
			}
			if r == nil {
				return
			}
			if _, rej := r.ReadIssue(tc.own); rej != nil {
				t.Errorf("own-scope ref %q refused: %+v", tc.own, rej)
			}
			if _, rej := r.ReadIssue(tc.foreign); rej == nil || rej.Status != "foreign_ref" {
				t.Errorf("foreign ref %q: got %+v, want foreign_ref", tc.foreign, rej)
			}
		})
	}
}

// The issue-search verb's searcher exists exactly when the issue reader does
// and the tracker can search. Absence must be an untyped nil: a typed-nil
// wrapper would defeat the handler's nil check.
func TestIssueSearcher_NeedsReaderAndCapability(t *testing.T) {
	for _, tc := range []struct {
		name, tracker string
		capability    bool
		want          bool
	}{
		{"no reader", "local", true, false},
		{"no capability", "github", false, false},
		{"reader and capability", "github", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.IssueTracker = tc.tracker
			c.RepoSlug = "owner/repo"
			fc := forge.NewFake()
			// Only the capability holds issue 7, so a searcher built over the
			// reader's tracker instead would not find it.
			searchable := forge.NewFake()
			searchable.SetIssue(forge.Issue{Number: "7", Title: "flaky widget"})
			var caps forge.Capabilities
			if tc.capability {
				caps.IssueSearcher = searchable
			}
			got := issueSearcher(issueReader(c, fc, io.Discard), caps, io.Discard)
			if (got != nil) != tc.want {
				t.Fatalf("issueSearcher non-nil = %v, want %v", got != nil, tc.want)
			}
			if got == nil {
				return
			}
			reply, rej := got.SearchIssues("widget", forge.IssueOpen, "", 5)
			if rej != nil || len(reply.Issues) != 1 || reply.Issues[0].Number != "7" {
				t.Errorf("SearchIssues = %+v, %+v; want the seeded issue 7 from the capability", reply, rej)
			}
		})
	}
}
