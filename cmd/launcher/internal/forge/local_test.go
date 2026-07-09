package forge

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeLocalIssue writes an issue file named slug+".md" under dir, using
// DefaultDispatchLabels' native marker for state.
func writeLocalIssue(t *testing.T, dir, slug string, li localIssue) {
	t.Helper()
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(li.render()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLocalTracker_ImplementsIssueTracker(t *testing.T) {
	var _ IssueTracker = NewLocalTracker(t.TempDir(), DefaultDispatchLabels())
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
	labels := DefaultDispatchLabels()

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
	issues, err := lt.ListIssues(Dispatchable)
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

func TestLocalTracker_TransitionState_RewritesFrontmatterInPlace(t *testing.T) {
	dir := t.TempDir()
	labels := DefaultDispatchLabels()
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.Dispatchable, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.TransitionState("fix-thing", Dispatchable, InProgress); err != nil {
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
	inProgress, err := lt.ListIssues(InProgress)
	if err != nil {
		t.Fatalf("ListIssues(InProgress): %v", err)
	}
	if len(inProgress) != 1 || inProgress[0].Number != "fix-thing" {
		t.Errorf("ListIssues(InProgress) = %+v, want [fix-thing]", inProgress)
	}
	dispatchable, err := lt.ListIssues(Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues(Dispatchable): %v", err)
	}
	if len(dispatchable) != 0 {
		t.Errorf("ListIssues(Dispatchable) = %+v, want none", dispatchable)
	}
}

func TestLocalTracker_DepsOf_ParsesBlockedBySlugSection(t *testing.T) {
	dir := t.TempDir()
	labels := DefaultDispatchLabels()
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
	want := []string{"init-database", "setup-ci"}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("DepsOf = %v, want %v", deps, want)
	}
}

func TestLocalTracker_DepsOf_NoBlockedBySection(t *testing.T) {
	dir := t.TempDir()
	labels := DefaultDispatchLabels()
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
	labels := DefaultDispatchLabels()
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
	labels := DefaultDispatchLabels()
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
	lt := NewLocalTracker(dir, DefaultDispatchLabels())
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
	labels := DefaultDispatchLabels()
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
	lt := NewLocalTracker(t.TempDir(), DefaultDispatchLabels())
	if err := lt.CreateLabel("foo", "desc", "ededed"); err != nil {
		t.Errorf("CreateLabel: %v", err)
	}
}
