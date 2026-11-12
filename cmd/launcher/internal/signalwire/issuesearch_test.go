package signalwire

import (
	"encoding/json"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestIssueSearchLabelAcceptsOnlyTheFixedSet(t *testing.T) {
	for _, l := range []string{"agent-review-finding", "agent-research-reject", "agent-research-finding", "agent-butler-finding"} {
		if !IssueSearchLabel(l) {
			t.Errorf("IssueSearchLabel(%q) = false", l)
		}
	}
	for _, l := range []string{"", "agent-trigger", "ready-for-agent", "Agent-Review-Finding", "agent-review-finding "} {
		if IssueSearchLabel(l) {
			t.Errorf("IssueSearchLabel(%q) = true", l)
		}
	}
}

func TestCapIssueSearchBoundsResultsAndTitles(t *testing.T) {
	var in IssueSearchReply
	for range MaxIssueSearchResults + 5 {
		in.Issues = append(in.Issues, IssueSearchResult{Number: "1", Title: strings.Repeat("é", MaxIssueSearchTitleBytes), State: "open"})
	}
	got := CapIssueSearch(in)
	if len(got.Issues) != MaxIssueSearchResults || !got.Truncated {
		t.Fatalf("got %d results truncated=%v", len(got.Issues), got.Truncated)
	}
	for _, i := range got.Issues {
		if len(i.Title) > MaxIssueSearchTitleBytes || strings.ContainsRune(i.Title, '�') {
			t.Fatalf("title %d bytes, want a rune-aligned cut within %d", len(i.Title), MaxIssueSearchTitleBytes)
		}
	}
	if len(in.Issues) != MaxIssueSearchResults+5 {
		t.Errorf("CapIssueSearch mutated its input")
	}
}

func TestCapIssueSearchEncodesEmptyAsArrays(t *testing.T) {
	b, err := json.Marshal(CapIssueSearch(IssueSearchReply{}))
	if err != nil || string(b) != `{"issues":[],"truncated":false}` {
		t.Fatalf("got %s, %v", b, err)
	}
	b, _ = json.Marshal(CapIssueSearch(IssueSearchReply{Issues: []IssueSearchResult{{Number: "1"}}}))
	if !strings.Contains(string(b), `"labels":[]`) {
		t.Fatalf("got %s, want labels as []", b)
	}
}

// TestIssueSearchLabelsCoverTheFindingLabels pins the hardcoded set against the
// dispatch-kind rows, which declare the finding provenance labels once.
func TestIssueSearchLabelsCoverTheFindingLabels(t *testing.T) {
	for _, d := range dispatchkind.All {
		if !IssueSearchLabel(d.FindingLabel) {
			t.Errorf("%s: FindingLabel %q is not an accepted issue-search label", d.Name, d.FindingLabel)
		}
	}
}
