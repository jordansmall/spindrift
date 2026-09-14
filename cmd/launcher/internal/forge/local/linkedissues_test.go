package local

import (
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func mustFind(t *testing.T, links []forge.LinkedIssue, ref string) forge.LinkedIssue {
	t.Helper()
	for _, l := range links {
		if l.Ref == ref {
			return l
		}
	}
	t.Fatalf("no linked issue with Ref %q in %+v", ref, links)
	return forge.LinkedIssue{}
}

func TestLocalTracker_ImplementsLinkedIssueLister(t *testing.T) {
	var _ forge.LinkedIssueLister = NewLocalTracker(t.TempDir(), testLabels)
}

func TestLinkedIssues_TransitiveBlockedBy(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- b\n",
	})
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "B", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- c\n",
	})
	writeLocalIssue(t, dir, "c", localIssue{
		frontmatter: localFrontmatter{Title: "C", Created: "2026-01-01T00:00:00Z"},
		body:        "no blockers",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	b := mustFind(t, links, "b")
	if b.Relation != forge.LinkBlockedBy || b.Depth != 1 || b.LinkedFrom != "a" {
		t.Errorf("b = %+v, want relation=blocked-by depth=1 linkedFrom=a", b)
	}
	if b.Issue == nil || b.Issue.Title != "B" {
		t.Errorf("b.Issue = %+v, want resolved Title=B", b.Issue)
	}

	c := mustFind(t, links, "c")
	if c.Relation != forge.LinkBlockedBy || c.Depth != 2 || c.LinkedFrom != "b" {
		t.Errorf("c = %+v, want relation=blocked-by depth=2 linkedFrom=b", c)
	}
	if c.Issue == nil || c.Issue.Title != "C" {
		t.Errorf("c.Issue = %+v, want resolved Title=C", c.Issue)
	}
}

func TestLinkedIssues_ParentExactFileMatchIsFollowedAndWalked(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "child", localIssue{
		frontmatter: localFrontmatter{Title: "Child", Created: "2026-01-01T00:00:00Z", Parent: "parent"},
		body:        "no blockers",
	})
	writeLocalIssue(t, dir, "parent", localIssue{
		frontmatter: localFrontmatter{Title: "Parent", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- grandparent-blocker\n",
	})
	writeLocalIssue(t, dir, "grandparent-blocker", localIssue{
		frontmatter: localFrontmatter{Title: "GP Blocker", Created: "2026-01-01T00:00:00Z"},
		body:        "no blockers",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("child")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	p := mustFind(t, links, "parent")
	if p.Relation != forge.LinkParent || p.Depth != 1 || p.LinkedFrom != "child" {
		t.Errorf("parent = %+v, want relation=parent depth=1 linkedFrom=child", p)
	}
	if p.Issue == nil || p.Issue.Title != "Parent" {
		t.Errorf("parent.Issue = %+v, want resolved Title=Parent", p.Issue)
	}

	gp := mustFind(t, links, "grandparent-blocker")
	if gp.Relation != forge.LinkBlockedBy || gp.Depth != 2 || gp.LinkedFrom != "parent" {
		t.Errorf("grandparent-blocker = %+v, want relation=blocked-by depth=2 linkedFrom=parent", gp)
	}
}

func TestLinkedIssues_NonFileParentIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z", Parent: "https://github.com/o/r/issues/7"},
		body:        "",
	})
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "B", Created: "2026-01-01T00:00:00Z", Parent: "PROJ-123"},
		body:        "",
	})

	lt := NewLocalTracker(dir, testLabels)

	for _, tc := range []struct {
		num, ref string
	}{
		{"a", "https://github.com/o/r/issues/7"},
		{"b", "PROJ-123"},
	} {
		links, err := lt.LinkedIssues(tc.num)
		if err != nil {
			t.Fatalf("LinkedIssues(%s): %v", tc.num, err)
		}
		l := mustFind(t, links, tc.ref)
		if l.Issue != nil {
			t.Errorf("%s: Issue = %+v, want nil", tc.num, l.Issue)
		}
		if l.Err == nil {
			t.Errorf("%s: Err = nil, want non-nil", tc.num)
		}
		if l.Relation != forge.LinkParent {
			t.Errorf("%s: Relation = %q, want parent", tc.num, l.Relation)
		}
	}
}

func TestLinkedIssues_MissingBlockerSlugIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- missing\n",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	m := mustFind(t, links, "missing")
	if m.Issue != nil {
		t.Errorf("Issue = %+v, want nil", m.Issue)
	}
	if m.Err == nil {
		t.Errorf("Err = nil, want non-nil")
	}
}

func TestLinkedIssues_MalformedLinkedFileIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- broken\n",
	})
	// Malformed: no closing frontmatter delimiter.
	brokenPath := dir + "/broken.md"
	if err := os.WriteFile(brokenPath, []byte("---\ntitle: Broken\n"), 0o644); err != nil {
		t.Fatalf("write broken.md: %v", err)
	}

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	b := mustFind(t, links, "broken")
	if b.Issue != nil {
		t.Errorf("Issue = %+v, want nil", b.Issue)
	}
	if b.Err == nil || !strings.Contains(b.Err.Error(), "parse") {
		t.Errorf("Err = %v, want mentioning parse failure", b.Err)
	}
}

func TestLinkedIssues_CycleAndBackLinkTerminate(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- b\n",
	})
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "B", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- a\n",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	count := 0
	for _, l := range links {
		if l.Ref == "a" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("subject 'a' appeared %d times in links, want 0 (back-link terminates)", count)
	}
	bCount := 0
	for _, l := range links {
		if l.Ref == "b" {
			bCount++
		}
	}
	if bCount != 1 {
		t.Errorf("'b' appeared %d times, want 1", bCount)
	}
}

func TestLinkedIssues_ReachableAtTwoDepthsAppearsOnceAtShallowest(t *testing.T) {
	dir := t.TempDir()
	// a is blocked by b and c; b is also blocked by c. c should appear once,
	// at depth 1 (a -> c directly), not depth 2 (a -> b -> c).
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- b\n- c\n",
	})
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "B", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- c\n",
	})
	writeLocalIssue(t, dir, "c", localIssue{
		frontmatter: localFrontmatter{Title: "C", Created: "2026-01-01T00:00:00Z"},
		body:        "no blockers",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}

	count := 0
	var found forge.LinkedIssue
	for _, l := range links {
		if l.Ref == "c" {
			count++
			found = l
		}
	}
	if count != 1 {
		t.Fatalf("'c' appeared %d times, want 1", count)
	}
	if found.Depth != 1 || found.LinkedFrom != "a" {
		t.Errorf("c = %+v, want depth=1 linkedFrom=a (shallowest)", found)
	}
}

func TestLinkedIssues_LinkedFromNamesCarryingIssue(t *testing.T) {
	dir := t.TempDir()
	writeLocalIssue(t, dir, "a", localIssue{
		frontmatter: localFrontmatter{Title: "A", Created: "2026-01-01T00:00:00Z"},
		body:        "## Blocked by\n\n- b\n",
	})
	writeLocalIssue(t, dir, "b", localIssue{
		frontmatter: localFrontmatter{Title: "B", Created: "2026-01-01T00:00:00Z"},
		body:        "no blockers",
	})

	lt := NewLocalTracker(dir, testLabels)
	links, err := lt.LinkedIssues("a")
	if err != nil {
		t.Fatalf("LinkedIssues: %v", err)
	}
	b := mustFind(t, links, "b")
	if b.LinkedFrom != "a" {
		t.Errorf("LinkedFrom = %q, want %q", b.LinkedFrom, "a")
	}
}

func TestLinkedIssues_SubjectReadFailureReturnsError(t *testing.T) {
	lt := NewLocalTracker(t.TempDir(), testLabels)
	if _, err := lt.LinkedIssues("nonexistent"); err == nil {
		t.Error("LinkedIssues(nonexistent) = nil error, want non-nil")
	}
}
