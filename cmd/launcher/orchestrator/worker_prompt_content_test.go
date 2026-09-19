package main

import "testing"

// TestWorkerPromptOperativeContract guards worker-prompt.md's operative rules
// against silent content drift (issues #3225, #3419, #3420). Issue #3225 cut
// the batching paragraph's coordinator-side rationale clause while keeping the
// operative batching rule itself, so this table pins each clause separately and
// a later cut cannot quietly take a rule with it.
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
			name:   "#3420 batch a group into one patch file, apply once, verify once",
			clause: "write them into one patch file and apply it in a single command, then verify once for the group",
		},
		{
			name:   "#3420 apply with git apply --recount -C1 --reject",
			clause: "Apply with `git apply --recount -C1 --reject`, which recounts the hunk headers and matches on one line of context, so exact line numbers are not load-bearing",
		},
		{
			name:   "#3420 unplaceable hunks land in a .rej file",
			clause: "any hunk it cannot place is written to a `.rej` file beside its target — fix those from the reject output rather than re-reading the file",
		},
		{
			name:   "#3420 delete reject files once the group lands",
			clause: "delete the reject files once the group lands",
		},
		{
			name:   "#3420 never re-read a file solely to construct a patch",
			clause: "Never re-read a file solely to construct a patch",
		},
		{
			name:   "#3420 never batch a change dependent on another change in the group",
			clause: "never group a change whose content depends on another change in the same group",
		},
		{
			name:   "#3419 check/build output arrives as a bounded tail with the full log on disk",
			clause: "Check and build output already reaches you as a bounded tail with the full log on disk — grep that log file for anything the tail cut off, never read it whole",
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

	assertInlinesCodeCommentsPolicy(t, "worker-prompt.md")
}
