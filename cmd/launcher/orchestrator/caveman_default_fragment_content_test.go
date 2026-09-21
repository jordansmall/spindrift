package main

import "testing"

// TestCavemanDefaultFragmentContract pins the prose of
// fragments/caveman-default.md (issue #2710): why each marker is exempt and the
// shape it must keep. The parity guard in markers_test.go's
// TestPromptMarkersMatchScanner covers only the bare marker literals.
func TestCavemanDefaultFragmentContract(t *testing.T) {
	cases := []promptClause{
		{
			name:   "regression guard: code, commands, error messages, and commit messages stay exempt",
			clause: "Code, commands, error messages, and commit messages are exempt and stay verbatim",
		},
		{
			name:   "regression guard: commit messages are always full human-quality prose",
			clause: "commit messages are always full human-quality prose",
		},
		{
			name:   "marker grammar routed through /caveman is forbidden",
			clause: "Never route these through `/caveman`",
		},
		{
			name:   "note= field named as human-quality prose, exempt like a commit message",
			clause: "the `note=` field of the SPINDRIFT_OUTCOME line is exempt",
		},
		{
			name:   "shape requirement is scoped to marker lines, not every exemption",
			clause: "Every exempted marker line above must keep its required shape exactly intact",
		},
		{
			name:   "outcome line's key=value pairs stay intact",
			clause: "the outcome line's key=value pairs",
		},
		{
			name:   "PR-intent line's nonce and base64 payload stay one unbroken token",
			clause: "the PR-intent line's nonce and base64 payload as one unbroken token",
		},
		{
			name:   "exempted marker lines are never reworded, reflowed, or line-wrapped",
			clause: "never reworded, reflowed, or line-wrapped",
		},
		{
			name:   "verdict line must stay the first line of the final message",
			clause: "The verdict line must additionally remain the first line of the agent's final message",
		},
	}

	assertPromptClauses(t, "fragments/caveman-default.md", cases)
}

// TestCavemanDefaultReviewFragmentContract pins the prose of
// fragments/caveman-default-review.md (issue #3265): the review-only
// exemption tier for Blocking/Non-blocking findings and Probed lines. The
// directive case is what a reviewer agent must actually act on — without it
// the rationale clauses alone survive a paragraph that inverts the order.
func TestCavemanDefaultReviewFragmentContract(t *testing.T) {
	cases := []promptClause{
		{
			name:   "Blocking/Non-blocking findings stay exempt",
			clause: "every finding written under `## Blocking` or `## Non-blocking`",
		},
		{
			name:   "Probed lines are exempt on the same tier",
			clause: "The `## Probed (APPROVE only)` lines are exempt on the same tier",
		},
		{
			name:   "Probed section rationale: text a human reads cold",
			clause: "but text a human reads cold",
		},
		{
			name:   "Probed section rationale: compressed section stops being evidence",
			clause: "compressed, the Probed section stops being evidence",
		},
		{
			name:   "Probed lines carry the full-prose directive, not just its rationale",
			clause: "Write those lines in full human-quality prose too",
		},
	}

	assertPromptClauses(t, "fragments/caveman-default-review.md", cases)
}
