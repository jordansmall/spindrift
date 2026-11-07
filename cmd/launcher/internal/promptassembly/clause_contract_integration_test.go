//go:build integration

package promptassembly_test

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalwire"
)

// clauseContracts binds spindrift's own prompt fragments; a Consumer override
// of a fragment keeps free wording. A clause several fragments carry alike is
// in sharedClauses, and one a hand-mirrored pair must keep in step is also
// compared in twinContracts, which pins the pair equal where this table pins
// only what each must say.
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

	// review-loop-orchestrator.md: non-blocking triage
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "acceptance criteria",
		reason:   "the triage's \"in scope\" is anchored to the issue's own acceptance criteria (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "slice as originally authored",
		reason:   "the triage's \"in scope\" is anchored to the slice as originally authored (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "second review round",
		reason:   "item 3's fix-vs-escalate default is round-aware (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "escalate it",
		reason:   "item 3 names escalation as the outcome of a round-aware tiebreak (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "3. Escalate",
		from:     "2. Drop",
		reason:   "item 2 (Drop) must stand as a genuine third outcome between items 1 and 3, not collapse back into the binary fix/escalate triage (issue #3610).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "The floor is worth, not certainty",
		from:     "2. Drop",
		to:       "3. Escalate",
		reason:   "item 2 drops on worth, not certainty, or a revert to a confidence test would re-file every trivial out-of-scope finding (issue #3610; the Nix triage-drop-arm obligation in lib/prompt-contract.nix is its counterpart).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "legitimate third outcome",
		from:     "2. Drop",
		to:       "3. Escalate",
		reason:   "dropping is named a legitimate third outcome, not a failure to act (issue #3610).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Scope is never the uncertain part — whether this branch touched a surface is a fact the diff answers — so what reaches item 3 this way is still out of scope, and item 3 files it rather than fixing it.",
		from:     "2. Drop",
		to:       "3. Escalate",
		reason:   "the scope gate sits in item 2's span, where a whole-file check (the Nix triage-escape-hatch-scope obligation) cannot see it move into the preamble or item 3 (issue #3816).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Item 1's scope pin is absolute here, so the round-aware tiebreak below only ever runs where fixing is available at all",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "item 3's carve-out: the round-aware tiebreak runs only where an inline fix is available (issue #3816).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "there is no inline fix to weigh, so an uncertain finding item 2 hands up is filed, at every round.",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "an uncertain finding on a surface the branch never touched is filed at every round (issue #3816).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "When unsure whether a finding clears that bar: on the first review round, fix it rather than file it.",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "the tiebreak is matched verbatim so an inverted default (first round escalates, second fixes) cannot pass (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "From the second review round on, escalate it only when fixing it would widen the diff",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "from round 2 on, escalation is gated on diff growth (issues #2701, #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "is still fixed inline, at every round",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "an ambiguous finding whose fix stays small and inside the branch's touched surface is fixed inline at every round, not only the first (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Narrowing the round-2 flip to diff growth is this tiebreak's calibration, not a weakening of it.",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "losing the diff-growth calibration would silently widen what round 2 escalates back toward the old blanket flip (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "review round",
		absent:   true,
		from:     "1. Fix inline",
		to:       "2. Drop",
		reason:   "item 1's fix-inline rule stays unconditional across every round; gating it on the round would stop fixing in-scope findings from round 2 on (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Lines this branch itself touched in an earlier round's own absorbed fix count as that surface: they are this branch's own work, so a finding about them is in scope at every round.",
		from:     "1. Fix inline",
		to:       "2. Drop",
		reason:   "item 1 counts the branch's own earlier-round fixes as in-scope surface; matched verbatim so a verb shift cannot pass on a loose substring (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "not whatever surface the diff has since grown to touch",
		absent:   true,
		from:     "1. Fix inline",
		to:       "Before stopping this turn",
		reason:   "the old scope-pin wording was deleted and must not return (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "\"## Round N (verdict: ...)\" section headers",
		reason:   "each orchestrator pass is a fresh session, so it reads the round from the Findings log's headers (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "N == 1 means this is the first review round; N > 1 means the second review round or later.",
		reason:   "the round-count mapping is matched verbatim so an inverted mapping cannot pass on keywords (issue #2701).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "deferring only costs an extra filed issue",
		absent:   true,
		reason:   "the fallback must not carry its own cost model, which would override item 3's narrowed test and license round-2 over-firing (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "not a silently widened diff, so it's the safer failure mode here",
		absent:   true,
		reason:   "the fallback must not carry its own cost model, which would override item 3's narrowed test and license round-2 over-firing (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "under item 3 below that default is not a blanket deferral but the narrower diff-growth test",
		reason:   "the fallback for an undeterminable round defers to item 3's narrowed test (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "stays the default regardless of round",
		reason:   "the opening framing is reconciled with the round-aware tiebreak; \"regardless of round\", not \"on every round\", since the triage runs once per pass (issue #3611).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "applies only to the non-blocking triage below, not the blocking-verdict handling above",
		reason:   "round-awareness is scoped to non-blocking triage; blocking findings are fixed the round they are raised (issue #3611).",
	},

	// review-loop-orchestrator.md: no re-escalation
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "already escalated",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "item 3 must say a finding already escalated is never escalated again (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "earlier round of this run",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "the no-reescalate guard covers an escalation from an earlier round of the same run (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "must never be escalated again",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "a re-escalated finding is filed twice under different keys (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "a relayed filer's intents file only once the run ends",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "relayed intents are filed host-side after the run ends, so a later round's dedup search cannot see them (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "its own duplicate search",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "the filer's own duplicate search cannot see in-flight intents of an earlier round (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "a reworded copy can slip past any search",
		from:     "3. Escalate",
		to:       "Before stopping this turn",
		reason:   "a reworded copy can slip past any search, so the guard cannot lean on dedup (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "escalated to filer",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "an escalated finding's won't-fix reason is recorded as \"escalated to filer\", so a later round recognises it and skips item 3 (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "escalated to PR body",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "with no filer present the won't-fix reason is \"escalated to PR body\" (issue #4108).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "already escalated",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "the dispositions paragraph says why the reason is recorded: so a later round knows a finding is already escalated (issue #4108).",
	},

	// review-loop-orchestrator.md: pre-existing failure
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Non-blocking triage —",
		from:     "When a check surfaces a failure",
		reason:   "the pre-existing-failure paragraph comes before the non-blocking triage intro (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "`git stash` alone does not establish a clean base",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "proving a failure pre-existing needs a clean checkout of the base, which `git stash` cannot give once the slice's edits are committed (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "git fetch origin",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "the base ref is fetched first so it is not stale (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "check out `origin/main` itself somewhere outside this working tree",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "the clean base is the base branch itself checked out outside the working tree (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "sibling worktree",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "a sibling worktree is one way to reach the clean tree (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "or a fresh clone",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "a fresh clone is the other way to reach the clean tree (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "is an unmet precondition, not proof, so do not go on to claim pre-existence from that run",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "a cleaning step that reports nothing to clean proves nothing (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Report which revision you verified against and how you reached the clean tree",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "the claim must name the revision and how the clean tree was reached (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "auditable from this log",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "the pre-existence claim must be auditable from the run's log (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "A failure you cannot prove this way is a failure this branch caused",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "an unproven failure is the branch's own to fix (issue #2714).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "reproduces solely in the Box is still real",
		from:     "When a check surfaces a failure",
		to:       "Non-blocking triage —",
		reason:   "the proof runs wherever the check actually ran; a Box-only failure is still real (issue #2714).",
	},

	// review-loop-orchestrator-filer.md and review-loop-orchestrator-no-filer.md:
	// the twins differ only in the FILE ISSUES mentions (issue #5300) and
	// twinContracts pins the pair equal, so this table holds only what each must
	// say.
	{
		fragment: "review-loop-orchestrator-filer.md",
		clause:   "Do not run FILE ISSUES, LAND THE CHANGE",
		reason:   "with a Filer, the stop-after-COMMIT bullet names FILE ISSUES among the steps not to run (issue #5300).",
	},
	{
		fragment: "review-loop-orchestrator-filer.md",
		clause:   "continue straight into FILE ISSUES, LAND THE CHANGE",
		reason:   "with a Filer, the APPROVE bullet continues into FILE ISSUES (issue #5300).",
	},
	{
		fragment: "review-loop-orchestrator-no-filer.md",
		clause:   "FILE ISSUES",
		absent:   true,
		reason:   "with no Filer provisioned there is no FILE ISSUES step to name; the twins differ only in those mentions (issue #5300).",
	},

	// file-issues-triage-recap.md
	{
		fragment: "file-issues-triage-recap.md",
		clause:   "Escalation is the third, and it is this step",
		reason:   "the recap names escalation as the third outcome, not the old binary fix/escalate triage, which would make an agent skip FILE ISSUES with a genuine escalated finding in hand (issue #3610).",
	},
	{
		fragment: "file-issues-triage-recap.md",
		clause:   "already fixed inline every non-blocking finding",
		absent:   true,
		reason:   "the old binary-triage wording must not return (issue #3610).",
	},
	{
		fragment: "file-issues-triage-recap.md",
		clause:   "an ambiguous finding REVIEW's own round-aware tiebreak deferred rather than fixed",
		reason:   "a finding escalated by REVIEW's round-aware tiebreak must not be silently dropped once it reaches FILE ISSUES (issue #2701).",
	},
	{
		fragment: "file-issues-triage-recap.md",
		clause:   "An uncertain finding on a surface the branch never touched arrives here at every round instead",
		reason:   "an uncertain finding on an untouched surface has no inline fix, so it arrives at every round, not only from round 2 (issue #3816).",
	},
	{
		fragment: "file-issues-triage-recap.md",
		clause:   "If none survived, skip this step; do not re-file what you just fixed or dropped.",
		reason:   "with no surviving finding the step is skipped and nothing already fixed or dropped is re-filed (issues #2701, #3826).",
	},

	// file-issues-direct.md, file-issues-relay.md, file-issues-relay-socket.md
	{
		fragment: "file-issues-direct.md",
		clause:   "the issue number, and the branch (or PR URL, once one is open) for provenance.",
		reason:   "only the direct carrier can hold a PR URL, so only it offers one; the sentence's head is held equal across the filer variants by twinContracts (issues #3826, #3827, #6184).",
	},

	// land-pass-order-orchestrator.md
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "Last reviewer verdict:",
		reason:   "prompt assembly renders one prompt per run, not per pass, so the fragment scopes itself to the land pass off the handoff's verdict line (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "APPROVE",
		reason:   "the land-pass work order is keyed on the verdict line reading APPROVE (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "git fetch origin",
		reason:   "the land pass fetches before it rebases (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "git rebase origin/main",
		reason:   "the land pass rebases onto the fresh base branch (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "non-blocking fix",
		absent:   true,
		to:       "git rebase origin/main",
		reason:   "rebasing must precede applying non-blocking fixes, so a reorder cannot regress to the old gate-then-rebase-then-regate order (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "non-blocking fix",
		from:     "git rebase origin/main",
		reason:   "the fragment must still order the non-blocking fixes after the rebase (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "check gate",
		absent:   true,
		to:       "git rebase origin/main",
		reason:   "rebasing must precede running the check gate, so a reorder cannot regress to the old gate-then-rebase-then-regate order (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "check gate",
		from:     "git rebase origin/main",
		reason:   "the fragment must still order the check gate after the rebase (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "already up to date",
		reason:   "a rebase that reported the branch already up to date owes no second gate run (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "re-run the gate",
		reason:   "a second gate run is owed only to a tree change since the first gate ran, never unconditionally (issue #3214).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "Folds",
		reason:   "the fragment draws the line between reviewer-sourced folds and gate-discovered work, which issue #3221's land pass lacked (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "Gate-discovered work",
		reason:   "the fragment draws the line between reviewer-sourced folds and gate-discovered work, so the default below has something to apply to (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   splicedFragment("land-pass-order-gate-default-"),
		from:     "Gate-discovered work",
		to:       "/tmp/decisions.md",
		reason:   "the gate-default twin is spliced between the gate-discovered definition and the fix-declaration requirement, so the distinction precedes the default, which precedes the declaration (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "/tmp/decisions.md",
		reason:   "an inline fix for gate-discovered work is declared in the run's decisions record (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "outcome note",
		reason:   "an inline fix for gate-discovered work is declared in the outcome note (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "files touched",
		reason:   "the declaration names the files touched, so a human or a later delta gate sees post-review work without diffing the branch (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "one-line why",
		reason:   "the declaration carries a one-line why (issue #3245).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "A fold answering a finding about a comment or doc line rewrites that line in place",
		reason:   "without it the land pass answers a prose finding with a sentence appended beside the line, as PR #3499 did (issue #3506).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "stays proportional to the change it documents",
		reason:   "a prose fold stays proportional to the change it documents (issue #3506).",
	},
	{
		fragment: "land-pass-order-orchestrator.md",
		clause:   "A prose fold owes no declaration",
		reason:   "a prose fold needs no gate-discovered-work declaration (issue #3506).",
	},

	// land-pass-order-gate-default-filer.md and land-pass-order-gate-default-no-filer.md
	{
		fragment: "land-pass-order-gate-default-filer.md",
		clause:   "file, don't fix",
		reason:   "a gate failure proven pre-existing defaults to filing, not fixing inline with no trace of the decision (issue #3245).",
	},
	{
		fragment: "land-pass-order-gate-default-filer.md",
		clause:   "pre-existing",
		reason:   "the file-don't-fix default applies to a failure proven pre-existing on the base (issue #3245).",
	},
	{
		fragment: "land-pass-order-gate-default-filer.md",
		clause:   "FILE ISSUES",
		reason:   "with a Filer the pre-existing failure is filed through the FILE ISSUES step (issues #3245, #5300).",
	},
	{
		fragment: "land-pass-order-gate-default-filer.md",
		clause:   "outcome note",
		reason:   "the pre-existing failure is reported plainly in the outcome note (issue #3245).",
	},
	{
		fragment: "land-pass-order-gate-default-no-filer.md",
		clause:   "report, don't fix",
		reason:   "with no Filer the default is report, don't fix (issue #5300).",
	},
	{
		fragment: "land-pass-order-gate-default-no-filer.md",
		clause:   "pre-existing",
		reason:   "the no-Filer twin keeps the pre-existing proof (issue #5300).",
	},
	{
		fragment: "land-pass-order-gate-default-no-filer.md",
		clause:   "outcome note",
		reason:   "with no Filer the report goes to the outcome note (issue #5300).",
	},
	{
		fragment: "land-pass-order-gate-default-no-filer.md",
		clause:   "PR body",
		reason:   "with no Filer the report also goes to the PR body (issue #5300).",
	},
	{
		fragment: "land-pass-order-gate-default-no-filer.md",
		clause:   "leave the change out of the landing",
		reason:   "the no-Filer twin keeps the leave-it-out rule (issue #5300).",
	},

	// land-pass-order-finish-filer.md and land-pass-order-finish-no-filer.md
	{
		fragment: "land-pass-order-finish-filer.md",
		clause:   "4. Finish the pass: FILE ISSUES, LAND THE CHANGE, OUTCOME.",
		reason:   "step 4 names FILE ISSUES only when a Filer is provisioned, so each finish twin carries its own step order (issue #5300).",
	},
	{
		fragment: "land-pass-order-finish-no-filer.md",
		clause:   "4. Finish the pass: LAND THE CHANGE, OUTCOME.",
		reason:   "step 4 names FILE ISSUES only when a Filer is provisioned, so each finish twin carries its own step order (issue #5300).",
	},

	// caveman-default.md
	{
		fragment: "caveman-default.md",
		clause:   "Code, commands, error messages, and commit messages are exempt and stay verbatim",
		reason:   "code, commands, error messages, and commit messages stay exempt from compression (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "commit messages are always full human-quality prose",
		reason:   "commit messages are always full human-quality prose (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "Never route these through `/caveman`",
		reason:   "marker grammar routed through /caveman is forbidden (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the `note=` field of the SPINDRIFT_OUTCOME line is exempt",
		reason:   "the note= field is human-quality prose, exempt like a commit message (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "The payload those signals carry is exempt on that same tier, whichever carrier takes it",
		reason:   "a signal payload is exempt whichever carrier takes it (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the draft PR's title and body, and a filed issue's title and body, stay full human-quality prose",
		reason:   "PR and filed-issue titles and bodies stay full human-quality prose (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "as the text you hand `driver-exec signal pr-intent` or `driver-exec signal issue-intent` under the `socket` carrier, where no marker line renders at all",
		reason:   "the socket-carrier payload is named, since no marker line renders under it (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "Where a marker line is the carrier, it must keep its required shape exactly intact",
		reason:   "the shape requirement is scoped to marker lines, not every exemption (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the outcome line's key=value pairs",
		reason:   "the outcome line's key=value pairs must stay intact (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the PR-intent line's nonce and base64 payload as one unbroken token",
		reason:   "the PR-intent line's nonce and base64 payload must stay one unbroken token (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "never reworded, reflowed, or line-wrapped",
		reason:   "exempted marker lines are never reworded, reflowed, or line-wrapped (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "The verdict line must additionally remain the first line of the agent's final message",
		reason:   "the verdict line must stay the first line of the final message (issue #2710).",
	},

	// caveman-default-review.md
	{
		fragment: "caveman-default-review.md",
		clause:   "every finding written under `## Blocking` or `## Non-blocking`",
		reason:   "Blocking and Non-blocking findings stay exempt on the review-only tier (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "The `## Probed (APPROVE only)` lines are exempt on the same tier",
		reason:   "Probed lines are exempt on the same tier (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "but text a human reads cold",
		reason:   "the Probed exemption rationale names text a human reads cold (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "compressed, the Probed section stops being evidence",
		reason:   "the Probed exemption rationale says a compressed section stops being evidence (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "Write those lines in full human-quality prose too",
		reason:   "the reviewer must get the full-prose directive, not just its rationale, which alone would survive a paragraph that inverts the order (issue #3265).",
	},

	// filer-file-relay.md
	{
		fragment: "filer-file-relay.md",
		clause:   "'{\"title\":\"...\",\"body\":\"...\",\"dedupTerms\":[\"path/to/file.go:Symbol\"]}'",
		reason:   "the untyped payload's dedupTerms example keeps the path/to/file.go:Symbol site key, the one concrete shape a filer must match (issue #4108).",
	},
	{
		fragment: "filer-file-relay.md",
		clause:   "'{\"title\":\"...\",\"body\":\"...\",\"dedupTerms\":[\"path/to/file.go:Symbol\"],\"type\":\"bug\"}'",
		reason:   "the typed payload's dedupTerms example keeps the path/to/file.go:Symbol site key, the one concrete shape a filer must match (issue #4108).",
	},

	// filer-file-relay-socket.md
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "the filer, who runs the send, is told to run it bare so a pipe cannot mask a rejection (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "driver-exec signal issue-intent -title '<title>' -type",
		reason:   "the heredoc example opens with the title and -type flags (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "driver-exec signal issue-intent -title '<title>' -type bug -dedup 'path/to/file.go:Symbol' <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the heredoc example carries -dedup, the one concrete site-key shape a filer must match (issue #3866).",
	},

	// if-blocked-pr-outbox-socket.md
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "If it never exits zero, even after you fixed each rejection's reason and sent it again — here or before you reached IF BLOCKED — stop resending and go on to step 5, naming the rejection in `note=` alongside what's done and what remains.",
		reason:   "only the IF BLOCKED step names a step 5 for the resend loop to exit to; the open step's exit is the OUTCOME fragment (issue #6256).",
	},

	// outcome-ready-means-outbox-socket.md
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "no intent is queued: follow IF BLOCKED below instead, which ends in `status=blocked` with a `note=` naming the rejection.",
		reason:   "a send that never exited zero must route to IF BLOCKED rather than claim ready (issue #3866).",
	},
}

// sharedClause is a clause contract several fragments must each satisfy
// verbatim: listing it once keeps the twins from drifting apart in this table.
type sharedClause struct {
	fragments []string
	clauseContract
}

// sharedClauses expands into one contract per fragment (generatedContracts).
var sharedClauses = []sharedClause{
	{
		fragments: []string{"file-issues-relay.md", "file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "the issue number, and the branch for provenance.",
			reason: "the read-only relay carriers offer the branch alone, since only the direct carrier can hold a PR URL; the sentence's head is held equal across the filer variants by twinContracts (issues #3826, #3827, #6184).",
		},
	},
	{
		fragments: []string{"file-issues-direct.md", "file-issues-relay.md", "file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: splicedFragment("file-issues-triage-recap"),
			to:     "It is pre-provisioned",
			reason: "the triage recap lives once in file-issues-triage-recap.md and every filer variant splices it ahead of the provisioning sentence, so a variant that drops the splice files without triaging (issues #2701, #6184).",
		},
	},
	{
		fragments: []string{"land-pass-order-gate-default-filer.md", "land-pass-order-gate-default-no-filer.md"},
		clauseContract: clauseContract{
			clause: "A tree the reviewer approved is what lands; a pre-existing failure belongs to whichever branch introduced it, not to this one to absorb silently.",
			reason: "the gate-default twins differ in how a pre-existing failure is reported but share the closing rule on what lands, so each must say it alike (issue #5300).",
		},
	},
	{
		fragments: []string{"land-pass-order-gate-default-no-filer.md", "land-pass-order-finish-no-filer.md"},
		clauseContract: clauseContract{
			clause: "FILE ISSUES",
			absent: true,
			reason: "the twin renders only when no Filer is provisioned, so it must not name the FILE ISSUES step (issue #5300).",
		},
	},
	{
		fragments: []string{"filer-file-relay.md", "filer-file-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "(a line key from step 2 still goes stale once the code around it moves)",
			reason: "the carrier prose must not imply any key it is handed is durable (issue #3812).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "no `-body` flag",
			reason: "the send has no -body flag; the body goes on stdin or -body-file (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "`bug`, `enhancement`, `chore`",
			reason: "the three -type values are named (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "title with backticks needs single quotes, not double",
			reason: "a title with backticks must be single-quoted, or the shell runs it as a command substitution (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "title containing a single quote needs `'\\''` in its place",
			reason: "a title with an apostrophe needs the '\\'' escape (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "exit code is the acceptance",
			reason: "the exit code is the acceptance of the send (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "-body-file",
			absent: true,
			to:     "<<'SPINDRIFT_SIGNAL_EOF'",
			reason: "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "-body-file",
			from:   "<<'SPINDRIFT_SIGNAL_EOF'",
			reason: "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
		},
	},
	{
		fragments: []string{"filer-file-relay-socket.md", "open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
			verbatim: true,
			reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
		},
	},
	{
		fragments: []string{"file-issues-relay-socket.md", "research-file-issues-relay-socket.md", "butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
			reason: "the relay summaries stay third-person about the filer's bare send, since the reader never runs it (issues #3866, #3909).",
		},
	},
	{
		fragments: []string{"butler-file-issues-relay.md"},
		clauseContract: clauseContract{
			clause: "(`path/to/file.go:Symbol`, where Symbol is the symbol it names or else the nearest enclosing one, or `path/to/file.go:<line>` only when no symbol encloses the site)",
			reason: "the dedup key is the nearest enclosing symbol, a bare line only when none encloses; the log twin is pinned here and twinContracts holds the socket twin equal to it (issue #3812).",
		},
	},
	{
		fragments: []string{"butler-file-issues-relay.md"},
		clauseContract: clauseContract{
			clause: "A Markdown site has no symbol: key it `path/to/file.md:<anchor>`, the anchor of the nearest heading above it (lowercased, punctuation dropped but hyphens and underscores kept, then each run of spaces and hyphens folded to one hyphen, a repeated heading taking a `-1`, `-2` suffix: `#### The pi Driver` is `the-pi-driver`), never a line number or a free word. Text above a file's first heading keys on the bare path `path/to/file.md`.",
			reason: "a Markdown site keys on its heading anchor, never a line number; the log twin is pinned here and twinContracts holds the socket twin equal to it (issue #3812).",
		},
	},
	{
		fragments: []string{"butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "If the filer reports its class was rejected as off the class list, re-delegate the finding with a class from the valid ones the rejection names (see FINDING CLASSES above) — never drop it for that.",
			reason: "a class-rejected finding is re-delegated with a valid class, never dropped (issue #4766); the socket twin is pinned here, since the log carrier has no class check that can reject, and twinContracts keeps the log twin in step.",
		},
	},
	{
		fragments: []string{"butler-file-issues-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "On any other failure (the filer errors, times out, or returns nothing usable), drop the finding",
			reason: "every other failure still drops the finding, so the class exemption stays narrow (issue #4766).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md"},
		clauseContract: clauseContract{
			clause: "driver-exec signal pr-intent -title '<conventional title>' <<'SPINDRIFT_SIGNAL_EOF'",
			reason: "the heredoc opens with the title flag and no -body-file, a one-call stdin send (issue #3866).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md"},
		clauseContract: clauseContract{
			clause: "There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line.",
			reason: "the send has no -nonce flag (issue #3866).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
			reason: "a piped send hides a rejection behind the last command's exit code (issue #3866).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "The command's exit code is the acceptance",
			reason: "the exit code is the acceptance of the send (issue #3866).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md"},
		clauseContract: clauseContract{
			clause: "Sending the command again replaces the earlier intent rather than racing it",
			reason: "a corrected resend replaces the earlier intent (issue #3866).",
		},
	},
	{
		fragments: []string{"open-pr-create-outbox-socket.md", "if-blocked-pr-outbox-socket.md"},
		clauseContract: clauseContract{
			clause: "A title containing a single quote needs `'\\''` in its place",
			reason: "a title with an apostrophe needs the '\\'' escape (issue #3866).",
		},
	},
	{
		fragments: []string{"research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "driver-exec signal comment <<'SPINDRIFT_SIGNAL_EOF'",
			reason: "driver-exec signal comment is the verb, with the verdict body as a stdin heredoc (issue #3866).",
		},
	},
	{
		fragments: []string{"research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "The only flag is `-body-file` (optional)",
			reason: "the only flag is -body-file (issue #3866).",
		},
	},
	{
		fragments: []string{"research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: fmt.Sprintf("a hard ceiling of %d bytes per field", signalwire.MaxBodyBytes),
			reason: "the socket enforces signalwire.MaxBodyBytes per field, and the fragment must name it (issue #3726).",
		},
	},
	{
		fragments: []string{"research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md"},
		clauseContract: clauseContract{
			clause: "A body over that ceiling is refused outright, never truncated",
			reason: "an oversize body is refused, never truncated, so the agent must not expect half a verdict to post (issue #3726).",
		},
	},
}

// landPassForbiddenMarkers are the code-out actions the land-pass-order
// fragment must never carry: the land pass is reachable on a read-only Box,
// where any of them would be an ungated code-out action. `gh api` is listed
// although its read-only uses are legitimate (issue #3214).
var landPassForbiddenMarkers = []string{
	"git push",
	"git bundle create",
	"gh pr create",
	"gh pr ready",
	"gh pr merge",
	"gh issue comment",
	"gh issue create",
	"gh api",
}

// bareEOFDelimiter matches a heredoc opened with a bare EOF delimiter in any
// spelling (quoted, dash-stripped, spaced); the trailing \b keeps
// <<'SPINDRIFT_SIGNAL_EOF' from matching.
var bareEOFDelimiter = regexp.MustCompile(`<<-?\s*['"]?EOF\b`)

// bareEOFFragments are the fragments that instruct a heredoc send.
var bareEOFFragments = []string{
	"filer-file-relay-socket.md",
	"open-pr-create-outbox-socket.md",
	"if-blocked-pr-outbox-socket.md",
	"research-verdict-relay-socket.md",
	"local/research-verdict-relay-socket.md",
}

// generatedContracts expands the contracts shared by several fragments.
func generatedContracts() []clauseContract {
	var out []clauseContract
	for _, s := range sharedClauses {
		for _, fragment := range s.fragments {
			c := s.clauseContract
			c.fragment = fragment
			out = append(out, c)
		}
	}
	for _, marker := range landPassForbiddenMarkers {
		out = append(out, clauseContract{
			fragment: "land-pass-order-orchestrator.md",
			clause:   marker,
			absent:   true,
			verbatim: true,
			reason:   "a code-out action must not appear in a fragment a read-only Box can reach (issue #3214).",
		})
	}
	for _, fragment := range bareEOFFragments {
		out = append(out, clauseContract{
			fragment: fragment,
			pattern:  bareEOFDelimiter,
			absent:   true,
			reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
		})
	}
	return out
}

// goldenRenders assembles every golden cell once and returns the fragment
// text each rendered.
func goldenRenders(t *testing.T) renderedFragments {
	t.Helper()
	renders := renderedFragments{}
	for _, cell := range goldenCells() {
		c := defaultCell()
		cell.setup(c)
		result, _ := assembleCell(t, c, t.TempDir())
		renders[cell.name] = promptassembly.FragmentRenders(result)
	}
	return renders
}

// TestClauseContracts checks each contract against the fragment text the
// golden cells actually rendered.
func TestClauseContracts(t *testing.T) {
	renders := goldenRenders(t)

	for _, c := range append(slices.Clone(clauseContracts), generatedContracts()...) {
		t.Run(c.fragment+"/"+c.what(), func(t *testing.T) {
			if err := checkClauseContract(c, renders); err != nil {
				t.Error(err)
			}
		})
	}
}

// relaySendParagraphMarker opens the send paragraph the relay-socket
// summaries share. relaySendParagraphEnd is the opening words of the paragraph
// after it, which all three summaries begin alike; ending the compared span
// there (inclusive) catches text appended to the send paragraph in only one.
const (
	relaySendParagraphMarker = "The filer sends each in one call"
	relaySendParagraphEnd    = "Best-effort: filing must never block"
)

// The filer variants share one head, so their twin contracts share its scope
// and reason.
const (
	fileIssuesHeadFrom    = "# FILE ISSUES"
	fileIssuesHeadThrough = "the issue number, and the branch"
	fileIssuesHeadReason  = "the filer variants share a head (the heading, the triage-recap splice, and the provisioning sentence up to its provenance clause); the provenance clause is the first divergence, since only the direct carrier can hold a PR URL (issues #3826, #3827)."
)

// twinContracts binds the hand-mirrored fragment pairs to each other. Text is
// compared after whitespace normalization (the old tests that compared bytes
// let no re-wrap pass; a re-wrap is harmless to the model), so a one-sided
// re-wrap passes while any one-sided wording change fails.
var twinContracts = []twinContract{
	{
		a:       "file-issues-relay-socket.md",
		b:       "research-file-issues-relay-socket.md",
		from:    relaySendParagraphMarker,
		through: relaySendParagraphEnd,
		reason:  "the relay-socket summaries intentionally duplicate the send paragraph, so a wording fix applied to only one drifts silently (issues #3866, #3909).",
	},
	{
		a:       "file-issues-relay-socket.md",
		b:       "butler-file-issues-relay-socket.md",
		from:    relaySendParagraphMarker,
		through: relaySendParagraphEnd,
		editB: []twinEdit{{
			old:   "`-body-file`, `-class`, `-concurrence`, and `-patch-file` (the\nlast two only when the finding has one)",
			new:   "and `-body-file`",
			count: 1,
		}},
		reason: "the butler's send paragraph differs only in its flag clause, because `-class`, `-concurrence` and `-patch-file` are butler-only (cmd/launcher/driver-exec/signal_cmd.go, issues #3880, #3909).",
	},
	{
		a:       "open-pr-create-outbox-socket.md",
		b:       "if-blocked-pr-outbox-socket.md",
		from:    "The closing `SPINDRIFT_SIGNAL_EOF`",
		through: "never the run.",
		endsA:   true,
		reason:  "both fragments intentionally duplicate the span from the closing-delimiter sentence through \"never the run.\", so a wording fix applied to one drifts silently; the IF BLOCKED fragment appends its own exit after the span (issues #3866, #6256), so the open fragment must still end at it.",
	},
	{
		a:      "research-verdict-relay-socket.md",
		b:      "local/research-verdict-relay-socket.md",
		from:   "The closing `SPINDRIFT_SIGNAL_EOF`",
		reason: "the shared root file and the local one intentionally duplicate everything from the closing-delimiter sentence onward, so a wording fix applied to one drifts silently (issue #3866).",
	},
	{
		a:      "review-loop-orchestrator-filer.md",
		b:      "review-loop-orchestrator-no-filer.md",
		editA:  []twinEdit{{old: "FILE ISSUES, ", new: ""}},
		reason: "the Filer and no-Filer review-loop twins differ only in the FILE ISSUES mentions; a rewording must land in both (issue #5300).",
	},
	{
		a:       "file-issues-direct.md",
		b:       "file-issues-relay.md",
		from:    fileIssuesHeadFrom,
		through: fileIssuesHeadThrough,
		reason:  fileIssuesHeadReason,
	},
	{
		a:       "file-issues-direct.md",
		b:       "file-issues-relay-socket.md",
		from:    fileIssuesHeadFrom,
		through: fileIssuesHeadThrough,
		reason:  fileIssuesHeadReason,
	},
}

// relayTwinMechanism are the two mechanism clauses the log and socket relay
// twins differ in.
const (
	relayLogMechanism    = "It emits `SPINDRIFT_ISSUE_INTENT` lines instead of filing directly"
	relaySocketMechanism = "It sends each one over the Signal socket via `driver-exec signal issue-intent` instead of filing directly"
)

// generatedTwinContracts expands the log/socket relay twins, which share one
// edit shape.
func generatedTwinContracts() []twinContract {
	var out []twinContract
	for _, c := range []struct{ name, reason string }{
		{"butler-file-issues-relay", "the carrier is chosen per dispatch and fragments share no include, so the butler relay's prose is mirrored by hand (issue #3927)."},
		{"research-file-issues-relay", "the carrier is chosen per dispatch and fragments share no include, so the research relay's prose is mirrored by hand (issue #3947)."},
	} {
		out = append(out, twinContract{
			a:      c.name + ".md",
			b:      c.name + "-socket.md",
			cutB:   relaySendParagraphMarker,
			editA:  []twinEdit{{old: relayLogMechanism, new: "<mechanism clause>", count: 1}},
			editB:  []twinEdit{{old: relaySocketMechanism, new: "<mechanism clause>", count: 1}},
			reason: c.reason + " Only the mechanism clause and the socket twin's send paragraph (pinned against the other relay summaries) differ.",
		})
	}
	return out
}

// TestTwinContracts checks every twin pair against the cells' rendered text.
func TestTwinContracts(t *testing.T) {
	renders := goldenRenders(t)
	for _, c := range append(slices.Clone(twinContracts), generatedTwinContracts()...) {
		t.Run(c.a+"~"+c.b, func(t *testing.T) {
			if err := checkTwinContract(c, renders); err != nil {
				t.Error(err)
			}
		})
	}
}
