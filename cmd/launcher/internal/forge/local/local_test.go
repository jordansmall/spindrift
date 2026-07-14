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

// TestLocalTracker_Issue_LabelsIncludeDispatchState verifies that the
// dispatch-state marker is included in the returned Issue's Labels, matching
// the GitHub adapter's behavior (a GitHub issue's Labels always include
// whichever label represents its current dispatch state). main.go's
// cross-backend blocker logic (hasFailedInBatchBlocker) checks
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
