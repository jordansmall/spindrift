package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Collapsing whitespace lets these checks survive a harmless re-wrap of a
// prompt or fragment file's prose across lines.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
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

	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, promptFile))

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("%s no longer states %q", promptFile, c.clause)
			}
		})
	}
}

// assertInlinesCodeCommentsPolicy asserts promptFile inlines the
// code-comments skill's policy body verbatim (issues #3419, #3505). It reads
// SKILL.md itself -- not through readPromptFile, which only resolves
// templates/default/prompts/... -- so a reworded skill fails here instead of
// leaving a hand-typed copy behind.
func assertInlinesCodeCommentsPolicy(t *testing.T, promptFile string) {
	t.Helper()

	repoRoot := filepath.Join("..", "..", "..")
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
