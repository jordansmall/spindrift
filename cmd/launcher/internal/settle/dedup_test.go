package settle

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// normalizeDedupKey trims, collapses internal whitespace runs to one space,
// lowercases, folds ".", ":", "#", "/", "_" runs to a single ":", and trims
// any leading/trailing ":" left by the fold -- the normalizations the dedup
// key comparison relies on to treat "different prose, same site" as one key
// (issue #3609), including a site named with a different punctuation style
// (issue #3977).
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
		{"dot-separated", "pkg/file.go:Type.Field", "pkg:file:go:type:field"},
		{"colon-separated", "pkg/file.go:Type:Field", "pkg:file:go:type:field"},
		{"hash-separated", "pkg/file.go:type#field", "pkg:file:go:type:field"},
		{"underscore-separated", "pkg/file.go:type_field", "pkg:file:go:type:field"},
		{"space around separator", "Type. Field", "type:field"},
		{"trailing separator", "mount.go:", "mount:go"},
		{"leading separator", "#3957", "3957"},
		{"separator-only", "./_", ""},
		{"dash-only", "-", ""},
		{"dash-separator-dash", "-:-", ""},
		{"dash-dot-dash with spaces", "- . -", ""},
		{"dash dash", "- -", ""},
		{"punctuation-only bangs", "!!", ""},
		{"punctuation-only plus", "+", ""},
		{"dash with letter kept", "-v", "-v"},
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
		{"separator-only dropped", "._/", "", false},
		{"lone hyphen dropped", "-", "", false},
		{"punctuation-only dropped", "!!", "", false},
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

// UsableDedupTerm is exactly normalizeDedupTerm's ok, so a caller outside
// settle keeps the same terms splitDedupTerms does (issue #4036).
func TestUsableDedupTerm_AgreesWithNormalizeDedupTerm(t *testing.T) {
	for _, in := range []string{"a.go:Foo", "-", "!!", "a.go:F, misc", "a--b", ""} {
		_, ok := normalizeDedupTerm(in)
		if got := UsableDedupTerm(in); got != ok {
			t.Errorf("UsableDedupTerm(%q) = %v, want %v", in, got, ok)
		}
	}
}

// Punctuation folding must never manufacture a "--": folding onto "-"
// instead of ":" would turn a term already carrying a hyphen next to a
// folded separator into a rejected term. Folding onto ":" keeps both terms
// accepted, with no "--" anywhere in the normalized form.
func TestNormalizeDedupTerm_PunctuationFoldNeverManufacturesDoubleDash(t *testing.T) {
	for _, in := range []string{"a-:-b", "a- . -b"} {
		got, ok := normalizeDedupTerm(in)
		if !ok {
			t.Errorf("normalizeDedupTerm(%q) ok = false, want true", in)
		}
		if strings.Contains(got, "--") {
			t.Errorf("normalizeDedupTerm(%q) = %q, contains \"--\"", in, got)
		}
	}
}

// A site named with "." in one filing and ":" in another (issue
// #3957/#3958's exact pair) normalizes to the same dedup key, so
// splitDedupTerms' new-intent key set matches an old-style marker already
// sitting in the backlog via parseDedupMarker/backlogDedupIndex/matchDedup.
func TestMatchDedup_AcrossOldAndNewSeparatorStyle(t *testing.T) {
	newIntentTerm := "cmd/launcher/internal/runner/mount.go:mountParams:boxForgeAndIssueAccess"
	keys, dropped := splitDedupTerms([]string{newIntentTerm})
	if len(dropped) != 0 {
		t.Fatalf("splitDedupTerms dropped = %v, want none", dropped)
	}
	if len(keys) != 1 {
		t.Fatalf("splitDedupTerms keys = %v, want exactly one", keys)
	}

	oldMarkerBody := "some finding body\n\n" +
		"<!-- spindrift-dedup: cmd/launcher/internal/runner/mount.go:mountParams.boxForgeAndIssueAccess -->"
	index := make(map[string]string)
	indexFindingIssues(index, []forge.Issue{
		{Number: "9", Labels: []string{dispatchkind.Work.FindingLabel}, Body: oldMarkerBody},
	})

	ov := matchDedup(index, keys)
	if !ov.full || len(ov.refs) != 1 || ov.refs[0] != "#9" {
		t.Errorf("matchDedup = %+v, want full match against #9", ov)
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

// A separator-only term normalizes to blank, so it yields neither a key nor
// a dropped-term warning: it carried no information to lose.
func TestSplitDedupTerms_SeparatorOnlyIsBlank(t *testing.T) {
	keys, dropped := splitDedupTerms([]string{"./_"})
	if len(keys) != 0 || len(dropped) != 0 {
		t.Errorf("splitDedupTerms = %v, %v; want no keys, no drops", keys, dropped)
	}
}

// A term with no letter or digit -- dash-only or punctuation-only -- also
// normalizes to blank, the same as a separator-only term: neither a key nor
// a dropped-term warning.
func TestSplitDedupTerms_PunctuationOnlyIsBlank(t *testing.T) {
	keys, dropped := splitDedupTerms([]string{"-", "-:-", "!!", "-->", "---"})
	if len(keys) != 0 || len(dropped) != 0 {
		t.Errorf("splitDedupTerms = %v, %v; want no keys, no drops", keys, dropped)
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

// A punctuated term round-trips through buildDedupMarker/parseDedupMarker
// already folded (issue #3977): the marker carries the normalized key, not
// the raw punctuation.
func TestDedupMarker_RoundTripsFoldsPunctuation(t *testing.T) {
	marker := buildDedupMarker([]string{"pkg/file.go:Type.Field"})
	got := parseDedupMarker(marker)
	want := []string{"pkg:file:go:type:field"}
	if !slices.Equal(got, want) {
		t.Errorf("parseDedupMarker = %v, want %v", got, want)
	}
}

// Terms that are all punctuation-only (no letter or digit) normalize to
// blank, so buildDedupMarker has nothing usable to carry and returns "".
func TestDedupMarker_PunctuationOnlyTermsReturnsEmpty(t *testing.T) {
	if got := buildDedupMarker([]string{"-", "-:-", "!!"}); got != "" {
		t.Errorf("buildDedupMarker(punctuation-only) = %q, want empty", got)
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

// matchDedup partitions keys into the subset index already tracks; a
// multi-site intent must file when even one of its sites is untracked, so the
// caller needs a three-way answer, not an any-hit boolean: full decides
// skip-versus-file, covered names which sites were already tracked, and refs
// names every distinct issue tracking them.
func TestMatchDedup(t *testing.T) {
	// Both keys covered by the same issue: full, refs names it once.
	index := map[string]string{"site a": "#42", "site b": "#42"}
	ov := matchDedup(index, map[string]bool{"site a": true, "site b": true})
	if !ov.full || len(ov.refs) != 1 || ov.refs[0] != "#42" {
		t.Errorf("matchDedup(all covered, one issue) = %+v, want full, refs [#42]", ov)
	}

	// Both keys covered, but by two different issues: full, refs names both
	// in covered-key-sorted order.
	index = map[string]string{"site a": "#42", "site b": "#43"}
	ov = matchDedup(index, map[string]bool{"site a": true, "site b": true})
	if !ov.full || len(ov.refs) != 2 || ov.refs[0] != "#42" || ov.refs[1] != "#43" {
		t.Errorf("matchDedup(all covered, two issues) = %+v, want full, refs [#42 #43]", ov)
	}

	// Only one of two keys covered: the caller must still file, so covered
	// must name exactly the tracked site, not the whole set.
	ov = matchDedup(index, map[string]bool{"site a": true, "site c": true})
	if ov.full || len(ov.covered) != 1 || ov.covered[0] != "site a" || len(ov.refs) != 1 || ov.refs[0] != "#42" {
		t.Errorf("matchDedup(partial) = %+v, want not full, covered [site a], refs [#42]", ov)
	}

	// Two of three keys covered, by two different issues: not full, but
	// refs still names both -- the partial-overlap line prints them all.
	ov = matchDedup(index, map[string]bool{"site a": true, "site b": true, "site c": true})
	if ov.full || len(ov.covered) != 2 || len(ov.refs) != 2 || ov.refs[0] != "#42" || ov.refs[1] != "#43" {
		t.Errorf("matchDedup(partial, two issues) = %+v, want not full, covered [site a site b], refs [#42 #43]", ov)
	}

	// Disjoint key set: nothing covered, no refs.
	ov = matchDedup(index, map[string]bool{"nothing here": true})
	if ov.full || len(ov.covered) != 0 || len(ov.refs) != 0 {
		t.Errorf("matchDedup(disjoint) = %+v, want not full, empty covered and refs", ov)
	}

	// An empty key set must never read as "all covered" -- see
	// TestFileIssueIntentsDetailed_Dedup_NoTermsWarns for the caller-side
	// guard this backs.
	ov = matchDedup(index, map[string]bool{})
	if ov.full || len(ov.covered) != 0 || len(ov.refs) != 0 {
		t.Errorf("matchDedup(empty keys) = %+v, want not full, empty covered and refs", ov)
	}
}

// Map iteration order is randomized, so the returned covered keys and refs
// must both be deterministic: sorted key order, not whichever key the runtime
// visits first. Looped to pin this against map-order flakiness.
func TestMatchDedup_DeterministicRefsAcrossManyCoveredKeys(t *testing.T) {
	index := map[string]string{
		"a site": "#1",
		"b site": "#2",
		"c site": "#3",
	}
	keys := map[string]bool{"a site": true, "b site": true, "c site": true}
	for i := 0; i < 20; i++ {
		ov := matchDedup(index, keys)
		if !ov.full || len(ov.covered) != 3 || ov.covered[0] != "a site" || ov.covered[1] != "b site" || ov.covered[2] != "c site" {
			t.Fatalf("iteration %d: matchDedup = %+v, want full, covered [a site b site c site]", i, ov)
		}
		if len(ov.refs) != 3 || ov.refs[0] != "#1" || ov.refs[1] != "#2" || ov.refs[2] != "#3" {
			t.Fatalf("iteration %d: matchDedup = %+v, want refs [#1 #2 #3]", i, ov)
		}
	}
}

// dedupKeyLineSpec parses a normalized key's trailing line-site segment, or
// reports ok=false for a key that names no line site at all (issue #4108).
func TestDedupKeyLineSpec(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		wantPrefix string
		wantLo     int
		wantHi     int
		wantOK     bool
	}{
		{"single line", "pkg:file:go:80", "pkg:file:go", 80, 80, true},
		{"line range", "pkg:file:go:80-83", "pkg:file:go", 80, 83, true},
		{"no trailing segment at all", "80", "", 0, 0, false},
		{"empty prefix", ":80", "", 0, 0, false},
		{"trailing colon, no segment", "pkg:file:go:", "", 0, 0, false},
		{"non-numeric segment", "pkg:file:go:main", "", 0, 0, false},
		{"reversed range", "pkg:file:go:83-80", "", 0, 0, false},
		{"zero line", "pkg:file:go:0", "", 0, 0, false},
		{"malformed range", "pkg:file:go:80-", "", 0, 0, false},
		{"leading-sign single line", "pkg:file:go:+80", "", 0, 0, false},
		{"leading-sign range hi", "pkg:file:go:80-+83", "", 0, 0, false},
		{"overflow single line", "pkg:file:go:1234567890123456789012345", "", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prefix, lo, hi, ok := dedupKeyLineSpec(c.key)
			if ok != c.wantOK || prefix != c.wantPrefix || lo != c.wantLo || hi != c.wantHi {
				t.Errorf("dedupKeyLineSpec(%q) = (%q, %d, %d, %v), want (%q, %d, %d, %v)",
					c.key, prefix, lo, hi, ok, c.wantPrefix, c.wantLo, c.wantHi, c.wantOK)
			}
		})
	}
}

// dedupLineOverlap requires both a shared file prefix and an intersecting
// line range -- either alone is not enough (issue #4108).
func TestDedupLineOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical single line", "pkg:file:go:80", "pkg:file:go:80", true},
		{"range contains point", "pkg:file:go:80-83", "pkg:file:go:80", true},
		{"point at range tail", "pkg:file:go:80-83", "pkg:file:go:83", true},
		{"ranges overlap partially", "pkg:file:go:80-83", "pkg:file:go:82-90", true},
		{"adjacent, not overlapping", "pkg:file:go:80-83", "pkg:file:go:84", false},
		{"same lines, different file", "pkg:a:go:80-83", "pkg:b:go:80-83", false},
		{"one side not a line site", "pkg:file:go:80", "pkg:file:go:main", false},
		{"neither side a line site", "pkg:file:go:main", "pkg:file:go:other", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dedupLineOverlap(c.a, c.b); got != c.want {
				t.Errorf("dedupLineOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// When two run keys both overlap one intent key, the smallest run key's ref
// must win regardless of Go's randomized map order. Looped, like
// TestMatchDedup_DeterministicRefsAcrossManyCoveredKeys, to pin this against
// map-order flakiness.
func TestApplyRunLineOverlap_DeterministicRefAcrossOverlappingRunKeys(t *testing.T) {
	for i := 0; i < 20; i++ {
		dedupIndex := map[string]string{}
		runKeys := map[string]string{
			"pkg:file:go:80-85": "ref A",
			"pkg:file:go:82-90": "ref B",
		}
		keys := map[string]bool{"pkg:file:go:83": true}
		applyRunLineOverlap(dedupIndex, runKeys, keys)
		if got := dedupIndex["pkg:file:go:83"]; got != "ref A" {
			t.Fatalf("iteration %d: dedupIndex[%q] = %q, want %q", i, "pkg:file:go:83", got, "ref A")
		}
	}
}

// One key of an intent must never alias onto a sibling key of the same
// intent: 83-90 overlaps only 80-83, not the run's filed 80, so it stays
// unaliased whichever key the map visits first. Looped against map order.
func TestApplyRunLineOverlap_SiblingKeysNeverChain(t *testing.T) {
	for i := 0; i < 200; i++ {
		dedupIndex := map[string]string{"pkg:file:go:80": "ref A"}
		runKeys := map[string]string{"pkg:file:go:80": "ref A"}
		keys := map[string]bool{"pkg:file:go:80-83": true, "pkg:file:go:83-90": true}
		applyRunLineOverlap(dedupIndex, runKeys, keys)
		if got := dedupIndex["pkg:file:go:80-83"]; got != "ref A" {
			t.Fatalf("iteration %d: dedupIndex[%q] = %q, want %q", i, "pkg:file:go:80-83", got, "ref A")
		}
		if got, ok := dedupIndex["pkg:file:go:83-90"]; ok {
			t.Fatalf("iteration %d: dedupIndex[%q] = %q, want unaliased", i, "pkg:file:go:83-90", got)
		}
	}
}

// isFindingIssue recognizes every provenance label and rejects an issue
// carrying none of them -- the gate that keeps an ordinary backlog issue from
// ever suppressing a filing.
func TestIsFindingIssue(t *testing.T) {
	if !isFindingIssue([]string{"bug", dispatchkind.Work.FindingLabel}) {
		t.Error("isFindingIssue false for a review-finding-labeled issue")
	}
	if !isFindingIssue([]string{dispatchkind.Research.FindingLabel}) {
		t.Error("isFindingIssue false for a research-finding-labeled issue")
	}
	if !isFindingIssue([]string{dispatchkind.Butler.FindingLabel}) {
		t.Error("isFindingIssue false for a butler-finding-labeled issue (ADR 0056)")
	}
	if !isFindingIssue([]string{"agent-butler-patch"}) {
		t.Error("isFindingIssue false for an agent-butler-patch-labeled issue (ADR 0057)")
	}
	if isFindingIssue([]string{"bug", "ready-for-agent"}) {
		t.Error("isFindingIssue true for an issue carrying neither finding label")
	}
}

// backlogListerCall records one ListIssuesWithLabels invocation:
// labeledBacklogListerStub keeps one per call so a test can assert both the
// state and the labels asked for.
type backlogListerCall struct {
	state  forge.IssueState
	labels []string
}

// labeledBacklogListerStub wraps forge.IssueTrackerFake with
// forge.LabeledBacklogLister so backlogDedupIndex's capability-preferred path
// (issue #3609 review; closed lookup added issue #3873) has something to
// prefer. issues and errs are keyed by state so a test can script the open
// and closed lookups independently -- unlike the fake's own ListOpenIssues,
// which forces isFindingIssue to filter.
type labeledBacklogListerStub struct {
	*forge.IssueTrackerFake
	calls  []backlogListerCall
	issues map[forge.IssueState][]forge.Issue
	errs   map[forge.IssueState]error
}

func (s *labeledBacklogListerStub) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	s.calls = append(s.calls, backlogListerCall{state: state, labels: labels})
	if err := s.errs[state]; err != nil {
		return nil, err
	}
	return s.issues[state], nil
}

// callFor returns the recorded call for state, or nil if none was made.
func (s *labeledBacklogListerStub) callFor(state forge.IssueState) *backlogListerCall {
	for i := range s.calls {
		if s.calls[i].state == state {
			return &s.calls[i]
		}
	}
	return nil
}

// backlogDedupIndex prefers forge.LabeledBacklogLister when the tracker
// implements it, calling it once per state (closed, then open), each scoped
// to every provenance label, and never falls through to ListOpenIssues.
func TestBacklogDedupIndex_PrefersLabeledBacklogLister(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueOpen: {
				{Number: "9", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: race in settle -->"},
			},
		},
	}
	// A ListOpenIssues call would return this instead; its absence from the
	// index proves the capability path, not the fallback, produced it.
	stub.SetIssue(forge.Issue{Number: "1", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: from list open issues -->"})

	index := backlogDedupIndex(stub, "100")

	if len(stub.calls) != 2 {
		t.Fatalf("ListIssuesWithLabels calls = %d, want 2 (one per state)", len(stub.calls))
	}
	want := []string{dispatchkind.Work.FindingLabel, dispatchkind.Research.FindingLabel, dispatchkind.Butler.FindingLabel, "agent-butler-patch"}
	for _, state := range []forge.IssueState{forge.IssueOpen, forge.IssueClosed} {
		call := stub.callFor(state)
		if call == nil {
			t.Fatalf("no ListIssuesWithLabels call for state %v", state)
		}
		if len(call.labels) != len(want) {
			t.Fatalf("ListIssuesWithLabels(%v) labels = %v, want %v", state, call.labels, want)
		}
		for i := range want {
			if call.labels[i] != want[i] {
				t.Errorf("ListIssuesWithLabels(%v) labels = %v, want %v", state, call.labels, want)
			}
		}
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
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: race in settle -->"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{"unrelated"}, Body: "<!-- spindrift-dedup: not a finding -->"})

	index := backlogDedupIndex(fc, "100")

	if index["race in settle"] != "#1" {
		t.Errorf("index[race in settle] = %q, want #1", index["race in settle"])
	}
	if _, ok := index["not a finding"]; ok {
		t.Error("index carries a key from an issue lacking a finding label")
	}
}

// Both LabeledBacklogLister lookups failing is non-fatal: backlogDedupIndex
// warns twice, in the same "?? #<num>" style as the sibling degradations in
// this package, and falls back to an empty index rather than erroring the
// whole filing pass (issue #3609 review).
func TestBacklogDedupIndex_ListFailureWarnsAndReturnsEmpty(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		errs: map[forge.IssueState]error{
			forge.IssueOpen:   errors.New("boom"),
			forge.IssueClosed: errors.New("boom"),
		},
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

// A closed finding-labelled issue is indexed alongside open ones (issue
// #3873): a closed finding was already filed once and must never be refiled
// just because it was since closed.
func TestBacklogDedupIndex_IndexesClosedFindingIssue(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{Number: "5", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: closed site -->"},
			},
		},
	}

	index := backlogDedupIndex(stub, "100")

	if index["closed site"] != "#5" {
		t.Errorf("index[closed site] = %q, want #5", index["closed site"])
	}
}

// Closed dedup coverage extends to a butler-labelled finding too (ADR 0056:
// "Host dedup covers closed findings, for every finding kind"), not just the
// review/research labels the two tests above already cover.
func TestBacklogDedupIndex_IndexesClosedButlerFindingIssue(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{Number: "7", Labels: []string{dispatchkind.Butler.FindingLabel}, Body: "<!-- spindrift-dedup: closed butler site -->"},
			},
		},
	}

	index := backlogDedupIndex(stub, "100")

	if index["closed butler site"] != "#7" {
		t.Errorf("index[closed butler site] = %q, want #7", index["closed butler site"])
	}
}

// A finding the host already landed as a patch PR carries agent-butler-patch
// instead of (or alongside) agent-butler-finding (ADR 0057); the dedup index
// must still list it, since it is not any Descriptor's FindingLabel.
func TestBacklogDedupIndex_IndexesButlerPatchLabelledIssue(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueOpen: {
				{Number: "9", Labels: []string{"agent-butler-patch"}, Body: "<!-- spindrift-dedup: patched butler site -->"},
			},
		},
	}

	index := backlogDedupIndex(stub, "100")

	if index["patched butler site"] != "#9" {
		t.Errorf("index[patched butler site] = %q, want #9", index["patched butler site"])
	}
}

// A closed issue lacking a finding label never suppresses a filing, same as
// the open case (isFindingIssue filters both lookups' results identically).
func TestBacklogDedupIndex_ClosedNonFindingIssueNotIndexed(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{Number: "5", Labels: []string{"bug"}, Body: "<!-- spindrift-dedup: not a finding -->"},
			},
		},
	}

	index := backlogDedupIndex(stub, "100")

	if _, ok := index["not a finding"]; ok {
		t.Error("index carries a key from a closed issue lacking a finding label")
	}
}

// When the same dedup key is covered by both a closed and an open finding
// issue, the open issue's ref wins: it is indexed second, and the actively
// tracked issue is the more useful pointer to surface (issue #3873).
func TestBacklogDedupIndex_OpenRefWinsOverClosedForSharedKey(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{Number: "5", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: shared site -->"},
			},
			forge.IssueOpen: {
				{Number: "9", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: shared site -->"},
			},
		},
	}

	index := backlogDedupIndex(stub, "100")

	if index["shared site"] != "#9" {
		t.Errorf("index[shared site] = %q, want #9 (open ref wins)", index["shared site"])
	}
}

// A closed-lookup failure warns naming "closed" and still indexes whatever
// the open lookup returned -- the two lookups degrade independently (issue
// #3873).
func TestBacklogDedupIndex_ClosedFailureWarnsAndKeepsOpenKeys(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueOpen: {
				{Number: "9", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: open site -->"},
			},
		},
		errs: map[forge.IssueState]error{
			forge.IssueClosed: errors.New("boom"),
		},
	}

	var index map[string]string
	stderr := captureStderr(t, func() {
		index = backlogDedupIndex(stub, "100")
	})

	if index["open site"] != "#9" {
		t.Errorf("index[open site] = %q, want #9 (open lookup unaffected by closed failure)", index["open site"])
	}
	if !strings.Contains(stderr, "?? #100") || !strings.Contains(stderr, "closed") || !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want a warning naming issue #100, \"closed\", and the underlying error", stderr)
	}
}

// An open-lookup failure warns naming "open" and still indexes whatever the
// closed lookup returned -- the mirror of the case above.
func TestBacklogDedupIndex_OpenFailureWarnsAndKeepsClosedKeys(t *testing.T) {
	stub := &labeledBacklogListerStub{
		IssueTrackerFake: forge.NewFake().IssueTrackerFake,
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{Number: "5", Labels: []string{dispatchkind.Work.FindingLabel}, Body: "<!-- spindrift-dedup: closed site -->"},
			},
		},
		errs: map[forge.IssueState]error{
			forge.IssueOpen: errors.New("boom"),
		},
	}

	var index map[string]string
	stderr := captureStderr(t, func() {
		index = backlogDedupIndex(stub, "100")
	})

	if index["closed site"] != "#5" {
		t.Errorf("index[closed site] = %q, want #5 (closed lookup unaffected by open failure)", index["closed site"])
	}
	if !strings.Contains(stderr, "?? #100") || !strings.Contains(stderr, "open") || !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want a warning naming issue #100, \"open\", and the underlying error", stderr)
	}
}
