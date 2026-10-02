package main

import (
	"os"
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

// assertBodyFileAfterHeredoc asserts promptFile's first send example is the
// stdin heredoc, not `-body-file`: any `-body-file` mention must come after
// the `<<'SPINDRIFT_SIGNAL_EOF'` marker, so the fallback flag never upstages
// the one-call stdin send in the first example a reader sees. Both the
// marker and `-body-file` are required — either missing means the fragment
// never grew (or lost) the one-call stdin example this pins.
func assertBodyFileAfterHeredoc(t *testing.T, promptFile string) {
	t.Helper()

	raw := readPromptFile(t, repoRoot, promptFile)

	heredocIdx := strings.Index(raw, "<<'SPINDRIFT_SIGNAL_EOF'")
	if heredocIdx == -1 {
		t.Errorf("%s missing the <<'SPINDRIFT_SIGNAL_EOF' heredoc marker", promptFile)
	}

	idx := strings.Index(raw, "-body-file")
	if idx == -1 {
		t.Errorf("%s missing -body-file", promptFile)
		return
	}
	if heredocIdx != -1 && idx < heredocIdx {
		t.Errorf("%s mentions -body-file before the heredoc example, want the first example to be stdin-only", promptFile)
	}
}

// bareEOFDelimiter matches every bare-EOF heredoc opener variant (`<<EOF`,
// `<<'EOF'`, `<<"EOF"`, `<<-EOF`, `<<-'EOF'`, ...). The trailing \b keeps an
// EOF-suffixed name like `<<'SPINDRIFT_SIGNAL_EOF'` from matching: EOF must
// end a word, not just appear as its last three letters.
var bareEOFDelimiter = regexp.MustCompile(`<<-?\s*['"]?EOF\b`)

// assertNoBareEOFDelimiter asserts promptFile no longer uses any bare-EOF
// heredoc delimiter variant (issue #3866): untrusted issue/comment text
// quoted in the heredoc body can carry a bare `EOF` line of its own, which
// would end the heredoc early and run the rest as shell.
func assertNoBareEOFDelimiter(t *testing.T, promptFile string) {
	t.Helper()

	raw := readPromptFile(t, repoRoot, promptFile)

	if bareEOFDelimiter.MatchString(raw) {
		t.Errorf("%s still uses a bare EOF heredoc delimiter, want <<'SPINDRIFT_SIGNAL_EOF'", promptFile)
	}
}

// assertClosingDelimiterAtColumnOne asserts promptFile's rendered heredoc
// example closes with `SPINDRIFT_SIGNAL_EOF` alone at column one (issue
// #3866): a copied, indented closing delimiter (the fragment's own example
// used to render as an indented code block) never matches, so bash never
// terminates the heredoc.
func assertClosingDelimiterAtColumnOne(t *testing.T, promptFile string) {
	t.Helper()

	raw := readPromptFile(t, repoRoot, promptFile)
	if !strings.Contains(raw, "\nSPINDRIFT_SIGNAL_EOF\n") {
		t.Errorf("%s missing a closing SPINDRIFT_SIGNAL_EOF line alone at column one", promptFile)
	}
}

// sharedSpanFrom returns raw from the start of the line beginning with
// marker through the end of the string, for pinning two fragments' shared
// tail byte-for-byte.
func sharedSpanFrom(t *testing.T, raw, marker string) string {
	t.Helper()

	idx := strings.Index(raw, marker)
	if idx == -1 {
		t.Fatalf("marker %q not found", marker)
	}
	return raw[idx:]
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

// assertInlinesCodeCommentsPolicy asserts promptFile inlines the
// code-comments skill's policy body verbatim (issues #3419, #3505). It reads
// SKILL.md itself -- not through readPromptFile, which only resolves
// templates/default/prompts/... -- so a reworded skill fails here instead of
// leaving a hand-typed copy behind.
func assertInlinesCodeCommentsPolicy(t *testing.T, promptFile string) {
	t.Helper()

	skillPath := filepath.Join(repoRoot, "templates", "default", "skills", "code-comments", "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read %s: %v", skillPath, err)
	}

	lines := strings.Split(string(raw), "\n")
	seen := 0
	var body []string
	for _, line := range lines {
		if seen >= 2 {
			body = append(body, line)
		}
		if line == "---" && seen < 2 {
			seen++
		}
	}
	policy := normalizeWhitespace(strings.Join(body, "\n"))
	if policy == "" {
		t.Fatalf("%s yielded an empty policy body -- missing its second '---' frontmatter delimiter?", skillPath)
	}

	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, promptFile))
	if !strings.Contains(normalized, policy) {
		t.Errorf("%s no longer states the code-comments policy verbatim", promptFile)
	}
	if strings.Contains(normalized, "/code-comments") {
		t.Errorf("%s still references the /code-comments skill anchor, want the policy inlined instead", promptFile)
	}
}
