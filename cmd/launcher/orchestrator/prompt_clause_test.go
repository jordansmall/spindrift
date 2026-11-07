package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the relative path from this package to the repo root, used by
// the helpers in this file that resolve a prompt or fragment file. Other
// files in this package that need the repo root declare their own local
// copy (e.g. run_test.go's chdirToFreshGitRepo return value, a different
// meaning entirely) rather than sharing this one.
var repoRoot = filepath.Join("..", "..", "..")

// hyphenLineBreak matches a line ending in a word-internal hyphen, plus the
// next line's indent. It runs before whitespace is collapsed, because only an
// actual line break marks a re-wrap: a suspended hyphen like "kernel- or
// namespace-level" keeps its space, and a bullet's "- " has no letter before
// the hyphen.
var hyphenLineBreak = regexp.MustCompile(`([\p{L}\p{N}_])-[ \t]*\r?\n[ \t]*`)

// Collapsing whitespace lets these checks survive a harmless re-wrap of a
// prompt or fragment file's prose across lines; a line break after a hyphen
// is a re-wrap too, so it is rejoined.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(hyphenLineBreak.ReplaceAllString(s, "${1}-")), " ")
}

func TestNormalizeWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "hyphen split across a line break", in: "one-\nline why", want: "one-line why"},
		{name: "chained hyphen splits", in: "a-\nb-\nc", want: "a-b-c"},
		{name: "multi-line collapse", in: "re-run\nthe   gate\n", want: "re-run the gate"},
		{name: "bullet keeps its space", in: "a\n- b", want: "a - b"},
		{name: "suspended hyphen keeps its space", in: "kernel- or\nnamespace-level", want: "kernel- or namespace-level"},
		{name: "non-ASCII letter before the hyphen", in: "café-\nstyle", want: "café-style"},
		{name: "indented continuation line", in: "one-\n   line why", want: "one-line why"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeWhitespace(c.in); got != c.want {
				t.Errorf("normalizeWhitespace(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The coordinator prompts inline the code-comments policy (issues #3419,
// #3505). prompt-code-comments-inlined (nix/checks/prompts.nix) owns verbatim
// equality against SKILL.md, so one phrase is enough here.
const codeCommentsPolicyPhrase = "the non-obvious why, a constraint, or a gotcha"

// promptClause is one clause the prose must keep verbatim, modulo line wraps.
type promptClause struct {
	name   string
	clause string
}

// Each clause gets its own subtest so a reword of one clause fails only that
// case instead of the whole paragraph.
func assertPromptClauses(t *testing.T, promptFile string, cases []promptClause) {
	t.Helper()

	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, promptFile))

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("%s no longer states %q", promptFile, c.clause)
			}
		})
	}
}

// sharedSpanBetween returns raw from the start of the line beginning with
// marker through the following blank line, for pinning a shared paragraph
// byte-for-byte even when the surrounding fragments diverge before and
// after it.
func sharedSpanBetween(t *testing.T, raw, marker string) string {
	t.Helper()

	idx := strings.Index(raw, marker)
	if idx == -1 {
		t.Fatalf("marker %q not found", marker)
	}
	rest := raw[idx:]
	if end := strings.Index(rest, "\n\n"); end != -1 {
		return rest[:end]
	}
	return rest
}
