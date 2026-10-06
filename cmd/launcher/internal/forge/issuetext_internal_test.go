package forge

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// package forge (not forge_test) because renderLinkedIssues is unexported.

func manyUnresolved(n int) []LinkedIssue {
	var out []LinkedIssue
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("local-issue-reference-%019d", i) // 41 bytes
		out = append(out, LinkedIssue{
			Ref: ref, Relation: LinkBlockedBy, LinkedFrom: "42", Depth: 1,
			Err: errors.New("read local issue " + ref + ": open /x/" + ref + ".md: no such file or directory"),
		})
	}
	return out
}

func manyResolved(n int) []LinkedIssue {
	var out []LinkedIssue
	for i := 0; i < n; i++ {
		out = append(out, LinkedIssue{
			Ref: fmt.Sprintf("issue-%04d", i), Relation: LinkParent, LinkedFrom: "42", Depth: 1,
			Issue: &Issue{Title: "t", Body: strings.Repeat("x", 100)},
		})
	}
	return out
}

func TestRenderLinkedIssues_HoldsBudget(t *testing.T) {
	both := append(manyUnresolved(50), manyResolved(500)...)
	tests := []struct {
		name         string
		links        []LinkedIssue
		budget       int
		wantCollapse bool
	}{
		{"many unresolved", manyUnresolved(50), 200, true},
		{"many omitted", manyResolved(500), 200, true},
		{"both", both, 200, true},
		{"both, roomy", both, 3000, true},
		{"budget just above heading", both, len(linkedIssuesHeading) + 1, false},
		{"budget heading+40", both, len(linkedIssuesHeading) + 40, false},
		{"budget heading+60", both, len(linkedIssuesHeading) + 60, true},
		{"budget heading+40 unresolved", manyUnresolved(50), len(linkedIssuesHeading) + 40, false},
		{"everything fits", append(manyUnresolved(2), manyResolved(2)...), 4000, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for budget := tc.budget; budget <= tc.budget+3; budget++ {
				out := renderLinkedIssues(tc.links, budget)
				if len(out) > budget {
					t.Fatalf("budget %d: len(out)=%d overshoots", budget, len(out))
				}
			}
			out := renderLinkedIssues(tc.links, tc.budget)
			if got := strings.Contains(out, "- … and "); tc.wantCollapse && !got {
				t.Errorf("want a collapse line, got:\n%s", out)
			} else if !tc.wantCollapse && got {
				t.Errorf("unexpected collapse line:\n%s", out)
			}
		})
	}
}

func TestRenderLinkedIssues_CollapseCount(t *testing.T) {
	out := renderLinkedIssues(manyResolved(500), 200)
	var shown int
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "- issue-") {
			shown++
		}
	}
	want := fmt.Sprintf("- … and %d more", 500-shown)
	if !strings.HasSuffix(out, want) {
		t.Errorf("want suffix %q, got:\n%s", want, out)
	}
}

func TestRenderLinkedIssues_FitsUnchanged(t *testing.T) {
	links := append(manyUnresolved(2), manyResolved(1)...)
	links[2].Issue.Body = strings.Repeat("y", 5000)
	out := renderLinkedIssues(links, 1000)
	if strings.Contains(out, "- … and ") {
		t.Errorf("unexpected collapse:\n%s", out)
	}
	want := "### Omitted for size\n\n- issue-0000"
	if !strings.HasSuffix(out, want) {
		t.Errorf("want omitted list %q at end, got:\n%s", want, out)
	}
}

func TestRenderLinkedIssues_NeverOvershoots(t *testing.T) {
	links := append(manyUnresolved(20), manyResolved(60)...)
	for i, l := range links[20:] {
		l.Issue.Body = strings.Repeat("z", 7*i)
	}
	for budget := 0; budget <= 8000; budget++ {
		if out := renderLinkedIssues(links, budget); len(out) > budget {
			t.Fatalf("budget %d: len(out)=%d overshoots", budget, len(out))
		}
	}
}
