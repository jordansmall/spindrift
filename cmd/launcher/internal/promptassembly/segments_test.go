package promptassembly

import (
	"strings"
	"testing"
)

// substVars wraps each string value of raw as a one-segment body owned by
// Source{SourceVar, name} -- the shape assemblePromptBodies builds a
// scalar allowlist entry into, reused here so renderSegments tests exercise
// the same var shape production code will hand it.
func substVars(raw map[string]string) map[string]body {
	out := make(map[string]body, len(raw))
	for k, v := range raw {
		out[k] = varBody(k, v)
	}
	return out
}

func assertRenderMatchesSubstitute(t *testing.T, raw string, allowlist map[string]string) {
	t.Helper()
	want := strings.TrimRight(substitute(raw, allowlist), "\n")
	owner := Source{Kind: SourceTemplate, Name: "t"}
	got := renderSegments(raw, owner, substVars(allowlist)).trimTrailingNewlines().text()
	if got != want {
		t.Fatalf("renderSegments text mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderSegmentsMatchesSubstituteNoTokens(t *testing.T) {
	assertRenderMatchesSubstitute(t, "plain text, no tokens here\n", nil)
}

func TestRenderSegmentsMatchesSubstituteTokenAtStart(t *testing.T) {
	assertRenderMatchesSubstitute(t, "${NAME} trailing text\n", map[string]string{"NAME": "VALUE"})
}

func TestRenderSegmentsMatchesSubstituteTokenAtEnd(t *testing.T) {
	assertRenderMatchesSubstitute(t, "leading text ${NAME}", map[string]string{"NAME": "VALUE"})
}

func TestRenderSegmentsMatchesSubstituteRepeatedTokens(t *testing.T) {
	assertRenderMatchesSubstitute(t, "${A}-${A}-${B}-${A}", map[string]string{"A": "x", "B": "y"})
}

func TestRenderSegmentsMatchesSubstituteUnknownToken(t *testing.T) {
	assertRenderMatchesSubstitute(t, "known=${A} unknown=${NOPE}", map[string]string{"A": "x"})
}

func TestRenderSegmentsMatchesSubstituteEmptyValuedVar(t *testing.T) {
	assertRenderMatchesSubstitute(t, "before[${EMPTY}]after", map[string]string{"EMPTY": ""})
}

func TestRenderSegmentsMatchesSubstituteVarEndsInNewlines(t *testing.T) {
	assertRenderMatchesSubstitute(t, "head ${TRAILING} tail\n\n", map[string]string{"TRAILING": "value\n\n\n"})
}

// TestRenderSegmentsNestedAttribution exercises a var whose own body is
// multi-segment -- e.g. a fragment that itself substituted a carried
// scalar -- confirming renderSegments splices in the nested segments (and
// their own sources) rather than flattening them into the referencing
// template's ownership.
func TestRenderSegmentsNestedAttribution(t *testing.T) {
	fragmentOwner := Source{Kind: SourceFragment, Name: "frag.md"}
	nested := body{
		{src: fragmentOwner, text: "rendered "},
		{src: Source{Kind: SourceVar, Name: "ISSUE_NUMBER"}, text: "123"},
		{src: fragmentOwner, text: " fragment\n\n"},
	}
	vars := map[string]body{"FRAGMENT_VAR": nested}
	owner := Source{Kind: SourceTemplate, Name: "t"}
	raw := "before ${FRAGMENT_VAR}after"

	got := renderSegments(raw, owner, vars)
	want := "before rendered 123 fragment\n\nafter"
	if got.text() != want {
		t.Fatalf("text mismatch: got %q want %q", got.text(), want)
	}

	// Confirm the nested segments kept their own sources rather than being
	// absorbed into owner.
	foundVar := false
	for _, s := range got {
		if s.src.Kind == SourceVar && s.src.Name == "ISSUE_NUMBER" {
			foundVar = true
			if s.text != "123" {
				t.Fatalf("var segment text = %q, want 123", s.text)
			}
		}
	}
	if !foundVar {
		t.Fatalf("expected a var-source segment to survive in the output, got %+v", got)
	}
}

// TestBodyLengthsPartitionText asserts the sum of every segment's byte
// length equals len(body.text()), and that summing per-source lengths
// reproduces the same total with no remainder -- the "byte attribution is
// exactly linear" invariant the slice's brief calls out.
func TestBodyLengthsPartitionText(t *testing.T) {
	b := body{
		{src: Source{Kind: SourceTemplate, Name: "t"}, text: "abc "},
		{src: Source{Kind: SourceFragment, Name: "f.md"}, text: "defgh\n\n"},
		{src: Source{Kind: SourceCarried, Name: "X"}, text: "ij"},
		{src: Source{Kind: SourceFragment, Name: "f.md"}, text: "klm"},
	}

	total := len(b.text())
	sum := 0
	perSource := map[Source]int{}
	for _, s := range b {
		sum += len(s.text)
		perSource[s.src] += len(s.text)
	}
	if sum != total {
		t.Fatalf("sum of segment lengths = %d, want %d", sum, total)
	}

	partitioned := 0
	for _, n := range perSource {
		partitioned += n
	}
	if partitioned != total {
		t.Fatalf("per-source sums = %d, want %d (no remainder)", partitioned, total)
	}
}

// TestTrimTrailingNewlinesConsumesWholeTailSegment covers a tail segment
// that is nothing but newlines, so trimming must drop the segment entirely
// rather than leave an attributed-but-empty one behind.
func TestTrimTrailingNewlinesConsumesWholeTailSegment(t *testing.T) {
	b := body{
		{src: Source{Kind: SourceTemplate, Name: "t"}, text: "hello"},
		{src: Source{Kind: SourceFragment, Name: "f.md"}, text: "\n\n"},
	}
	got := b.trimTrailingNewlines()
	if got.text() != "hello" {
		t.Fatalf("text = %q, want %q", got.text(), "hello")
	}
	if len(got) != 1 {
		t.Fatalf("expected the fully-newline tail segment to be dropped, got %+v", got)
	}
}

// TestTrimTrailingNewlinesAcrossTwoTailSegments covers a trailing run of
// newlines that spans the boundary between the last two segments: the very
// last segment is pure newline (dropped entirely) and the newline run
// continues into the second-to-last segment's own tail (partially trimmed).
func TestTrimTrailingNewlinesAcrossTwoTailSegments(t *testing.T) {
	b := body{
		{src: Source{Kind: SourceTemplate, Name: "t"}, text: "hello\n\n"},
		{src: Source{Kind: SourceFragment, Name: "f.md"}, text: "\n"},
	}
	got := b.trimTrailingNewlines()
	if got.text() != "hello" {
		t.Fatalf("text = %q, want %q", got.text(), "hello")
	}
}
