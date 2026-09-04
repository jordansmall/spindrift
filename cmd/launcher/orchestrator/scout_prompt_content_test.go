package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestScoutPromptOperativeContract is a content-invariant guard (issue
// #3225) for scout-prompt.md's output contract: the four section headings,
// the "this final message IS the brief" rule, the line budget, the
// do-not-implement and no-narration rules, and the citation obligations
// each section states. Issue #3225 cuts the Map section's rationale
// sentence for the citation obligation ("The reader verifies from these
// excerpts instead of re-reading the tree, so a claim without one costs
// more than it saves") while keeping the obligation itself; pinning each
// clause here first means that cut can't silently take a rule with it. One
// case per clause, so a reword of one clause fails only its own case
// instead of the whole paragraph.
func TestScoutPromptOperativeContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "scout-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "#3225 role: explore and return a structured brief",
			clause: "Your role: explore the repo and return a structured brief for the implementer",
		},
		{
			name:   "#3225 this final message IS the brief",
			clause: "This final message IS the brief",
		},
		{
			name:   "#3225 max ~60 lines",
			clause: "Max ~60 lines",
		},
		{
			name:   "#3225 do not implement",
			clause: "Do not implement",
		},
		{
			name:   "#3225 no narration between tool calls",
			clause: "Do not narrate between tool calls — emit no text until this final brief",
		},
		{
			name:   "#3225 Map heading",
			clause: "## Map",
		},
		{
			name:   "#3225 Invariants & gotchas heading",
			clause: "## Invariants & gotchas",
		},
		{
			name:   "#3225 Suggested approach heading",
			clause: "## Suggested approach",
		},
		{
			name:   "#3225 Ruled out heading",
			clause: "## Ruled out",
		},
		{
			name:   "#3225 every load-bearing claim carries a cited verbatim excerpt",
			clause: "Every load-bearing claim — a seam, signature, invariant, or gotcha a coordinator decision will rest on — also carries a cited verbatim excerpt: the file's own lines quoted under a path:line anchor, not a paraphrase or a loose pointer",
		},
		{
			name:   "#3225 trim excerpts to decision-rich lines",
			clause: "Trim each excerpt to the decision-rich lines; never dump a whole file or function",
		},
		{
			name:   "#3225 Invariants & gotchas cites the same way as the Map",
			clause: "Cite each with a verbatim excerpt, same shape as the Map",
		},
		{
			name:   "#3225 Suggested approach cites a verbatim excerpt for signature/line steps",
			clause: "Cite a verbatim excerpt for any step that rests on a specific signature or line",
		},
		{
			name:   "#3225 return only the brief, no preamble or closing summary",
			clause: "Return only the brief — no preamble or closing summary",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("scout-prompt.md no longer states %q", c.clause)
			}
		})
	}
}
