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
// nix/checks/dispatch-labels.nix (issue #460).
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

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

func TestLocalTracker_ImplementsLabeledTracker(t *testing.T) {
	var _ forge.LabeledTracker = NewLocalTracker(t.TempDir(), testLabels)
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

// landing: holds the immutable landing ref RecordLanding writes (ADR 0029).
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

// FlagAbandoned sets abandoned: when a landing PR closes without merging
// (ADR 0029).
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

// Each case pins an escaping rule scalarNeedsQuoting, renderScalar and
// unquote must agree on, carried through render() then parseLocalIssue as
// the Title field.
func TestLocalIssue_RenderParseRoundTrip_ScalarEscaping(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{name: "tab", title: "\tleading tab"},
		{name: "carriage return", title: "a\rb"},
		{name: "leading double-quote", title: `"leading`},
		{name: "embedded double-quote", title: `a"b`},
		{name: "embedded colon-space", title: "feat: implementing a thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			li := localIssue{
				frontmatter: localFrontmatter{
					Title:   tc.title,
					State:   "ready-for-agent",
					Labels:  []string{"bug"},
					Created: "2026-07-09T12:00:00Z",
				},
				body: "## What to build\n\nDo the thing.\n",
			}
			got, err := parseLocalIssue([]byte(li.render()))
			if err != nil {
				t.Fatalf("parseLocalIssue(render()): %v", err)
			}
			if got.frontmatter.Title != tc.title {
				t.Errorf("Title round trip = %q, want %q", got.frontmatter.Title, tc.title)
			}
			if !reflect.DeepEqual(got, li) {
				t.Errorf("round trip = %+v, want %+v", got, li)
			}
			// A colon-space is a YAML mapping separator, so a title holding
			// one must render quoted or the frontmatter is invalid YAML,
			// even though this package's own tolerant first-colon-split
			// parser round-trips it either way.
			if strings.Contains(tc.title, ": ") {
				for _, line := range strings.Split(li.render(), "\n") {
					if rest, ok := strings.CutPrefix(line, "title: "); ok {
						if !strings.HasPrefix(rest, `"`) {
							t.Errorf("title line %q not quoted; unquoted colon-space produces invalid YAML", line)
						}
						break
					}
				}
			}
		})
	}
}

// scalarNeedsQuoting's colon check, widened in the frontmatter-colon-escaping
// fix, applies to every scalar field renderScalar writes, not just Title.
func TestLocalIssue_Render_QuotesColonBearingFields(t *testing.T) {
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:   "Fix the thing",
			State:   "ready-for-agent",
			Labels:  []string{"bug"},
			Created: "2026-07-09T12:00:00Z",
			Parent:  "parent:42",
			Landing: "https://github.com/o/r/pull/7",
		},
		body: "## What to build\n\nDo the thing.\n",
	}

	rendered := li.render()

	for _, want := range []string{
		`created: "2026-07-09T12:00:00Z"` + "\n",
		`parent: "parent:42"` + "\n",
		`landing: "https://github.com/o/r/pull/7"` + "\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render() missing expected line %q; got:\n%s", want, rendered)
		}
	}

	got, err := parseLocalIssue([]byte(rendered))
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

// ListOpenIssues ignores the frontmatter state marker, unlike ListIssues,
// which filters to a single state.
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

// Dropping a closed: true issue from the backlog matches forge.Fake's own
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

// The closed issue here still carries the requested state marker, so closed:
// has to win on its own (ADR 0029).
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

// The auto-surface sweep discovers every distinct resolved parent across a
// mixed batch from AllIssues, so it must ignore parent, state, and dispatch
// marker alike (ADR 0033, issue #1734).
func TestLocalTracker_AllIssues_ReturnsEveryIssueOpenAndClosed(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels

	writeLocalIssue(t, dir, "seam-2", localIssue{frontmatter: localFrontmatter{
		Title: "Seam 2", State: labels.Complete, Created: "2026-07-09T12:00:00Z", Parent: "broad-1", Closed: true,
	}})
	writeLocalIssue(t, dir, "seam-1", localIssue{frontmatter: localFrontmatter{
		Title: "Seam 1", State: labels.Dispatchable, Created: "2026-07-08T12:00:00Z", Parent: "broad-1",
	}})
	writeLocalIssue(t, dir, "no-parent", localIssue{frontmatter: localFrontmatter{
		Title: "No parent", State: labels.Dispatchable, Created: "2026-07-06T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	issues, err := lt.AllIssues()
	if err != nil {
		t.Fatalf("AllIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("AllIssues returned %d issues, want 3: %+v", len(issues), issues)
	}
	if issues[0].Number != "no-parent" || issues[1].Number != "seam-1" || issues[2].Number != "seam-2" {
		t.Fatalf("AllIssues = %+v, want [no-parent, seam-1, seam-2] (created-ascending)", issues)
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
	// State is not part of the launcher-facing Issue, so the re-list below is
	// the only way to confirm the on-disk frontmatter actually moved.
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

// A tracker built the work-kind way has no VerdictLabels, so CompleteVerdict
// must error rather than write an empty state marker. The github and jira
// adapters guard the same way.
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

func TestLocalTracker_CompleteVerdict_RewritesFrontmatterToVerdictLabel(t *testing.T) {
	labels := forge.ResearchDispatchLabels()
	verdictLabels := forge.ResearchVerdictLabels()

	cases := []struct {
		verdict   forge.Verdict
		wantState string
	}{
		{forge.Recommend, verdictLabels.Label(forge.Recommend)},
		{forge.Reject, verdictLabels.Label(forge.Reject)},
		{forge.Unclear, verdictLabels.Label(forge.Unclear)},
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

// A verdict terminal must not block the retry gesture,
// TransitionState(Untriaged, Dispatchable), from starting a fresh research
// pass on the same file.
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

// Research InProgress and Failed go through plain TransitionState, not
// CompleteVerdict, and Failed must stay distinct from every verdict terminal:
// it means the Box crashed or produced no verdict (ADR 0022).
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

	terminals := []string{labels.Failed, verdictLabels.Label(forge.Recommend), verdictLabels.Label(forge.Reject), verdictLabels.Label(forge.Unclear)}
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

// A "- None" bullet means zero blockers, not a literal slug. This mirrors the
// GitHub parser's sentinel case,
// forge.TestParseBlockerRefs_SentinelNoneBulletIgnoresInlineRef in
// seams_test.go.
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

// This adapter runs the sentinel check after backtick-stripping, so it skips
// a backtick-quoted sentinel that ParseBlockerRefs, which checks raw bullet
// content, would keep.
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

// The AC names "N/A" as a sentinel spelling alongside "None".
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

// A sentinel bullet cancels only itself, not the rest of the section.
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

// The AC requires behavior identical for issues that omit the section and
// issues that leave it empty.
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

// The frontmatter keeps state and labels as separate fields on disk, but
// main.go's Readiness.Status checks containsLabel(fi.Labels, c.failedLabel)
// generically across adapters, so Issue has to fold the state marker into
// Labels the way the GitHub adapter does.
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

// closed: is ADR 0029's local-only open/closed axis; absent counts as open.
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

// This is reconcile's read side of RecordLanding's write (ADR 0029).
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

// parent: is the per-issue key newCodeForge resolves the Integration branch
// from (ADR 0033, issue #1734).
func TestLocalTracker_Issue_ReportsParent(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
		Parent: "Calc Engine",
	}})

	lt := NewLocalTracker(dir, labels)
	iss, err := lt.Issue("fix-thing")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Parent != "Calc Engine" {
		t.Errorf("Parent = %q, want %q", iss.Parent, "Calc Engine")
	}
}

// This is reconcile's read side of FlagAbandoned's write (ADR 0029).
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

// forge.LandingRecorder is optional (ADR 0029): only the local adapter
// records a landing ref, and github and jira do not implement it.
func TestLocalTracker_ImplementsLandingRecorder(t *testing.T) {
	var _ forge.LandingRecorder = NewLocalTracker(t.TempDir(), testLabels)
}

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

// forge.LandingPassRecorder is optional (issue #2983): only the local adapter
// has a per-issue file to annotate with the pass that produced the outcome.
func TestLocalTracker_ImplementsLandingPassRecorder(t *testing.T) {
	var _ forge.LandingPassRecorder = NewLocalTracker(t.TempDir(), testLabels)
}

// Issue #2983 added the landingpass: and landingpasskind: fields.
func TestLocalTracker_RecordLandingPass_WritesLandingPassFields(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.RecordLandingPass("fix-thing", 2, "fix"); err != nil {
		t.Fatalf("RecordLandingPass: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-thing.md"))
	if err != nil {
		t.Fatalf("read issue file: %v", err)
	}
	li, err := parseLocalIssue(data)
	if err != nil {
		t.Fatalf("parseLocalIssue: %v", err)
	}
	if li.frontmatter.LandingPass != 2 {
		t.Errorf("LandingPass = %d, want 2", li.frontmatter.LandingPass)
	}
	if li.frontmatter.LandingPassKind != "fix" {
		t.Errorf("LandingPassKind = %q, want %q", li.frontmatter.LandingPassKind, "fix")
	}
}

// An older manifest.json can leave Kind empty, and render (issue #2983) must
// then drop the landingpasskind: line rather than write a dangling key.
func TestLocalTracker_RecordLandingPass_EmptyKindOmitsLandingPassKindLine(t *testing.T) {
	dir := t.TempDir()
	labels := testLabels
	writeLocalIssue(t, dir, "fix-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix thing", State: labels.InProgress, Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, labels)
	if err := lt.RecordLandingPass("fix-thing", 2, ""); err != nil {
		t.Fatalf("RecordLandingPass: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-thing.md"))
	if err != nil {
		t.Fatalf("read issue file: %v", err)
	}
	if !strings.Contains(string(data), "landingpass: 2\n") {
		t.Errorf("expected landingpass: 2 line, got:\n%s", data)
	}
	if strings.Contains(string(data), "landingpasskind") {
		t.Errorf("expected no landingpasskind line for empty Kind, got:\n%s", data)
	}

	li, err := parseLocalIssue(data)
	if err != nil {
		t.Fatalf("parseLocalIssue: %v", err)
	}
	if li.frontmatter.LandingPass != 2 {
		t.Errorf("LandingPass = %d, want 2", li.frontmatter.LandingPass)
	}
	if li.frontmatter.LandingPassKind != "" {
		t.Errorf("LandingPassKind = %q, want empty", li.frontmatter.LandingPassKind)
	}
}

// forge.IssueCloser is optional (ADR 0029): only the local adapter has a
// native closed: axis for reconcile to flip.
func TestLocalTracker_ImplementsIssueCloser(t *testing.T) {
	var _ forge.IssueCloser = NewLocalTracker(t.TempDir(), testLabels)
}

// Issue #1744: the local adapter's only blocker concept is one-directional
// body-text parsing, with no relationship to query in reverse short of
// scanning every issue file.
func TestLocalTracker_DoesNotImplementBlockersLister(t *testing.T) {
	var it forge.IssueTracker = NewLocalTracker(t.TempDir(), testLabels)
	if _, ok := it.(forge.BlockersLister); ok {
		t.Error("LocalTracker satisfies forge.BlockersLister, want it absent")
	}
}

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

// forge.AbandonedFlagger is optional (ADR 0029): only the local adapter has a
// native abandoned: axis for reconcile to flip.
func TestLocalTracker_ImplementsAbandonedFlagger(t *testing.T) {
	var _ forge.AbandonedFlagger = NewLocalTracker(t.TempDir(), testLabels)
}

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

func TestLocalTracker_PostIssue_WritesFileWithFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	lt := NewLocalTracker(dir, testLabels)

	ref, err := lt.PostIssue("Fix the Thing", "## What to build\n\nDo the thing.\n", []string{"bug", "agent-review-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if ref != "local:fix-the-thing" {
		t.Errorf("ref = %q, want %q", ref, "local:fix-the-thing")
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-the-thing.md"))
	if err != nil {
		t.Fatalf("read issue file: %v", err)
	}
	li, err := parseLocalIssue(data)
	if err != nil {
		t.Fatalf("parseLocalIssue: %v", err)
	}
	if li.frontmatter.Title != "Fix the Thing" {
		t.Errorf("Title = %q, want %q", li.frontmatter.Title, "Fix the Thing")
	}
	if !reflect.DeepEqual(li.frontmatter.Labels, []string{"bug", "agent-review-finding"}) {
		t.Errorf("Labels = %v, want %v", li.frontmatter.Labels, []string{"bug", "agent-review-finding"})
	}
	if li.frontmatter.Created == "" {
		t.Error("Created is empty, want a timestamp")
	}
	if li.body != "## What to build\n\nDo the thing.\n" {
		t.Errorf("body = %q, want %q", li.body, "## What to build\n\nDo the thing.\n")
	}
}

// forge.HostPostedIssueFiler is optional (issue #2018).
func TestLocalTracker_ImplementsHostPostedIssueFiler(t *testing.T) {
	var _ forge.HostPostedIssueFiler = NewLocalTracker(t.TempDir(), testLabels)
}

// PostIssue leaves State empty (untriaged), so toIssue must not append that
// empty marker as a stray "" element in Labels.
func TestLocalTracker_PostIssue_ReadBack_NoEmptyLabel(t *testing.T) {
	dir := t.TempDir()
	lt := NewLocalTracker(dir, testLabels)

	ref, err := lt.PostIssue("Fix the Thing", "body", []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}

	iss, err := lt.Issue(strings.TrimPrefix(ref, "local:"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	want := []string{"agent-review-finding"}
	if !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("Labels = %v, want %v", iss.Labels, want)
	}
}

// PostIssue must never overwrite an existing issue file, so a taken slug
// retries with a "-2" suffix, then "-3", and so on.
func TestLocalTracker_PostIssue_SlugCollision_AppendsSuffix(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "fix-the-thing", localIssue{frontmatter: localFrontmatter{
		Title: "Fix the Thing (original)", Created: "2026-07-09T12:00:00Z",
	}})

	lt := NewLocalTracker(dir, testLabels)
	ref, err := lt.PostIssue("Fix the Thing", "new body", nil)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if ref != "local:fix-the-thing-2" {
		t.Errorf("ref = %q, want %q", ref, "local:fix-the-thing-2")
	}

	orig, err := lt.Issue("fix-the-thing")
	if err != nil {
		t.Fatalf("Issue(fix-the-thing): %v", err)
	}
	if orig.Title != "Fix the Thing (original)" {
		t.Errorf("original Title = %q, want unchanged %q", orig.Title, "Fix the Thing (original)")
	}

	newIss, err := lt.Issue("fix-the-thing-2")
	if err != nil {
		t.Fatalf("Issue(fix-the-thing-2): %v", err)
	}
	if newIss.Body != "new body" {
		t.Errorf("new Body = %q, want %q", newIss.Body, "new body")
	}
}

// The fixture forces os.MkdirAll to fail with ENOTDIR by planting a regular
// file where a path component belongs.
func TestLocalTracker_PostIssue_MkdirAllFails_ReturnsError(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", parent, err)
	}
	dir := filepath.Join(parent, "issues")

	lt := NewLocalTracker(dir, testLabels)
	if _, err := lt.PostIssue("Fix the Thing", "body", nil); err == nil {
		t.Fatal("PostIssue: got nil error, want non-nil (MkdirAll should fail: parent is a regular file)")
	}
}

// 0o500 is the mode that isolates the WriteFile failure: the owner can still
// traverse and stat the dir, so uniqueSlug's stat succeeds and only the
// create fails. Root bypasses permission checks, hence the skip.
func TestLocalTracker_PostIssue_WriteFileFails_ReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed, so this test can't force a write failure")
	}

	dir := filepath.Join(t.TempDir(), "issues")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	lt := NewLocalTracker(dir, testLabels)
	if _, err := lt.PostIssue("Fix the Thing", "body", nil); err == nil {
		t.Fatal("PostIssue: got nil error, want non-nil (WriteFile should fail: dir is read-only)")
	}
}

// 0o000 makes the dir unsearchable, so uniqueSlug's stat fails with a
// permission error instead of the IsNotExist that means "no collision".
// PostIssue must propagate it rather than loop past it. Root bypasses
// permission checks, hence the skip.
func TestLocalTracker_PostIssue_UniqueSlugStatFails_ReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed, so this test can't force a stat failure")
	}

	dir := filepath.Join(t.TempDir(), "issues")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	lt := NewLocalTracker(dir, testLabels)
	_, err := lt.PostIssue("Fix the Thing", "body", nil)
	if err == nil {
		t.Fatal("PostIssue: got nil error, want non-nil (uniqueSlug's stat should fail: dir is unsearchable)")
	}
	if !strings.Contains(err.Error(), "stat local issue") {
		t.Errorf("err = %q, want it to come from uniqueSlug's stat branch (contain %q)", err.Error(), "stat local issue")
	}
}

// PostIssue's title and body come verbatim from an attacker-influenceable
// SPINDRIFT_ISSUE_INTENT payload, so a title holding frontmatter-shaped lines
// must round-trip as one literal title, never be re-parsed as frontmatter.
func TestLocalTracker_PostIssue_NewlineTitle_NoFrontmatterInjection(t *testing.T) {
	dir := t.TempDir()
	lt := NewLocalTracker(dir, testLabels)

	title := "x\n---\nclosed: true\nlabels: [pwned]"
	ref, err := lt.PostIssue(title, "real body", []string{"agent-review-finding"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}

	iss, err := lt.Issue(strings.TrimPrefix(ref, "local:"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Title != title {
		t.Errorf("Title = %q, want %q", iss.Title, title)
	}
	if iss.State == forge.IssueClosed {
		t.Errorf("State = IssueClosed, want open (title injection must not close the issue)")
	}
	foundReview, foundPwned := false, false
	for _, l := range iss.Labels {
		if l == "agent-review-finding" {
			foundReview = true
		}
		if l == "pwned" {
			foundPwned = true
		}
	}
	if !foundReview {
		t.Errorf("Labels = %v, want it to contain %q", iss.Labels, "agent-review-finding")
	}
	if foundPwned {
		t.Errorf("Labels = %v, want it NOT to contain injected %q", iss.Labels, "pwned")
	}
}

// PostIssue's labels argument is caller-supplied since issue #2018, so a
// label holding a comma, bracket, or newline must round-trip whole rather
// than fragment the frontmatter flow-list.
func TestLocalTracker_PostIssue_LabelInjection_RoundTripsExactLabels(t *testing.T) {
	dir := t.TempDir()
	lt := NewLocalTracker(dir, testLabels)

	labels := []string{"a,b", "x[y]", "line1\nline2: pwned"}
	ref, err := lt.PostIssue("Fix the Thing", "body", labels)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}

	iss, err := lt.Issue(strings.TrimPrefix(ref, "local:"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !reflect.DeepEqual(iss.Labels, labels) {
		t.Errorf("Labels = %v, want %v (exact round-trip, not fragmented)", iss.Labels, labels)
	}
}

// A title with no [a-z0-9] characters must fall back to a usable slug rather
// than produce a bare ".md" file.
func TestLocalTracker_PostIssue_PunctuationTitle_FallsBackToIssueSlug(t *testing.T) {
	dir := t.TempDir()
	lt := NewLocalTracker(dir, testLabels)

	ref, err := lt.PostIssue("!!! ???", "body", nil)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if ref != "local:issue" {
		t.Errorf("ref = %q, want %q", ref, "local:issue")
	}
	iss, err := lt.Issue("issue")
	if err != nil {
		t.Fatalf("Issue(issue): %v", err)
	}
	if iss.Title != "!!! ???" {
		t.Errorf("Title = %q, want %q", iss.Title, "!!! ???")
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

// slugPath must reject more than ids that escape the tracker dir: also ids
// that land back inside it under a different filename ("a/../b" rewriting to
// "b.md"), and the empty, "." and ".." ids that would collide with a literal
// ".md", "..md" or "...md" file. The "../issues/x" case runs against a dir
// named "issues" so the traversal re-enters without ever escaping.
var pathTraversalCases = []struct {
	name string
	id   string
}{
	{"parent traversal", "../../x"},
	{"sibling traversal", "../sibling"},
	{"nested traversal", "sub/../../x"},
	{"absolute path multi-segment", "/etc/passwd"},
	{"absolute path single segment", "/passwd"},
	{"absolute path bare", "/abs"},
	{"dotdot segment rewrite", "a/../b"},
	{"leading dot segment", "./x"},
	{"escape then reenter same dir name", "../issues/x"},
	{"empty id", ""},
	{"dot id", "."},
	{"dotdot id", ".."},
}

// The issues dir is nested inside a temp root so a test can exercise ids that
// resolve above it without reaching the wider filesystem.
func issuesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return dir
}

// Planting a real file where id would resolve without the containment guard
// makes a test fail on a leak instead of passing on a missing file.
func plantEscapeFile(t *testing.T, dir, id string) string {
	t.Helper()
	escapePath := filepath.Clean(filepath.Join(dir, id+".md"))
	if err := os.MkdirAll(filepath.Dir(escapePath), 0o755); err != nil {
		t.Fatalf("MkdirAll escape dir: %v", err)
	}
	li := localIssue{frontmatter: localFrontmatter{Title: "leaked", State: testLabels.Dispatchable, Created: "2026-07-09T12:00:00Z"}}
	if err := os.WriteFile(escapePath, []byte(li.render()), 0o644); err != nil {
		t.Fatalf("write escape file: %v", err)
	}
	return escapePath
}

func TestLocalTracker_Issue_RejectsPathTraversal(t *testing.T) {
	for _, c := range pathTraversalCases {
		t.Run(c.name, func(t *testing.T) {
			dir := issuesDir(t)
			plantEscapeFile(t, dir, c.id)

			lt := NewLocalTracker(dir, testLabels)
			iss, err := lt.Issue(c.id)
			if err == nil {
				t.Fatalf("Issue(%q) = %+v, want error", c.id, iss)
			}
		})
	}
}

// The fixture uses a real b.md, not a decoy: a guard that only checked that
// the resolved path stays under dir, without also checking the filename
// matches the id, would pass "a/../b" straight through to issue "b".
func TestLocalTracker_Issue_RejectsRewriteToExistingSibling(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "Real issue b", State: testLabels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
	})

	lt := NewLocalTracker(dir, testLabels)
	iss, err := lt.Issue("a/../b")
	if err == nil {
		t.Fatalf(`Issue("a/../b") = %+v, want error`, iss)
	}
}

// Comment is the write-side reachability path into the same guard.
func TestLocalTracker_Comment_RejectsPathTraversal(t *testing.T) {
	for _, c := range pathTraversalCases {
		t.Run(c.name, func(t *testing.T) {
			dir := issuesDir(t)
			escapePath := plantEscapeFile(t, dir, c.id)
			original, err := os.ReadFile(escapePath)
			if err != nil {
				t.Fatalf("read planted file: %v", err)
			}

			lt := NewLocalTracker(dir, testLabels)
			if err := lt.Comment(c.id, "hi"); err == nil {
				t.Fatalf("Comment(%q) = nil error, want error", c.id)
			}
			got, err := os.ReadFile(escapePath)
			if err != nil {
				t.Fatalf("read planted file back: %v", err)
			}
			if string(got) != string(original) {
				t.Fatalf("Comment(%q) modified %s outside the tracker dir: got %q, want unchanged %q", c.id, escapePath, got, original)
			}
		})
	}
}

// The containment guard must not reject the ordinary slugs slugify generates.
func TestLocalTracker_PostIssue_ThenIssue_OrdinarySlugRoundTrips(t *testing.T) {
	lt := NewLocalTracker(t.TempDir(), testLabels)
	ref, err := lt.PostIssue("Fix the Thing", "body", nil)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	slug := strings.TrimPrefix(ref, "local:")
	iss, err := lt.Issue(slug)
	if err != nil {
		t.Fatalf("Issue(%q): %v", slug, err)
	}
	if iss.Title != "Fix the Thing" {
		t.Errorf("Title = %q, want %q", iss.Title, "Fix the Thing")
	}
}

// A blocker slug parsed out of an issue body is untrusted input reaching
// slugPath by a second route. DepsOf still returns the slug verbatim, because
// the guard lives in resolution, not parsing.
func TestLocalTracker_DepsOf_ThenIssue_RejectsPathTraversalBlocker(t *testing.T) {
	dir := issuesDir(t)

	const traversal = "../../x"
	plantEscapeFile(t, dir, traversal)

	writeLocalIssue(t, dir, "depends-on-traversal", localIssue{
		frontmatter: localFrontmatter{Title: "Depends on traversal", State: testLabels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
		body:        "## Blocked by\n\n- " + traversal + "\n",
	})

	lt := NewLocalTracker(dir, testLabels)
	deps, err := lt.DepsOf("depends-on-traversal")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{{ID: traversal, Source: forge.DepSourceBody}}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("DepsOf = %v, want %v", deps, want)
	}

	iss, err := lt.Issue(deps[0].ID)
	if err == nil {
		t.Fatalf("Issue(%q) = %+v, want error", deps[0].ID, iss)
	}
}

// A stray file named nothing but dots plus ".md" is an id slugPath rejects,
// and skipping it must not fail the whole listing.
func TestLocalTracker_ListIssues_SkipsDotOnlyNames(t *testing.T) {
	dir := issuesDir(t)
	writeLocalIssue(t, dir, "real-issue", localIssue{
		frontmatter: localFrontmatter{Title: "Real issue", State: testLabels.Dispatchable, Created: "2026-07-09T12:00:00Z"},
	})
	for _, name := range []string{".md", "..md", "...md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	lt := NewLocalTracker(dir, testLabels)
	issues, err := lt.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "real-issue" {
		t.Fatalf("ListIssues = %+v, want just real-issue", issues)
	}
}
