package main

import "testing"

// TestReviewAxisPromptOperativeContract is a content-invariant guard (issue
// #3447) for review-axis-prompt.md's contract: the fan-out spawns two of
// these per review, and each one must stay a single-axis reporter that never
// emits a verdict of its own -- the reviewer above it owns that, and an axis
// agent that drifts into verdict grammar would collide with ADR 0035's
// scanPassLog parse. The read-only-what-the-brief-names and targeted-diff
// rules are the other half: an axis agent that reads the whole diff or the
// whole standards document is the cost the roster entry exists to bound.
func TestReviewAxisPromptOperativeContract(t *testing.T) {
	assertPromptClauses(t, "review-axis-prompt.md", []promptClause{
		{
			name:   "#3447 role: exactly one axis of a two-axis review",
			clause: "run exactly ONE axis of a two-axis code review",
		},
		{
			name:   "#3447 the caller owns the verdict",
			clause: "You do not own the verdict",
		},
		{
			name:   "#3447 no narration between tool calls",
			clause: "Do not narrate between tool calls — emit no text until your final report",
		},
		{
			name:   "#3447 read only what the brief names",
			clause: "read only what the brief names, nothing more",
		},
		{
			name:   "#3447 targeted hunks, not the whole diff",
			clause: "grep or read targeted hunks out of it rather than\nreading the whole diff into context",
		},
		{
			name:   "#3447 grep the standards document, never read it whole",
			clause: "grep the repo's coding-standards document for\nthe section relevant to the hunks you're reviewing instead of reading it\nwhole",
		},
		{
			name:   "#3447 findings only, no verdict of its own",
			clause: "no preamble, no closing summary, and no verdict\nof your own",
		},
	})
}
