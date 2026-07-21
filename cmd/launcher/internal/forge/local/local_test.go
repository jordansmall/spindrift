package local

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix and pinned against the agent workflows by
// nix/checks/dispatch-labels.nix (issue #460). NewFake and the production
// adapters (Exec, Local, Jira) take labels as an explicit constructor
// argument rather than baking in a copy, so this package's tests share this
// one value instead of each restating the four label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// writeLocalIssue writes an issue file named slug+".md" under dir, using
// testLabels' native marker for state.
func writeLocalIssue(t *testing.T, dir, slug string, li localIssue) {
	t.Helper()
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(li.render()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLocalTracker_ImplementsIssueTracker(t *testing.T) {
	var _ forge.IssueTracker = NewLocalTracker(t.TempDir(), testLabels)
}

func TestLocalTracker_ImplementsSeamLister(t *testing.T) {
	var _ forge.SeamLister = NewLocalTracker(t.TempDir(), testLabels)
}

func TestParseLocalIssue_Frontmatter(t *testing.T) {
	data := []byte(`---
title: Fix the thing
state: ready-for-agent
labels: [bug, priority-high]
created: 2026-07-09T12:00:00Z
---
## What to build

Do the thing.
`)
	li, err := parseLocalIssue(data)
	if err != nil {
		t.Fatalf("parseLocalIssue: %v", err)
	}
	if li.frontmatter.Title != "Fix the thing" {
		t.Errorf("Title = %q, want %q", li.frontmatter.Title, "Fix the thing")
	}
	if li.frontmatter.State != "ready-for-agent" {
		t.Errorf("State = %q, want %q", li.frontmatter.State, "ready-for-agent")
	}
	wantLabels := []string{"bug", "priority-high"}
	if !reflect.DeepEqual(li.frontmatter.Labels, wantLabels) {
		t.Errorf("Labels = %v, want %v", li.frontmatter.Labels, wantLabels)
	}
	if li.frontmatter.Created != "2026-07-09T12:00:00Z" {
		t.Errorf("Created = %q, want %q", li.frontmatter.Created, "2026-07-09T12:00:00Z")
	}
	wantBody := "## What to build\n\nDo the thing.\n"
	if li.body != wantBody {
		t.Errorf("body = %q, want %q", li.body, wantBody)
	}
}

func TestLocalIssue_RenderParseRoundTrip(t *testing.T) {
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:   "Fix the thing",
			State:   "ready-for-agent",
			Labels:  []string{"bug", "priority-high"},
			Created: "2026-07-09T12:00:00Z",
			Parent:  "parent-slug",
		},
		body: "## What to build\n\nDo the thing.\n",
	}
	got, err := parseLocalIssue([]byte(li.render()))
	if err != nil {
		t.Fatalf("parseLocalIssue(render()): %v", err)
	}
	if !reflect.DeepEqual(got, li) {
		t.Errorf("round trip = %+v, want %+v", got, li)
	}
}

// TestLocalIssue_RenderParseRoundTrip_Closed verifies closed: survives a
// parse/render round trip alongside the other frontmatter fields, mirroring
// TestLocalIssue_RenderParseRoundTrip's open-issue case.
func TestLocalIssue_RenderParseRoundTrip_Closed(t *testing.T) {
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:   "Fix the thing",
			State:   "agent-complete",
			Labels:  []string{"bug"},
			Created: "2026-07-09T12:00:00Z",
			Parent:  "parent-slug",
			Closed:  true,
		},
		body: "## What to build\n\nDo the thing.\n",
	}
	got, err := parseLocalIssue([]byte(li.render()))
	if err != nil {
		t.Fatalf("parseLocalIssue(render()): %v", err)
	}
	if !reflect.DeepEqual(got, li) {
		t.Errorf("round trip = %+v, want %+v", got, li)
	}
}

// TestLocalIssue_RenderParseRoundTrip_Landing verifies landing: survives a
// parse/render round trip alongside the other frontmatter fields — the
// immutable landing ref RecordLanding writes (ADR 0029).
func TestLocalIssue_RenderParseRoundTrip_Landing(t *testing.T) {
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:   "Fix the thing",
			State:   "agent-complete",
			Created: "2026-07-09T12:00:00Z",
			Landing: "https://github.com/o/r/pull/1",
		},
		body: "## What to build\n\nDo the thing.\n",
	}
	got, err := parseLocalIssue([]byte(li.render()))
	if err != nil {
		t.Fatalf("parseLocalIssue(render()): %v", err)
	}
	if !reflect.DeepEqual(got, li) {
		t.Errorf("round trip = %+v, want %+v", got, li)
	}
}

// TestLocalIssue_RenderParseRoundTrip_Abandoned verifies abandoned: survives
// a parse/render round trip alongside the other frontmatter fields — set by
// FlagAbandoned when a landing PR closes without merging (ADR 0029).
func TestLocalIssue_RenderParseRoundTrip_Abandoned(t *testing.T) {
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:     "Fix the thing",
			State:     "agent-in-progress",
			Created:   "2026-07-09T12:00:00Z",
			Landing:   "https://github.com/o/r/pull/1",
			Abandoned: true,
		},
		body: "## What to build\n\nDo the thing.\n",
	}
	got, err := parseLocalIssue([]byte(li.render()))
	if err != nil {
		t.Fatalf("parseLocalIssue(render()): %v", err)
	}
	if !reflect.DeepEqual(got, li) {
		t.Errorf("round trip = %+v, want %+v", got, li)
	}
}

func TestLocalTracker_ListIssues_OrderedByCreated(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "second", localIssue{frontmatter: localFrontmatter{
		Title: "Second", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "first", localIssue{frontmatter: localFrontmatter{
		Title: "First", State: labels.Dispatchable, Created: "2026-07-08T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "in-progress", localIssue{frontmatter: localFrontmatter{
		Title: "In Progress", State: labels.InProgress, Created: "2026-07-07T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2: %+v", len(issues), issues)
	}
	if issues[0].Number != "first" || issues[1].Number != "second" {
		t.Errorf("order = [%s, %s], want [first, second]", issues[0].Number, issues[1].Number)
	}
}

// TestLocalTracker_ListOpenIssues_AllStatesOrderedByCreated verifies
// ListOpenIssues returns every issue regardless of its frontmatter state
// marker, ordered by created ascending — unlike ListIssues, which filters
// to a single state.
func TestLocalTracker_ListOpenIssues_AllStatesOrderedByCreated(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "second", localIssue{frontmatter: localFrontmatter{
		Title: "Second", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "first", localIssue{frontmatter: localFrontmatter{
		Title: "First", State: labels.Dispatchable, Created: "2026-07-08T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "in-progress", localIssue{frontmatter: localFrontmatter{
		Title: "In Progress", State: labels.InProgress, Created: "2026-07-07T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("len(issues) = %d, want 3: %+v", len(issues), issues)
	}
	if issues[0].Number != "in-progress" || issues[1].Number != "first" || issues[2].Number != "second" {
		t.Errorf("order = [%s, %s, %s], want [in-progress, first, second]",
			issues[0].Number, issues[1].Number, issues[2].Number)
	}
}

// TestLocalTracker_ListOpenIssues_ExcludesClosed verifies ListOpenIssues
// drops a closed: true issue from the backlog, matching forge.Fake's own
// closed-exclusion behavior (ADR 0029).
func TestLocalTracker_ListOpenIssues_ExcludesClosed(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "open", localIssue{frontmatter: localFrontmatter{
		Title: "Open", State: labels.Dispatchable, Created: "2026-07-08T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "closed", localIssue{frontmatter: localFrontmatter{
		Title: "Closed", State: labels.Complete, Created: "2026-07-09T12:00:00Z", Closed: true,
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "open" {
		t.Errorf("ListOpenIssues = %+v, want [open]", issues)
	}
}

// TestLocalTracker_ListIssues_ExcludesClosed verifies ListIssues drops a
// closed: true issue even when its dispatch state marker still matches the
// requested state.
func TestLocalTracker_ListIssues_ExcludesClosed(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "open", localIssue{frontmatter: localFrontmatter{
		Title: "Open", State: labels.Complete, Created: "2026-07-08T12:00:00Z",
	}})
	writeLocalIssue(t, dir, "closed", localIssue{frontmatter: localFrontmatter{
		Title: "Closed", State: labels.Complete, Created: "2026-07-09T12:00:00Z", Closed: true,
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.ListIssues(forge.Complete)
	if err != nil {
		t.Fatalf("ListIssues(Complete): %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "open" {
		t.Errorf("ListIssues(Complete) = %+v, want [open]", issues)
	}
}

// TestLocalTracker_SeamsOf_ReturnsOpenAndClosedMatchingParent verifies
// SeamsOf returns every issue (open or closed) whose parent frontmatter
// equals the requested parent, in canonical created-ascending order, and
// excludes issues with a different (or absent) parent — the query
// CODE_FORGE=local's auto-surface sweep (issue #1730) uses to test whether a
// broad ticket's seams are all landed.
func TestLocalTracker_SeamsOf_ReturnsOpenAndClosedMatchingParent(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "seam-2", localIssue{frontmatter: localFrontmatter{
		Title: "Seam 2", State: labels.Complete, Created: "2026-07-09T12:00:00Z", Parent: "broad-1", Closed: true,
	}})
	writeLocalIssue(t, dir, "seam-1", localIssue{frontmatter: localFrontmatter{
		Title: "Seam 1", State: labels.Dispatchable, Created: "2026-07-08T12:00:00Z", Parent: "broad-1",
	}})
	writeLocalIssue(t, dir, "other-parent", localIssue{frontmatter: localFrontmatter{
		Title: "Other", State: labels.Dispatchable, Created: "2026-07-07T12:00:00Z", Parent: "broad-2",
	}})
	writeLocalIssue(t, dir, "no-parent", localIssue{frontmatter: localFrontmatter{
		Title: "No parent", State: labels.Dispatchable, Created: "2026-07-06T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.SeamsOf("broad-1")
	if err != nil {
		t.Fatalf("SeamsOf: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "seam-1" || issues[1].Number != "seam-2" {
		t.Fatalf("SeamsOf(broad-1) = %+v, want [seam-1, seam-2]", issues)
	}
	if issues[1].State != forge.IssueClosed {
		t.Errorf("seam-2 State = %v, want IssueClosed", issues[1].State)
	}
}

func TestLocalTracker_TransitionState_RewritesFrontmatterInPlace(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.TransitionState("fix-thing", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// State isn't part of the launcher-facing Issue; re-list under the new
	// state to confirm the on-disk frontmatter actually moved.
	if iss.Title != "Fix thing" {
		t.Fatalf("Title changed unexpectedly: %q", iss.Title)
	}
	inProgress, err := lt.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues(InProgress): %v", err)
	}
	if len(inProgress) != 1 || inProgress[0].Number != "fix-thing" {
		t.Errorf("ListIssues(InProgress) = %+v, want [fix-thing]", inProgress)
	}
	dispatchable, err := lt.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(dispatchable) != 0 {
		t.Errorf("ListIssues(Dispatchable) = %+v, want none", dispatchable)
	}
}

// TestLocalTracker_CompleteVerdict_UnconfiguredErrorsWithoutWriting verifies
// that CompleteVerdict on a tracker constructed with no VerdictLabels (the
// work-kind construction path) errors instead of overwriting the frontmatter
// state field with an empty string — matching the github/jira adapters'
// guard against silently corrupting the state marker.
func TestLocalTracker_CompleteVerdict_UnconfiguredErrorsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.CompleteVerdict("fix-thing", forge.Recommend); err == nil {
		t.Fatal("want error for unconfigured VerdictLabels, got nil")
	}

	inProg, err := lt.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues(InProgress): %v", err)
	}
	if len(inProg) != 1 || inProg[0].Number != "fix-thing" {
		t.Errorf("frontmatter state changed despite unconfigured VerdictLabels: ListIssues(InProgress) = %+v", inProg)
	}
}

// TestLocalTracker_CompleteVerdict_RewritesFrontmatterToVerdictLabel verifies
// that CompleteVerdict rewrites a research-kind issue's frontmatter state
// field from InProgress to each of the three verdict terminals, mirroring
// TransitionState_RewritesFrontmatterInPlace's file-rewrite assertion.
func TestLocalTracker_CompleteVerdict_RewritesFrontmatterToVerdictLabel(t *testing.T) {
	labels := forge.ResearchDispatchLabels()
	verdictLabels := forge.ResearchVerdictLabels()

	cases := []struct {
		verdict   forge.Verdict
		wantState string
	}{
		{forge.Recommend, verdictLabels.Recommend},
		{forge.Reject, verdictLabels.Reject},
		{forge.Unclear, verdictLabels.Unclear},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		writeLocalIssue(t, dir, "research-me", localIssue{frontmatter: localFrontmatter{
			Title: "Research me", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		}})

		lt := NewLocalTracker(dir, labels, verdictLabels)
		if err := lt.CompleteVerdict("research-me", tc.verdict); err != nil {
			t.Fatalf("CompleteVerdict(%v): %v", tc.verdict, err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "research-me.md"))
		if err != nil {
			t.Fatalf("read issue file: %v", err)
		}
		li, err := parseLocalIssue(data)
		if err != nil {
			t.Fatalf("parseLocalIssue: %v", err)
		}
		if li.frontmatter.State != tc.wantState {
			t.Errorf("verdict %v: state = %q, want %q", tc.verdict, li.frontmatter.State, tc.wantState)
		}
	}
}

// TestLocalTracker_CompleteVerdict_ThenRetryResearchable verifies that after
// a verdict terminal lands, re-marking the issue researchable (the retry
// gesture, TransitionState(Untriaged, Dispatchable)) still works — the
// verdict state must not wedge the file against a fresh research pass.
func TestLocalTracker_CompleteVerdict_ThenRetryResearchable(t *testing.T) {
	labels := forge.ResearchDispatchLabels()
	verdictLabels := forge.ResearchVerdictLabels()
	dir := t.TempDir()
	writeLocalIssue(t, dir, "research-me", localIssue{frontmatter: localFrontmatter{
		Title: "Research me", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels, verdictLabels)
	if err := lt.CompleteVerdict("research-me", forge.Reject); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}

	if err := lt.TransitionState("research-me", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(Untriaged, Dispatchable): %v", err)
	}

	dispatchable, err := lt.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(dispatchable) != 1 || dispatchable[0].Number != "research-me" {
		t.Errorf("ListIssues(Dispatchable) = %+v, want [research-me]", dispatchable)
	}
}

// TestLocalTracker_ResearchDispatch_InProgressAndFailedUseResearchLabels
// verifies the research kind's InProgress and Failed transitions (plain
// TransitionState, not CompleteVerdict) rewrite the frontmatter state field
// to the research label family, distinct from every verdict terminal —
// Failed strictly means the Box crashed or produced no verdict (ADR 0022),
// never a concluded verdict.
func TestLocalTracker_ResearchDispatch_InProgressAndFailedUseResearchLabels(t *testing.T) {
	labels := forge.ResearchDispatchLabels()
	verdictLabels := forge.ResearchVerdictLabels()
	dir := t.TempDir()
	writeLocalIssue(t, dir, "research-me", localIssue{frontmatter: localFrontmatter{
		Title: "Research me", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels, verdictLabels)
	if err := lt.TransitionState("research-me", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState(Dispatchable, InProgress): %v", err)
	}
	inProg, err := lt.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues(InProgress): %v", err)
	}
	if len(inProg) != 1 || inProg[0].Number != "research-me" {
		t.Fatalf("ListIssues(InProgress) = %+v, want [research-me]", inProg)
	}

	if err := lt.TransitionState("research-me", forge.InProgress, forge.Failed); err != nil {
		t.Fatalf("TransitionState(InProgress, Failed): %v", err)
	}
	failed, err := lt.ListIssues(forge.Failed)
	if err != nil {
		t.Fatalf("ListIssues(Failed): %v", err)
	}
	if len(failed) != 1 || failed[0].Number != "research-me" {
		t.Fatalf("ListIssues(Failed) = %+v, want [research-me]", failed)
	}

	terminals := []string{labels.Failed, verdictLabels.Recommend, verdictLabels.Reject, verdictLabels.Unclear}
	seen := map[string]bool{}
	for _, l := range terminals {
		if seen[l] {
			t.Fatalf("terminal state %q collides with another terminal: %v", l, terminals)
		}
		seen[l] = true
	}
}

func TestLocalTracker_DepsOf_ParsesBlockedBySlugSection(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "depends-on-others", localIssue{
		frontmatter: localFrontmatter{Title: "Depends on others", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- init-database\n- setup-ci\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("depends-on-others")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{
		{ID: "init-database", Source: forge.DepSourceBody},
		{ID: "setup-ci", Source: forge.DepSourceBody},
	}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("DepsOf = %v, want %v", deps, want)
	}
}

func TestLocalTracker_DepsOf_StripsBackticksFromSlug(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "depends-on-others", localIssue{
		frontmatter: localFrontmatter{Title: "Depends on others", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- `init-database`\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("depends-on-others")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{
		{ID: "init-database", Source: forge.DepSourceBody},
	}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("DepsOf = %v, want %v", deps, want)
	}
}

// TestLocalTracker_DepsOf_SkipsSentinelBullet mirrors the GitHub parser's
// sentinel case (forge.TestParseBlockerRefs_SentinelNoneBulletIgnoresInlineRef
// in seams_test.go): a "- None" bullet under "## Blocked by" means zero
// blockers, not a literal slug named "None — can start immediately".
func TestLocalTracker_DepsOf_SkipsSentinelBullet(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "first-slice", localIssue{
		frontmatter: localFrontmatter{Title: "First slice", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- None — can start immediately\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("first-slice")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DepsOf = %v, want none", deps)
	}
}

// TestLocalTracker_DepsOf_SkipsBacktickQuotedSentinel locks in the local
// adapter's widened sentinel recognition: the sentinel check runs after
// backtick-stripping, so a backtick-quoted "`None`" bullet is skipped too,
// unlike ParseBlockerRefs which checks raw bullet content.
func TestLocalTracker_DepsOf_SkipsBacktickQuotedSentinel(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "backtick-sentinel", localIssue{
		frontmatter: localFrontmatter{Title: "Backtick sentinel", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- `None`\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("backtick-sentinel")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DepsOf = %v, want none", deps)
	}
}

// TestLocalTracker_DepsOf_SkipsSentinelBulletNA covers the "N/A" spelling
// of the sentinel (AC names it explicitly, alongside "None").
func TestLocalTracker_DepsOf_SkipsSentinelBulletNA(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "third-slice", localIssue{
		frontmatter: localFrontmatter{Title: "Third slice", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- N/A\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("third-slice")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DepsOf = %v, want none", deps)
	}
}

// TestLocalTracker_DepsOf_SentinelBulletDoesNotSuppressRealSlug confirms a
// sentinel bullet only cancels itself — a real slug bullet in the same
// section still surfaces as a dependency.
func TestLocalTracker_DepsOf_SentinelBulletDoesNotSuppressRealSlug(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "second-slice", localIssue{
		frontmatter: localFrontmatter{Title: "Second slice", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n- None\n- 01-calc-add\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("second-slice")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{
		{ID: "01-calc-add", Source: forge.DepSourceBody},
	}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("DepsOf = %v, want %v", deps, want)
	}
}

func TestLocalTracker_DepsOf_NoBlockedBySection(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "standalone", localIssue{
		frontmatter: localFrontmatter{Title: "Standalone", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body:        "## What to build\n\nDo the thing.\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("standalone")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DepsOf = %v, want none", deps)
	}
}

// TestLocalTracker_DepsOf_EmptyBlockedBySection confirms an explicit
// "## Blocked by" section with no bullets parses to zero blockers, same
// as omitting the section entirely (AC: "behavior stays identical for
// issues that already omit the section or leave it empty").
func TestLocalTracker_DepsOf_EmptyBlockedBySection(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "empty-section", localIssue{
		frontmatter: localFrontmatter{Title: "Empty section", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body: "## What to build\n\nDo the thing.\n\n" +
			"## Blocked by\n\n## Touches\n\nsrc/\n",
	})

	lt := NewLocalTracker(dir, labels)
	deps, err := lt.DepsOf("empty-section")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("DepsOf = %v, want none", deps)
	}
}

// TestLocalTracker_Issue_LabelsIncludeDispatchState verifies that the
// dispatch-state marker is included in the returned Issue's Labels, matching
// the GitHub adapter's behavior (a GitHub issue's Labels always include
// whichever label represents its current dispatch state). main.go's
// cross-backend blocker logic (Readiness.Status) checks
// containsLabel(fi.Labels, c.failedLabel) generically across adapters, so the
// state marker must appear in Labels even though the frontmatter keeps state
// and labels as separate fields on disk.
func TestLocalTracker_Issue_LabelsIncludeDispatchState(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "broken", localIssue{frontmatter: localFrontmatter{
		Title: "Broken", State: labels.Failed, Labels: []string{"bug"}, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	iss, err := lt.Issue("broken")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	want := []string{"bug", labels.Failed}
	if !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("Labels = %v, want %v", iss.Labels, want)
	}
}

// TestLocalTracker_Issue_ReportsClosedState verifies Issue() reports
// forge.IssueClosed for a closed: true issue and forge.IssueOpen otherwise
// (absent or false), matching ADR 0029's local-only open/closed axis.
func TestLocalTracker_Issue_ReportsClosedState(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "done", localIssue{frontmatter: localFrontmatter{
		Title: "Done", State: labels.Complete, Created: "2026-07-09T12:00:00Z", Closed: true,
	}})
	writeLocalIssue(t, dir, "open", localIssue{frontmatter: localFrontmatter{
		Title: "Open", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	done, err := lt.Issue("done")
	if err != nil {
		t.Fatalf("Issue(done): %v", err)
	}
	if done.State != forge.IssueClosed {
		t.Errorf("done.State = %v, want IssueClosed", done.State)
	}
	open, err := lt.Issue("open")
	if err != nil {
		t.Fatalf("Issue(open): %v", err)
	}
	if open.State != forge.IssueOpen {
		t.Errorf("open.State = %v, want IssueOpen", open.State)
	}
}

// TestLocalTracker_Issue_ReportsLandingRef verifies Issue() surfaces the
// frontmatter landing: ref on forge.Issue.Landing — reconcile's read side of
// RecordLanding's write (ADR 0029).
func TestLocalTracker_Issue_ReportsLandingRef(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		Landing: "https://github.com/o/r/pull/1",
	}})

	lt := NewLocalTracker(dir, labels)
	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing = %q, want %q", iss.Landing, "https://github.com/o/r/pull/1")
	}
}

// TestLocalTracker_Issue_ReportsAbandonedFlag verifies Issue() surfaces the
// frontmatter abandoned: flag on forge.Issue.Abandoned — reconcile's read
// side of FlagAbandoned's write (ADR 0029).
func TestLocalTracker_Issue_ReportsAbandonedFlag(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		Landing: "https://github.com/o/r/pull/1", Abandoned: true,
	}})

	lt := NewLocalTracker(dir, labels)
	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !iss.Abandoned {
		t.Errorf("Abandoned = %v, want true", iss.Abandoned)
	}
}

// TestLocalTracker_ImplementsLandingRecorder asserts *LocalTracker satisfies
// the optional forge.LandingRecorder surface (ADR 0029) — only the local
// adapter records a landing ref; github/jira don't implement it.
func TestLocalTracker_ImplementsLandingRecorder(t *testing.T) {
	var _ forge.LandingRecorder = NewLocalTracker(t.TempDir(), testLabels)
}

// TestLocalTracker_RecordLanding_WritesLandingField verifies RecordLanding
// persists the given ref as the issue's landing: frontmatter field.
func TestLocalTracker_RecordLanding_WritesLandingField(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.RecordLanding("fix-thing", "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("RecordLanding: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-thing.md"))
	if err != nil {
		t.Fatalf("read issue file: %v", err)
	}
	li, err := parseLocalIssue(data)
	if err != nil {
		t.Fatalf("parseLocalIssue: %v", err)
	}
	if li.frontmatter.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing = %q, want %q", li.frontmatter.Landing, "https://github.com/o/r/pull/1")
	}
}

// TestLocalTracker_ImplementsIssueCloser asserts *LocalTracker satisfies the
// optional forge.IssueCloser surface (ADR 0029) — only the local adapter has
// a native closed: axis for reconcile to flip.
func TestLocalTracker_ImplementsIssueCloser(t *testing.T) {
	var _ forge.IssueCloser = NewLocalTracker(t.TempDir(), testLabels)
}

// TestLocalTracker_CloseIssue_SetsClosedTrue verifies CloseIssue flips the
// closed: frontmatter field without touching state/labels/landing.
func TestLocalTracker_CloseIssue_SetsClosedTrue(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		Landing: "https://github.com/o/r/pull/1",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.CloseIssue("fix-thing"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
	if iss.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing = %q, want unchanged %q", iss.Landing, "https://github.com/o/r/pull/1")
	}
}

// TestLocalTracker_ImplementsAbandonedFlagger asserts *LocalTracker satisfies
// the optional forge.AbandonedFlagger surface (ADR 0029) — only the local
// adapter has a native abandoned: axis for reconcile to flip.
func TestLocalTracker_ImplementsAbandonedFlagger(t *testing.T) {
	var _ forge.AbandonedFlagger = NewLocalTracker(t.TempDir(), testLabels)
}

// TestLocalTracker_FlagAbandoned_SetsAbandonedTrue verifies FlagAbandoned
// flips the abandoned: frontmatter field without touching state/landing.
func TestLocalTracker_FlagAbandoned_SetsAbandonedTrue(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		Landing: "https://github.com/o/r/pull/1",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.FlagAbandoned("fix-thing"); err != nil {
		t.Fatalf("FlagAbandoned: %v", err)
	}

	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !iss.Abandoned {
		t.Errorf("Abandoned = %v, want true", iss.Abandoned)
	}
	if iss.State != forge.IssueOpen {
		t.Errorf("State = %v, want unchanged IssueOpen", iss.State)
	}
	if iss.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing = %q, want unchanged %q", iss.Landing, "https://github.com/o/r/pull/1")
	}
}

func TestLocalTracker_Comment_AppendsToBody(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{
		frontmatter: localFrontmatter{Title: "Fix thing", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body:        "## What to build\n\nDo the thing.\n",
	})

	lt := NewLocalTracker(dir, labels)
	if err := lt.Comment("fix-thing", "started work"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(iss.Body, "## Comments") || !strings.Contains(iss.Body, "started work") {
		t.Errorf("Body = %q, want it to contain a Comments section with %q", iss.Body, "started work")
	}
}

func TestLocalTracker_Comment_MultilineUsageReportRendersAsBlock(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "fix-thing", localIssue{
		frontmatter: localFrontmatter{Title: "Fix thing", State: testLabels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body:        "## What to build\n\nDo the thing.\n",
	})

	lt := NewLocalTracker(dir, testLabels)
	report := "## Run usage\n\n" +
		"| Field | Value |\n| --- | --- |\n| Cost | $1 |\n\n" +
		"| Metric | Count |\n| --- | --- |\n| Turns | 3 |\n"
	if err := lt.Comment("fix-thing", report); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(iss.Body, "\n## Run usage\n") {
		t.Errorf("Body = %q, want \"## Run usage\" as a real heading on its own line", iss.Body)
	}
	if strings.Count(iss.Body, "\n| --- | --- |\n") != 2 {
		t.Errorf("Body = %q, want both table delimiter rows preserved on their own lines", iss.Body)
	}
}

func TestLocalTracker_Probe_CreatesDirAndReturnsPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "issues")
	lt := NewLocalTracker(dir, testLabels)
	resolved, err := lt.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if resolved == "" {
		t.Error("Probe returned empty path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Probe should have created %s: %v", dir, err)
	}
}

func TestLocalTracker_ListLabels_ReturnsDispatchLabels(t *testing.T) {
	labels := testLabels
	lt := NewLocalTracker(t.TempDir(), labels)
	got, err := lt.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	want := labels.AllLabels()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListLabels = %v, want %v", got, want)
	}
}

func TestLocalTracker_CreateLabel_NoOp(t *testing.T) {
	lt := NewLocalTracker(t.TempDir(), testLabels)
	if err := lt.CreateLabel("foo", "desc", "ededed"); err != nil {
		t.Errorf("CreateLabel: %v", err)
	}
}
