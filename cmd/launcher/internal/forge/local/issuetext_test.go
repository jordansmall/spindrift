package local_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// issueTextLabels mirrors the local package's own unexported testLabels
// (local_test.go): this file lives in the external local_test package, so
// it can't see that unexported var and needs its own copy of the same
// conventional lifecycle-label set.
var issueTextLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// writeIssue writes a local issue file straight from frontmatter-shaped
// fields, mirroring the local package's own writeLocalIssue test helper but
// expressed at the forge.IssueText seam this file exercises -- these tests
// deliberately never reach into the local package's unexported localIssue
// type.
func writeIssue(t *testing.T, dir, slug, frontmatter, body string) {
	t.Helper()
	content := "---\n" + frontmatter + "---\n" + body
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestIssueText_TransitiveBlockedByRendered(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- b\n")
	writeIssue(t, dir, "b", "title: B\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- c\n")
	writeIssue(t, dir, "c", "title: C\ncreated: 2026-01-01T00:00:00Z\n", "no blockers")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "### b — B (blocked-by of a)") {
		t.Errorf("missing b heading, got:\n%s", got)
	}
	if !strings.Contains(got, "### c — C (blocked-by of b)") {
		t.Errorf("missing c heading, got:\n%s", got)
	}
}

func TestIssueText_ParentFollowedOnExactFileMatchAndWalked(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "child", "title: Child\ncreated: 2026-01-01T00:00:00Z\nparent: parent\n", "no blockers")
	writeIssue(t, dir, "parent", "title: Parent\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- gpb\n")
	writeIssue(t, dir, "gpb", "title: GPB\ncreated: 2026-01-01T00:00:00Z\n", "no blockers")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "child")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "### parent — Parent (parent of child)") {
		t.Errorf("missing parent heading, got:\n%s", got)
	}
	if !strings.Contains(got, "### gpb — GPB (blocked-by of parent)") {
		t.Errorf("missing gpb heading, got:\n%s", got)
	}
}

func TestIssueText_UnresolvedNonFileParentAndMissingBlocker(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a",
		"title: A\ncreated: 2026-01-01T00:00:00Z\nparent: https://github.com/o/r/issues/7\n",
		"## Blocked by\n\n- missing\n")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "### Unresolved references") {
		t.Fatalf("missing unresolved section, got:\n%s", got)
	}
	if !strings.Contains(got, "https://github.com/o/r/issues/7 (parent of a):") {
		t.Errorf("missing unresolved parent line, got:\n%s", got)
	}
	if !strings.Contains(got, "missing (blocked-by of a):") {
		t.Errorf("missing unresolved blocker line, got:\n%s", got)
	}
}

func TestIssueText_MalformedLinkedFileUnresolvedWithReason(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- broken\n")
	brokenPath := filepath.Join(dir, "broken.md")
	if err := os.WriteFile(brokenPath, []byte("---\ntitle: Broken\n"), 0o644); err != nil {
		t.Fatalf("write broken.md: %v", err)
	}

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	idx := strings.Index(got, "broken (blocked-by of a):")
	if idx < 0 {
		t.Fatalf("missing unresolved broken line, got:\n%s", got)
	}
	if !strings.Contains(got[idx:], "parse") {
		t.Errorf("unresolved reason doesn't mention parse failure, got:\n%s", got[idx:])
	}
}

func TestIssueText_StatusLine(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- b\n- c\n")
	writeIssue(t, dir, "b",
		"title: B\ncreated: 2026-01-01T00:00:00Z\nclosed: true\nlanding: https://example.com/pr/1\nabandoned: true\n",
		"body of b")
	writeIssue(t, dir, "c", "title: C\ncreated: 2026-01-01T00:00:00Z\n", "body of c")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "status: closed, landing: https://example.com/pr/1, abandoned") {
		t.Errorf("missing closed/landing/abandoned status line, got:\n%s", got)
	}
	if !strings.Contains(got, "status: open") {
		t.Errorf("missing open status line, got:\n%s", got)
	}
}

func TestIssueText_AdmissionOrder(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\nparent: p\n", "## Blocked by\n\n- b\n")
	writeIssue(t, dir, "b", "title: B\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- c\n")
	writeIssue(t, dir, "c", "title: C\ncreated: 2026-01-01T00:00:00Z\n", "no blockers")
	writeIssue(t, dir, "p", "title: P\ncreated: 2026-01-01T00:00:00Z\n", "no blockers")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	ib := strings.Index(got, "### b")
	ip := strings.Index(got, "### p")
	ic := strings.Index(got, "### c")
	if ib < 0 || ip < 0 || ic < 0 {
		t.Fatalf("expected all three headings present, got:\n%s", got)
	}
	if !(ib < ip && ip < ic) {
		t.Errorf("expected order b < p < c, got indices b=%d p=%d c=%d", ib, ip, ic)
	}
}

func TestIssueText_WholeEntryOmissionForSize(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\nparent: small\n", "## Blocked by\n\n- big\n")
	writeIssue(t, dir, "big", "title: Big\ncreated: 2026-01-01T00:00:00Z\n", strings.Repeat("x", 70*1024))
	writeIssue(t, dir, "small", "title: Small\ncreated: 2026-01-01T00:00:00Z\n", "small body")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if len(got) > 64*1024 {
		t.Fatalf("IssueText len = %d, want <= 64KB", len(got))
	}
	if !strings.Contains(got, "### Omitted for size") {
		t.Fatalf("missing omitted section, got tail:\n%s", got[len(got)-200:])
	}
	if !strings.Contains(got, "- big") {
		t.Errorf("expected big listed as omitted, got:\n%s", got)
	}
	if strings.Contains(got, "### big") {
		t.Errorf("big should not be rendered in full, got:\n%s", got)
	}
	if !strings.Contains(got, "### small — Small (parent of a)") {
		t.Errorf("expected small admitted in full, got:\n%s", got)
	}
	if !strings.Contains(got, "small body") {
		t.Errorf("expected small's body present, got:\n%s", got)
	}
}

func TestIssueText_SubjectOnlyTruncationUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- b\n"+strings.Repeat("x", 100*1024))
	writeIssue(t, dir, "b", "title: B\ncreated: 2026-01-01T00:00:00Z\n", "body of b")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "[truncated: issue text exceeded 64KB]") {
		t.Errorf("missing truncation marker, got tail %q", got[len(got)-80:])
	}
	if strings.Contains(got, "## Linked issues") {
		t.Errorf("subject-only truncation must not add a linked section, got tail %q", got[len(got)-200:])
	}
	if len(got) >= 100*1024 {
		t.Errorf("IssueText len = %d, want well under original 100KB", len(got))
	}
}

func TestIssueText_CycleAndBackLinkRenderOnce(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- b\n")
	writeIssue(t, dir, "b", "title: B\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- a\n")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if n := strings.Count(got, "### b — B"); n != 1 {
		t.Errorf("expected b rendered exactly once, got %d times in:\n%s", n, got)
	}
	if strings.Contains(got, "### a") {
		t.Errorf("subject must not render itself as a linked entry, got:\n%s", got)
	}
}

func TestIssueText_LinkedIssueCommentsSectionCarriedThrough(t *testing.T) {
	dir := t.TempDir()
	writeIssue(t, dir, "a", "title: A\ncreated: 2026-01-01T00:00:00Z\n", "## Blocked by\n\n- b\n")
	writeIssue(t, dir, "b", "title: B\ncreated: 2026-01-01T00:00:00Z\n",
		"body of b\n\n## Comments\n\nalice (2024-01-01): hello\n")

	lt := local.NewLocalTracker(dir, issueTextLabels)
	got, err := forge.IssueText(lt, "a")
	if err != nil {
		t.Fatalf("IssueText: %v", err)
	}
	if !strings.Contains(got, "body of b\n\n## Comments\n\nalice (2024-01-01): hello") {
		t.Errorf("expected b's own Comments section carried through verbatim, got:\n%s", got)
	}
}
