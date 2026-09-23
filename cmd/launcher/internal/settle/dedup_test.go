package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// normalizeDedupKey trims, collapses internal whitespace runs to one space,
// and lowercases -- the three normalizations the dedup key comparison relies
// on to treat "different prose, same site" as one key (issue #3609).
func TestNormalizeDedupKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"leading/trailing whitespace", "  Foo   Bar  ", "foo bar"},
		{"already lower", "already lower", "already lower"},
		{"tab as whitespace", "Tab\tSeparated", "tab separated"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeDedupKey(c.in); got != c.want {
				t.Errorf("normalizeDedupKey(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// normalizeDedupTerm normalizes like normalizeDedupKey, then rejects a term
// that would corrupt the marker line: one carrying "," (the marker's own
// field separator) or "--" (would prematurely close the marker's HTML
// comment) is dropped whole rather than split or stripped (issue #3609
// review).
func TestNormalizeDedupTerm(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"plain term", "  Race In Settle  ", "race in settle", true},
		{"blank term", "   ", "", false},
		{"comma dropped", "a.go:F, misc", "", false},
		{"arrow-comment dropped", "closes -->  the comment", "", false},
		{"bare double-dash dropped", "a--b", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalizeDedupTerm(c.in)
			if got != c.want || ok != c.wantOK {
				t.Errorf("normalizeDedupTerm(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// splitDedupTerms' key set is terms-only: no title argument, and an intent
// with no usable term has an empty key set rather than falling back to prose
// (issue #3609 review -- keying on a formulaic title merges distinct
// findings).
func TestSplitDedupTerms_TermsOnly(t *testing.T) {
	keys, _ := splitDedupTerms([]string{"  race in settle  ", "", "  "})
	want := map[string]bool{"race in settle": true}
	if len(keys) != len(want) {
		t.Fatalf("splitDedupTerms keys = %v, want %v", keys, want)
	}
	for k := range want {
		if !keys[k] {
			t.Errorf("splitDedupTerms missing key %q; got %v", k, keys)
		}
	}
}

// A term-less intent's key set is empty, not a title fallback.
func TestSplitDedupTerms_EmptyWhenNoTerms(t *testing.T) {
	if keys, _ := splitDedupTerms(nil); len(keys) != 0 {
		t.Errorf("splitDedupTerms(nil) keys = %v, want empty", keys)
	}
}

// A comma-bearing term is dropped from the key set entirely, never split
// into two keys -- splitting would invent a bogus generic key ("misc" in
// this example) that could suppress an unrelated later finding.
func TestSplitDedupTerms_CommaTermDropped(t *testing.T) {
	keys, _ := splitDedupTerms([]string{"a.go:F, misc"})
	if len(keys) != 0 {
		t.Errorf("splitDedupTerms keys = %v, want empty (comma term dropped whole)", keys)
	}
}

// buildDedupMarker/parseDedupMarker round-trip: the terms recovered from a
// body carrying the marker line match what went in, normalized (issue
// #3609).
func TestDedupMarker_RoundTrips(t *testing.T) {
	marker := buildDedupMarker([]string{"  Race In Settle  ", "Dup   Filing"})
	if marker == "" {
		t.Fatal("buildDedupMarker returned empty for non-empty terms")
	}
	body := "some finding body\n\nmore detail\n\n" + marker
	got := parseDedupMarker(body)
	want := []string{"race in settle", "dup filing"}
	if len(got) != len(want) {
		t.Fatalf("parseDedupMarker = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseDedupMarker[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A multi-term set including an invalid term round-trips exactly the valid
// terms: buildDedupMarker drops the invalid one going in, so
// parseDedupMarker never even sees it.
func TestDedupMarker_RoundTripsDropsInvalidTerm(t *testing.T) {
	marker := buildDedupMarker([]string{"good term", "bad, term", "fine"})
	got := parseDedupMarker(marker)
	want := []string{"good term", "fine"}
	if len(got) != len(want) {
		t.Fatalf("parseDedupMarker = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseDedupMarker[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A body that quotes a marker line (e.g. a finding about this feature fenced
// in a code block) followed by the filer's own appended marker parses to the
// filer's terms only -- the quoted marker must never hijack the key set
// (issue #3609).
func TestParseDedupMarker_LastMarkerWinsOverQuoted(t *testing.T) {
	quoted := "```\n" + buildDedupMarker([]string{"quoted", "placeholder"}) + "\n```"
	real := buildDedupMarker([]string{"real term"})
	body := "finding body quoting the marker format:\n\n" + quoted + "\n\nmore detail\n\n" + real
	got := parseDedupMarker(body)
	want := []string{"real term"}
	if len(got) != len(want) {
		t.Fatalf("parseDedupMarker = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseDedupMarker[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A body with no marker line parses to nil, not an empty non-nil slice or an
// error -- the backlog index treats both the same, but the nil return keeps
// the no-marker case cheap to assert directly.
func TestParseDedupMarker_NoMarkerLine(t *testing.T) {
	if got := parseDedupMarker("just a plain body\n\nno marker here"); got != nil {
		t.Errorf("parseDedupMarker = %v, want nil", got)
	}
}

// The bare marker fileIssueIntentsDetailed appends when an intent has no
// usable term parses back to zero keys, so an always-appended marker costs a
// term-less issue nothing in the backlog index (issue #3609 review).
func TestParseDedupMarker_EmptyMarkerYieldsNoKeys(t *testing.T) {
	body := "a finding body\n\n" + dedupMarkerPrefix + dedupMarkerSuffix
	if got := parseDedupMarker(body); len(got) != 0 {
		t.Errorf("parseDedupMarker = %v, want no keys", got)
	}
}

// All-blank terms (after normalization) render nothing, leaving the caller
// to substitute the bare marker above rather than emitting a marker that
// carries an empty term.
func TestBuildDedupMarker_AllBlankTermsProducesEmpty(t *testing.T) {
	if got := buildDedupMarker([]string{"", "   "}); got != "" {
		t.Errorf("buildDedupMarker = %q, want empty", got)
	}
	if got := buildDedupMarker(nil); got != "" {
		t.Errorf("buildDedupMarker(nil) = %q, want empty", got)
	}
}

// A term that is entirely invalid (comma or "--" only) also produces no
// marker, the same as an all-blank set.
func TestBuildDedupMarker_AllInvalidTermsProducesEmpty(t *testing.T) {
	if got := buildDedupMarker([]string{"a, b", "-->"}); got != "" {
		t.Errorf("buildDedupMarker = %q, want empty", got)
	}
}

// matchDedup finds a hit on any key in the set and returns its reference; a
// key set with no hit reports ok=false.
func TestMatchDedup(t *testing.T) {
	index := map[string]string{"race in settle": "#42"}
	if ref, ok := matchDedup(index, map[string]bool{"race in settle": true, "other": true}); !ok || ref != "#42" {
		t.Errorf("matchDedup hit = (%q, %v), want (#42, true)", ref, ok)
	}
	if _, ok := matchDedup(index, map[string]bool{"nothing here": true}); ok {
		t.Error("matchDedup reported a hit for a disjoint key set")
	}
}

// isFindingIssue recognizes both provenance labels and rejects an issue
// carrying neither -- the gate that keeps an ordinary backlog issue from ever
// suppressing a filing.
func TestIsFindingIssue(t *testing.T) {
	if !isFindingIssue([]string{"bug", findingLabelReview}) {
		t.Error("isFindingIssue false for a review-finding-labeled issue")
	}
	if !isFindingIssue([]string{findingLabelResearch}) {
		t.Error("isFindingIssue false for a research-finding-labeled issue")
	}
	if isFindingIssue([]string{"bug", "ready-for-agent"}) {
		t.Error("isFindingIssue true for an issue carrying neither finding label")
	}
}

// labeledBacklogListerStub wraps forge.IssueTrackerFake with
// forge.LabeledBacklogLister so backlogDedupIndex's capability-preferred path
// (issue #3609 review) has something to prefer, recording the labels it was
// asked for and returning a scripted, already-labelled slice -- unlike the
// fake's own ListOpenIssues, which forces isFindingIssue to filter.
type labeledBacklogListerStub struct {
	*forge.IssueTrackerFake
	calls  [][]string
	issues []forge.Issue
	err    error
}

func (s *labeledBacklogListerStub) ListOpenIssuesWithLabels(labels []string) ([]forge.Issue, error) {
	s.calls = append(s.calls, labels)
	if s.err != nil {
		return nil, s.err
	}
	return s.issues, nil
}

// backlogDedupIndex prefers forge.LabeledBacklogLister when the tracker
// implements it, passing both provenance labels, and never falls through to
// ListOpenIssues.
func TestBacklogDedupIndex_PrefersLabeledBacklogLister(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: []forge.Issue{
			{Number: "9", Labels: []string{findingLabelReview}, Body: "<!-- spindrift-dedup: race in settle -->"},
		},
	}
	// A ListOpenIssues call would return this instead; its absence from the
	// index proves the capability path, not the fallback, produced it.
	stub.SetIssue(forge.Issue{Number: "1", Labels: []string{findingLabelReview}, Body: "<!-- spindrift-dedup: from list open issues -->"})

	index := backlogDedupIndex(stub, "100")

	if len(stub.calls) != 1 {
		t.Fatalf("ListOpenIssuesWithLabels calls = %d, want 1", len(stub.calls))
	}
	got := stub.calls[0]
	want := []string{findingLabelReview, findingLabelResearch}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ListOpenIssuesWithLabels labels = %v, want %v", got, want)
	}
	if index["race in settle"] != "#9" {
		t.Errorf("index[race in settle] = %q, want #9", index["race in settle"])
	}
	if _, ok := index["from list open issues"]; ok {
		t.Error("index carries a key from ListOpenIssues; want the capability path used exclusively")
	}
}

// Without the capability, backlogDedupIndex falls back to ListOpenIssues, the
// pre-#3609-review behavior forgejo/jira/local rely on.
func TestBacklogDedupIndex_FallsBackToListOpenIssues(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{findingLabelReview}, Body: "<!-- spindrift-dedup: race in settle -->"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{"unrelated"}, Body: "<!-- spindrift-dedup: not a finding -->"})

	index := backlogDedupIndex(fc, "100")

	if index["race in settle"] != "#1" {
		t.Errorf("index[race in settle] = %q, want #1", index["race in settle"])
	}
	if _, ok := index["not a finding"]; ok {
		t.Error("index carries a key from an issue lacking a finding label")
	}
}

// A LabeledBacklogLister list failure is non-fatal: backlogDedupIndex warns
// in the same "?? #<num>" style as the sibling degradations in this package
// and falls back to an empty index rather than erroring the whole filing
// pass (issue #3609 review).
func TestBacklogDedupIndex_ListFailureWarnsAndReturnsEmpty(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		err:              errors.New("boom"),
	}

	var index map[string]string
	stderr := captureStderr(t, func() {
		index = backlogDedupIndex(stub, "100")
	})

	if len(index) != 0 {
		t.Errorf("index = %v, want empty", index)
	}
	if !strings.Contains(stderr, "?? #100") || !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want a warning naming issue #100 and the underlying error", stderr)
	}
}
