package console

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestInvertEdges_ReturnsChildrenThatDeclareTheBlocker verifies invertEdges
// walks the forward edge map (child -> blockers) and returns every child
// that names number as one of its own blockers — the Blocks section's whole
// derivation, with no separate DepsOf call of its own (issue #1632).
func TestInvertEdges_ReturnsChildrenThatDeclareTheBlocker(t *testing.T) {
	edges := map[string][]string{
		"10": {"42"},
		"11": {"42", "7"},
		"12": {"7"},
	}
	sources := map[string]map[string]forge.DepSource{
		"10": {"42": forge.DepSourceNative},
		"11": {"42": forge.DepSourceBody, "7": forge.DepSourceNative},
	}

	ids, sourceOf := invertEdges(edges, sources, "42")

	if want := []string{"10", "11"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("invertEdges ids = %v, want %v", ids, want)
	}
	if sourceOf["10"] != forge.DepSourceNative {
		t.Errorf("sourceOf[10] = %v, want native", sourceOf["10"])
	}
	if sourceOf["11"] != forge.DepSourceBody {
		t.Errorf("sourceOf[11] = %v, want body", sourceOf["11"])
	}
}

// TestInvertEdges_OrdersNumericallyAscending verifies the returned child
// numbers sort ascending by numeric value — GitHub's own canonical order —
// rather than the random order Go's map iteration would otherwise produce
// (issue #1632).
func TestInvertEdges_OrdersNumericallyAscending(t *testing.T) {
	edges := map[string][]string{
		"100": {"1"},
		"9":   {"1"},
		"20":  {"1"},
	}
	sources := map[string]map[string]forge.DepSource{}

	ids, _ := invertEdges(edges, sources, "1")

	if want := []string{"9", "20", "100"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("invertEdges ids = %v, want %v (numeric order, not lexical)", ids, want)
	}
}

// TestInvertEdges_NoBlockedChildren_ReturnsEmpty verifies a number nothing
// else declares as a blocker returns an empty Blocks list, not a nil-vs-
// empty distinction callers would need to special-case.
func TestInvertEdges_NoBlockedChildren_ReturnsEmpty(t *testing.T) {
	edges := map[string][]string{"10": {"7"}}
	sources := map[string]map[string]forge.DepSource{}

	ids, sourceOf := invertEdges(edges, sources, "42")

	if len(ids) != 0 {
		t.Errorf("invertEdges ids = %v, want none", ids)
	}
	if len(sourceOf) != 0 {
		t.Errorf("invertEdges sourceOf = %v, want none", sourceOf)
	}
}

// TestResolveBlockerRef_PrefersBacklogOverFetch verifies a blocker already
// loaded in the backlog list resolves its title/state for free, with no
// Issue fetch at all (issue #1632).
func TestResolveBlockerRef_PrefersBacklogOverFetch(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "fetched title", State: forge.IssueOpen})
	all := []forge.Issue{{Number: "7", Title: "backlog title", State: forge.IssueOpen}}

	ref := resolveBlockerRef(f, all, "7", forge.DepSourceNative)

	if ref.Title != "backlog title" {
		t.Errorf("Title = %q, want the backlog row's title, not a fetched one", ref.Title)
	}
	if len(f.IssueCalls) != 0 {
		t.Errorf("IssueCalls = %v, want no fetch for a blocker already in the backlog", f.IssueCalls)
	}
}

// TestResolveBlockerRef_FetchesWhenNotInBacklog verifies a blocker not
// present in the backlog list (e.g. already closed) is resolved with its
// own Issue fetch (issue #1632).
func TestResolveBlockerRef_FetchesWhenNotInBacklog(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "closed blocker", State: forge.IssueClosed})

	ref := resolveBlockerRef(f, nil, "7", forge.DepSourceBody)

	if ref.Title != "closed blocker" {
		t.Errorf("Title = %q, want the fetched issue's title", ref.Title)
	}
	if ref.State != forge.IssueClosed {
		t.Errorf("State = %q, want closed", ref.State)
	}
	if len(f.IssueCalls) != 1 || f.IssueCalls[0] != "7" {
		t.Errorf("IssueCalls = %v, want exactly one call for #7", f.IssueCalls)
	}
}
