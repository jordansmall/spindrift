package main

import "testing"

// TestWorkerPromptOperativeContract is a content-invariant guard (issue
// #3225) for worker-prompt.md's operative rules: the scope-quarantine rule,
// the turn-budget/checkpoint obligation, the no-narration rule, and the
// final-report shape. Issue #3225 cut the batching paragraph's coordinator-
// side rationale clause ("a long-running worker replays its whole
// accumulated context on every turn, so fewer, larger checks cost less than
// many small ones") while keeping the operative batching rule itself;
// pinning each clause here first means that cut can't silently take a rule
// with it.
func TestWorkerPromptOperativeContract(t *testing.T) {
	assertPromptClauses(t, "worker-prompt.md", []promptClause{
		{
			name:   "#3225 stay inside the slice, no scope expansion",
			clause: "Stay inside the slice you were handed. Do not expand scope, refactor beyond what the task requires, or touch files outside the delegation's stated area",
		},
		{
			name:   "#3225 nearing the turn budget, stop cleanly rather than pushing on",
			clause: "If your delegation states a turn budget and you're nearing it, stop cleanly instead of pushing on",
		},
		{
			name:   "#3225 report what you finished, then a remaining-work checkpoint",
			clause: "report what you finished, then a remaining-work checkpoint",
		},
		{
			name:   "#3225 checkpoint detailed enough for a fresh worker to resume",
			clause: "detailed enough for a fresh worker to resume without re-deriving anything",
		},
		{
			name:   "#3225 group related edits into a batch, one combined verification per group",
			clause: "Group related edits into a batch and run one combined verification per group, rather than an edit-then-check loop per line",
		},
		{
			name:   "#3225 no narration between tool calls",
			clause: "Do not narrate between tool calls — emit no text until the final report",
		},
		{
			name:   "#3225 final report shape: files touched, checks run, outcome, checkpoint",
			clause: "Return only a concise final report of what changed (files touched, checks run, outcome, and any remaining-work checkpoint) — no preamble or closing summary",
		},
	})
}
