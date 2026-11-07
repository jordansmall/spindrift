//go:build integration

package promptassembly_test

import (
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// clauseContracts binds spindrift's own prompt fragments; a Consumer override
// of a fragment keeps free wording.
var clauseContracts = []clauseContract{
	// commit-rework-orchestrator.md
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "Run-state handoff",
		reason:   "the fold-or-not decision reads the seeded handoff section, so the fragment must name it (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "Last reviewer verdict:",
		reason:   "no env knob marks a rework pass, so the fragment keys off the handoff's verdict line; bare handoff presence is too coarse (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "git commit --amend",
		reason:   "a rework pass folds a fix into the branch-tip commit rather than adding a follow-up commit (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "autosquash fixup",
		reason:   "a rework pass folds a fix into an older commit with an autosquash fixup (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "rewriting that history here is expected",
		reason:   "the agent must know amending the branch's earlier commits is sanctioned on a rework pass (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "small focused commits",
		reason:   "a first-slice pass keeps the several-small-commits preference (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "force-push",
		absent:   true,
		reason:   "the fragment is ungated and a host-mediated local Box relays a bundle rather than force-pushing, so it must stay forge-neutral (issue #5285).",
	},

	// review-loop-orchestrator.md
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/pass-summary.md",
		reason:   "the pass summary goes to its own file, apart from dispositions and decisions (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/dispositions.md",
		reason:   "dispositions go to a file separate from the pass summary, at the path --dispositions-path defaults to (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "fixed in commit",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "a fixed finding's disposition line must cite the fixing commit (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "won't-fix",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "a deliberately unfixed finding's disposition line must say so (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "pasted diff hunks",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "the dispositions file stays a terse reference, not pasted diff content (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/decisions.md",
		reason:   "decisions go to a file separate from both the pass summary and the dispositions, at the path --decisions-path defaults to (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "chose",
		from:     "a third, separate file",
		reason:   "each decision line records what was chosen (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "rejected",
		from:     "a third, separate file",
		reason:   "each decision line records the rejected alternative (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "constraint",
		from:     "a third, separate file",
		reason:   "each decision line records the constraint that drove the choice (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "pasted diff hunks",
		from:     "a third, separate file",
		reason:   "the decisions file stays a terse reference, not pasted diff content (issue #2695).",
	},
}

// TestClauseContracts assembles every golden cell once and checks each
// contract against the fragment text the cells actually rendered.
func TestClauseContracts(t *testing.T) {
	renders := renderedFragments{}
	for _, cell := range goldenCells() {
		c := defaultCell()
		cell.setup(c)
		result, _ := assembleCell(t, c, t.TempDir())
		renders[cell.name] = promptassembly.FragmentRenders(result)
	}

	for _, c := range clauseContracts {
		t.Run(c.fragment+"/"+c.clause, func(t *testing.T) {
			if err := checkClauseContract(c, renders); err != nil {
				t.Error(err)
			}
		})
	}
}
