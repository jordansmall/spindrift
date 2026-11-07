//go:build integration

package promptassembly_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/signalwire"
)

// clauseContracts binds spindrift's own prompt fragments and, through the
// nixRenderedSources pseudo-cell, its Nix-rendered templates; a Consumer override
// of a fragment keeps free wording. A clause several fragments carry alike is
// in sharedClauses, and one a hand-mirrored pair must keep in step is also
// compared in twinContracts, which pins the pair equal where this table pins
// only what each must say.
var clauseContracts = []clauseContract{
	// scout-prompt.md (a base template, raw source in the nix-rendered-sources pseudo-cell)
	{
		fragment: "scout-prompt.md",
		clause:   "cited verbatim excerpt",
		reason:   "the brief must cite a verbatim excerpt for each load-bearing claim so the coordinator can verify a claim from the cited lines instead of re-exploring the tree (issue #3216).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "load-bearing claim",
		reason:   "the cited-excerpt rule is scoped to load-bearing claims, so the scout does not quote every line it reads (issue #3216).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "path:line anchor",
		reason:   "each excerpt sits under a path:line anchor, the sed range the pre-return diff check reproduces (issue #3216).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "/tmp/brief.md",
		reason:   "the brief is written to disk at a fixed path the coordinator and worker read back, not retyped into the return message (issue #3449).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "write a structured brief to",
		reason:   "the scout writes the brief file itself rather than returning it for the coordinator to persist (issue #3449).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "Never retype a cited excerpt",
		reason:   "excerpts are appended straight from source so they are byte-identical to the file; a retyped excerpt can drift from the tree (issue #3449).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "verify every",
		reason:   "the scout verifies each cited excerpt against its source before returning, so a drifted block is fixed rather than shipped (issue #3449).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "`diff` it against the excerpt block the brief",
		reason:   "the verification is a block-by-block diff of the anchor's own range, not a per-line grep, which would confirm only that the text exists somewhere (issue #3449).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "caveman",
		absent:   true,
		reason:   "the scout is a roster subagent with no Skill tool under the claude Driver, so its template carries no caveman anchor whether or not the skill is baked (issue #4562).",
	},
	{
		fragment: "scout-prompt.md",
		clause:   "${CAVEMAN",
		absent:   true,
		reason:   "no caveman fragment may be spliced into the scout template: a CAVEMAN_STEP var there would name a skill the subagent cannot invoke (issue #4562).",
	},
	// worker-prompt.md (a base template, raw source in the nix-rendered-sources pseudo-cell)
	{
		fragment: "worker-prompt.md",
		clause:   "caveman",
		absent:   true,
		reason:   "the worker's only output is a final report, not narration, so its template carries no caveman anchor whether or not the skill is baked (issue #4562).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "${CAVEMAN",
		absent:   true,
		reason:   "no caveman fragment may be spliced into the worker template: its only output is a final report (issue #4562).",
	},
	{
		fragment: "worker-prompt.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_OUTCOME|VERDICT:`),
		absent:   true,
		reason:   "the worker's prompt is its template plus the spliced fragments, and the template may carry no outcome or verdict grammar, so a stray marker from a misbehaving worker can never terminate the run or be read as a review result; the template's own ${...} placeholders are raw source, so the unsubstituted-token check covers only the spliced fragments (issues #2059, #2491, #6182).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "turn budget",
		reason:   "the worker must know its delegation may state a turn budget, or it cannot stop cleanly at one (issue #3159).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "stop cleanly",
		reason:   "nearing its budget the worker stops with a checkpoint rather than pushing past it, since a long-running worker replays its whole context each turn (issue #3159).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "remaining-work checkpoint",
		reason:   "the clean stop reports the work still left and where it stopped, which the coordinator seeds into a fresh worker (issue #3159).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "fresh worker",
		reason:   "the checkpoint must be detailed enough for a fresh worker to resume without re-deriving anything (issue #3159).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "one patch file",
		reason:   "a fully determined group of changes is written into one patch file, because every command replays the whole context (issue #3420).",
	},
	{
		fragment: "worker-prompt.md",
		clause:   "apply it in a single command",
		reason:   "the patch lands in one command and is verified once for the group, so eight changes cost about what one costs (issue #3420).",
	},
	// worker-scout-brief.md
	{
		fragment: "worker-scout-brief.md",
		clause:   "work from that excerpt first",
		reason:   "the worker works from the delegation's quoted excerpt first, so the scout's map reaches it without a re-exploration (issues #3157, #3419).",
	},
	{
		fragment: "worker-scout-brief.md",
		clause:   "Open the full brief at `/tmp/brief.md` only when the excerpt is",
		reason:   "reading the whole brief is conditional on the excerpt being missing, wrong, or silent, not the worker's first move (issue #3419).",
	},
	// tdd-baked.md
	// tdd-unbaked.md
	// principle-laziness-protocol-default.md
	{
		fragment: "principle-laziness-protocol-default.md",
		clause:   "`/principle-laziness-protocol`",
		reason:   "the anchor names the skill the implementer invokes before writing the change; the worker splices it too (issue #6182).",
	},
	// principle-redesign-from-first-principles-default.md
	{
		fragment: "principle-redesign-from-first-principles-default.md",
		clause:   "`/principle-redesign-from-first-principles`",
		reason:   "the anchor names the skill the implementer invokes when integrating a new requirement; the worker splices it too (issue #6182).",
	},
	// scout-delegate.md
	{
		fragment: "scout-delegate.md",
		clause:   "/tmp/brief.md",
		reason:   "the coordinator must be told where the scout's brief lands (issue #3157).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "The scout writes its brief itself, to `/tmp/brief.md`",
		reason:   "the scout writes the brief; the session reads it back from disk and never persists or appends to it (issue #3449).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "read it back from disk before you start work",
		reason:   "the brief is read from disk before work starts, not expected in the scout's return message (issue #3163).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "> scout: map the files, seams, and existing tests relevant to this issue.",
		reason:   "the delegation is the request alone; scout-prompt.md owns the citation, brief-location, pointer and no-implement rules (issue #6183).",
	},
	{
		fragment: "scout-delegate.md",
		pattern:  regexp.MustCompile(`Write the brief to|pointer to it|cited with a verbatim excerpt|Do not implement`),
		absent:   true,
		reason:   "the delegation must not restate the scout persona's rules, which scout-prompt.md owns (issue #6183).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "Persist what it returns",
		absent:   true,
		reason:   "the scout writes the brief itself, so the coordinator is no longer told to persist what the scout returns (issue #3449).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "If `/tmp/brief.md` isn't there, explore the repo yourself as usual",
		reason:   "COORDINATOR_SCOUT_BRIEF needs a worker too, so in a scout-only cell this fragment is the only one that can carry the brief-absent degradation (issue #3449).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "Re-search only when a citation is wrong",
		reason:   "the coordinator re-searches only when a citation itself is wrong or missing, not on any wrong pointer (issue #3216).",
	},
	{
		fragment: "scout-delegate.md",
		clause:   "before you delegate any slice",
		absent:   true,
		reason:   "SCOUT_PROVISIONED renders without a worker, so the wording must not assert a delegation step a scout-only roster never runs (issue #3163).",
	},
	{
		fragment: "scout-delegate.md",
		pattern:  regexp.MustCompile(`(?i)worker`),
		absent:   true,
		reason:   "SCOUT_PROVISIONED renders without a worker, so the section must not reference one (issue #3163).",
	},
	// scout-absent.md
	{
		fragment: "scout-absent.md",
		clause:   "Scout the issue yourself before implementing",
		reason:   "the section leads with the work, so the lead directive is pinned and not only the trailing absence clause (issue #3168).",
	},
	{
		fragment: "scout-absent.md",
		clause:   "there is no brief waiting on disk",
		reason:   "with no scout there is no brief to read, and the fragment must say so (issue #3157).",
	},
	{
		fragment: "scout-absent.md",
		clause:   "/tmp/brief.md",
		absent:   true,
		reason:   "with no scout no brief was written, so the fragment must not point at the brief path (issue #3157).",
	},
	// coordinator.md
	{
		fragment: "coordinator.md",
		clause:   "run IMPLEMENT as its **coordinator**",
		reason:   "a provisioned worker makes the main session the coordinator rather than the implementer (issue #2056).",
	},
	{
		fragment: "coordinator.md",
		pattern:  regexp.MustCompile(`the brief's relevant pointers|Use the scout brief`),
		absent:   true,
		reason:   "coordinator.md is scout-neutral; the slice-from-the-brief instruction lives in coordinator-scout-brief.md (issue #3157).",
	},
	{
		fragment: "coordinator.md",
		clause:   "stays bounded",
		reason:   "the coordinator sizes slices so each worker run stays bounded, on every roster with a worker, scout or not (issue #3159).",
	},
	{
		fragment: "coordinator.md",
		clause:   "worker: implement <one slice, with its relevant pointers>.",
		reason:   "the delegation template carries inputs only; the test-first rule and report shape are owned by worker-prompt.md (issue #6183).",
	},
	{
		fragment: "coordinator.md",
		clause:   "Turn budget for this slice: about <N> turns.",
		reason:   "each delegation states the turn budget, in turns, so the worker can stop cleanly at it (issue #3159).",
	},
	{
		fragment: "coordinator.md",
		clause:   "**fresh** worker seeded from that checkpoint",
		reason:   "a worker that stopped at its budget is replaced by a fresh one seeded from its checkpoint; bare \"fresh\" matches unrelated prose (issue #3159).",
	},
	{
		fragment: "coordinator.md",
		clause:   "Each delegated slice must satisfy the one-slice, test-first discipline below",
		reason:   "the test-first rule is enforced by the coordinator but performed by the worker, so the delegation points at it instead of restating it (issue #6183).",
	},
	{
		fragment: "coordinator.md",
		pattern:  regexp.MustCompile(`Work test-first\. (?:> )?Turn|Return (?:> )?a concise report|not (?:> )?the diffs`),
		absent:   true,
		reason:   "the delegation template must not restate persona rules (test-first, report shape) that worker-prompt.md owns (issue #6183).",
	},
	// coordinator-scout-brief.md
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "break the issue into the ordered set of slices",
		reason:   "coordinator.md dropped the slice-from-the-brief instruction, so a scout-present run loses it unless this fragment states it (issue #3157).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "verbatim from the brief",
		reason:   "each delegation quotes the scout's own lines, not a paraphrase, so the worker starts from the map (issue #3158).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "never paste the whole brief into a delegation",
		reason:   "the quoted excerpt is scoped to the slice (issue #3158).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "read `/tmp/brief.md` first",
		absent:   true,
		reason:   "the worker reads the whole brief only conditionally, via worker-scout-brief.md, never as a coordinator-imposed first step (issue #3419).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "spot-check a citation",
		reason:   "the coordinator verifies claims from the cited excerpts and reads the tree only to spot-check a citation (issue #3216).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "standing sweep",
		reason:   "reading the tree is never a standing sweep over ground the brief already covers (issue #3216).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "the same evidence your delegations carry",
		reason:   "the cited excerpts the coordinator verifies from are the same evidence its delegations carry (issue #3216).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "You already persisted",
		absent:   true,
		reason:   "the scout writes the brief itself, so the coordinator must not claim to have persisted it (issue #3449).",
	},
	{
		fragment: "coordinator-scout-brief.md",
		clause:   "The scout wrote its brief to `/tmp/brief.md` itself",
		reason:   "the coordinator is told the scout wrote the brief and that it reads it back from disk (issue #3449).",
	},
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
		clause:   "Default to the `/caveman` skill for all narration",
		reason:   "a baked caveman skill is the default register for narration, injected into the shared COMMS block (issues #455, #487).",
	},
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

	// scout-prompt.md, review-prompt.md, conflict-resolve-prompt.md
	{
		fragment: "scout-prompt.md",
		clause:   "Do not narrate between tool calls",
		reason:   "the scout's only output is its final pointer message, so mid-turn narration is wasted output.",
	},
	{
		fragment: "review-prompt.md",
		clause:   "Do not narrate between tool calls",
		reason:   "the reviewer's only output is its final verdict, so mid-turn narration is wasted output.",
	},
	{
		fragment: "conflict-resolve-prompt.md",
		clause:   "Do not narrate between tool calls",
		reason:   "the conflict resolver's only output is one short final message, so mid-turn narration is wasted output.",
	},
	{
		fragment: "review-prompt.md",
		clause:   "at one line",
		reason:   "a reviewer finding is capped at one line, so a verdict stays scannable and the host can file it as one title.",
	},
	{
		fragment: "review-prompt.md",
		clause:   "git fetch origin",
		reason:   "the implementor rebases against origin/BASE_BRANCH, so the reviewer must diff against the same freshly fetched ref, not a possibly stale local one.",
	},
	{
		fragment: "review-prompt.md",
		clause:   "git diff origin/${BASE_BRANCH}...HEAD",
		reason:   "the reviewer diffs against the fetched origin ref the implementor rebased onto, not a local branch.",
	},
	{
		fragment: "review-prompt.md",
		clause:   "git log origin/${BASE_BRANCH}..HEAD",
		reason:   "the reviewer lists commits against the fetched origin ref the implementor rebased onto, not a local branch.",
	},
	{
		fragment: "conflict-resolve-prompt.md",
		clause:   "\"DO NOT EDIT\" notice",
		reason:   "a conflicted file that declares itself generated must be recognized by its header, and the wording stays repo-agnostic (issue #403).",
	},
	{
		fragment: "conflict-resolve-prompt.md",
		clause:   "never hand-merge the conflict markers",
		reason:   "a generated file's conflict is never hand-merged (issue #403).",
	},
	{
		fragment: "conflict-resolve-prompt.md",
		clause:   "Resolve the conflict in its source of truth instead",
		reason:   "a generated file is resolved in its source of truth, wording kept repo-agnostic (issue #403).",
	},
	{
		fragment: "conflict-resolve-prompt.md",
		clause:   "regenerate the artifact",
		reason:   "after resolving the source of truth the artifact is regenerated, not merged (issue #403).",
	},

	// issue-prompt.md: the pre-implement gates
	{
		fragment: "issue-prompt.md",
		clause:   "OPEN A PULL REQUEST",
		reason:   "the launcher gates on CI green itself (issue #1651), so the Driver's OPEN A PULL REQUEST step stops at opening the draft (issue #1653).",
	},
	{
		fragment: "issue-prompt.md",
		pattern:  regexp.MustCompile(`(?m)^# WATCH CI$`),
		verbatim: true,
		absent:   true,
		reason:   "the launcher already gates on CI green before flipping the PR ready and merging (issue #1651), so the Driver polling for CI registration too is a redundant LLM step (issue #1653).",
	},

	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "Compare the issue's title against its body",
		reason:   coherenceReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "merely elaborates, restates, or adds acceptance criteria to the title's own topic is exempt and must pass through",
		reason:   "a body that only elaborates on the title's own topic is exempt and must pass through (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "On a genuine, contradictory mismatch, halt immediately",
		reason:   coherenceReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "do not scout, do not open a branch's worth of commits, do not write any diff",
		reason:   coherenceReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "naming both interpretations, what the title implies and what the body implies, and asking a human which one governs",
		reason:   coherenceReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "SPINDRIFT_OUTCOME line's `note=` field below. Post nothing yourself: the launcher posts that note as the issue comment, host-side",
		reason:   "the escalation lives in the note= field; the launcher posts it host-side, never the agent itself (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ambiguous note=<escalation naming both interpretations>",
		reason:   coherenceReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "raw plain text, not wrapped in backticks, a code fence, or any other markdown formatting, nothing after it",
		reason:   "the outcome line is parsed from the final message, so it must be raw plain text with nothing after it (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "`status=ambiguous` is a distinct, successful, non-crash stop — never `status=blocked`",
		reason:   "status=ambiguous is a successful stop, never status=blocked (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ISSUE COHERENCE GATE",
		to:       "# COMMS",
		clause:   "proceed straight into # SCOUT",
		reason:   "a coherent issue passes through the gate to SCOUT with no further comment (issue #3224).",
	},

	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "Run `git fetch origin`, then verify directly against `origin/${BASE_BRANCH}`",
		reason:   "already-resolved is verified against the fetched default branch, not the worker's own seed (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "A partial or speculative match does not qualify",
		reason:   "only a demonstrable match counts as already resolved (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "the existing check or test that proves it named in the note",
		reason:   "the note names the proving check or test (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "no commits, no push, no PR, no in-box comment, no in-box close: the launcher does the rest host-side",
		reason:   "an already-resolved stop makes no commit, push, PR, comment or close; the launcher does the rest host-side (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "Name the resolving commit SHA or PR, and the check or test proving it",
		reason:   "the note names the resolving commit or PR and its proof (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=already-resolved note=",
		reason:   "the stop is a distinct status the launcher parses (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "raw plain text, not wrapped in backticks, a code fence, or any other markdown formatting, nothing after it",
		reason:   "the outcome line is parsed from the final message, so it must be raw plain text with nothing after it (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "`status=already-resolved` is a distinct, successful, non-crash stop — never `status=blocked`",
		reason:   "already-resolved is a successful stop, never status=blocked, mirroring the coherence gate (issue #4015).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# ALREADY RESOLVED",
		to:       "# IMPLEMENT",
		clause:   "If the change is not already present, proceed straight into",
		reason:   "an unresolved issue proceeds to IMPLEMENT with no further comment (issue #4015).",
	},

	// issue-prompt.md: COMMS and CHECK
	{
		fragment: "issue-prompt.md",
		from:     "# COMMS",
		to:       "# SCOUT",
		clause:   "Your text output is a machine-parsed log, not a conversation",
		reason:   commsReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# COMMS",
		to:       "# SCOUT",
		clause:   "no pleasantries, acknowledgements, praise, or apologies; never restate what a subagent returned; no narrative framing. One terse, data-bearing status line per step",
		reason:   commsReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# COMMS",
		to:       "# SCOUT",
		clause:   "Human-quality prose is reserved exclusively for: commit messages (Conventional Commits section), the PR title and body, the issue comment required by IF BLOCKED, and the `note=` field of the SPINDRIFT_OUTCOME line.",
		reason:   "the carve-out keeps commits, the PR body, the IF BLOCKED comment and the outcome note= human prose; scoped to COMMS so dropping the sentence fails even though those parts are named elsewhere.",
	},

	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		clause:   "Run every check or build gate in the foreground and block on it yourself — never background it",
		reason:   backgroundGateReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		clause:   "SPINDRIFT_OUTCOME",
		reason:   backgroundGateReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		clause:   "CHECK_HYGIENE_STEP",
		reason:   "CHECK keeps the obligation and the foreground-gate rule while the elaborated guidance lives in the check-hygiene skill, reached through a bakedness-gated fragment (issue #3220).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		clause:   "until its `SPINDRIFT_OUTCOME` line",
		reason:   "the dispatcher parses SPINDRIFT_OUTCOME, so the terminal-outcome mandate must not depend on the agent having read an on-demand skill body (issue #3220).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		clause:   "told to stop right after COMMIT",
		reason:   "the terminal-outcome mandate is scoped to the owning pass, so the implement and fix passes the orchestrator stops after COMMIT are not ordered to print an outcome (issue #5129).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		pattern:  regexp.MustCompile(`(?i)do not stop this run`),
		absent:   true,
		reason:   checkRestatedReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		pattern:  regexp.MustCompile("(?i)\\(`status=ready` or `status=blocked`\\)"),
		absent:   true,
		reason:   checkRestatedReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		pattern:  regexp.MustCompile(`(?i)vanished`),
		absent:   true,
		reason:   checkRestatedReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# CHECK",
		to:       "# REVIEW",
		pattern:  regexp.MustCompile("(?i)never `cat`"),
		absent:   true,
		reason:   checkRestatedReason,
	},
	{
		fragment: "check-hygiene-default.md",
		clause:   "/check-hygiene",
		reason:   "the CHECK anchor renders from a bakedness-gated fragment, so the section carries the variable and this fragment carries the pointer to the skill (issue #3220).",
	},
	{
		fragment: "fix-prompt.md",
		from:     "# CHECK",
		to:       "# LAND THE CHANGE",
		clause:   "CHECK_HYGIENE_STEP",
		reason:   "the fix pass's CHECK section anchors the check-hygiene skill through the same bakedness-gated fragment as the implement pass (issue #726).",
	},
	{
		fragment: "fix-prompt.md",
		from:     "# CHECK",
		to:       "# LAND THE CHANGE",
		clause:   "NIX_CHECKS_STEP",
		reason:   "the nix-checks lore moved into the skill (issue #3223); what the fix pass still needs here is the route to it.",
	},

	{
		fragment: "fix-prompt.md",
		clause:   "Rewriting the branch's own unmerged history is expected",
		reason:   "the branch's own history may be rewritten under either access mode: a read-only Box never pushes and the host relays the branch (issues #2462, #3225).",
	},
	{
		fragment: "fix-prompt.md",
		clause:   "The branch already force-pushes",
		absent:   true,
		reason:   "the history-rewrite line must not presuppose the Box pushed, since a read-only Box never does (issues #2462, #3225).",
	},
	{
		fragment: "commit-baked.md",
		clause:   "Use the `/commit` skill to write every commit message.",
		reason:   "the baked-skill commit anchor points at the /commit skill, injected into the shared CHECK/COMMIT block (issues #455, #487).",
	},
	{
		fragment: "github/fix-ci-read.md",
		clause:   "gh pr view",
		reason:   "a GitHub fix pass reads CI through gh (issue #1963).",
	},
	{
		fragment: "github/fix-ci-read.md",
		clause:   "fj pr status",
		absent:   true,
		reason:   "fj does not exist against a GitHub remote (issue #1963).",
	},
	{
		fragment: "forgejo/fix-ci-read.md",
		clause:   "fj pr status",
		reason:   "a Forgejo fix pass reads CI through fj (issue #1963).",
	},
	{
		fragment: "forgejo/fix-ci-read.md",
		clause:   "gh pr view",
		absent:   true,
		reason:   "the gh CI commands do not exist against a Forgejo remote (issue #1963).",
	},
	{
		fragment: "open-pr-create-outbox.md",
		clause:   "a line with the nonce missing or wrong is silently dropped",
		reason:   "the read-only log carrier tells the Driver why a wrong nonce loses its PR intent (issues #1938, #1939).",
	},

	// issue-prompt.md and fix-prompt.md: the code-comments policy sits inside the pass that writes code
	{
		fragment: "issue-prompt.md",
		from:     "# IMPLEMENT",
		to:       "# CHECK",
		clause:   "the non-obvious why, a constraint, or a gotcha",
		reason:   codeCommentsReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "CODE_COMMENTS_STEP",
		absent:   true,
		reason:   codeCommentsReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "/code-comments",
		absent:   true,
		reason:   codeCommentsReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "# CODE COMMENTS",
		absent:   true,
		reason:   codeCommentsReason,
	},
	{
		fragment: "fix-prompt.md",
		from:     "# FIX",
		to:       "# COMMS",
		clause:   "the non-obvious why, a constraint, or a gotcha",
		reason:   codeCommentsReason,
	},
	{
		fragment: "fix-prompt.md",
		clause:   "CODE_COMMENTS_STEP",
		absent:   true,
		reason:   codeCommentsReason,
	},
	{
		fragment: "fix-prompt.md",
		clause:   "/code-comments",
		absent:   true,
		reason:   codeCommentsReason,
	},

	// nix-checks-skill.md
	{
		fragment: "nix-checks-skill.md",
		to:       "flake.nix",
		clause:   "git add",
		reason:   "nix flakes only evaluate git-tracked files, so an agent that runs `nix build` before staging a new file burns a checks cycle; the guidance must land before the flake.nix paragraph so it primes every nix invocation (issues #714, #3223).",
	},
	{
		fragment: "nix-checks-skill.md",
		clause:   "is not tracked by Git",
		reason:   "a bare 'tracked' pin false-passes if this sentence is rewritten away, because \"git-tracked files\" earlier in the same body still contains the word; the pin follows its subject to the skill body (issues #782, #3223).",
	},

	// issue-prompt.md: CODE_FORGE=git, OPEN A PULL REQUEST
	{
		fragment: "issue-prompt.md",
		clause:   "CODE_FORGE=git",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "skip OPEN A PULL REQUEST below entirely",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "LAND_GIT_STOP_READ_WRITE_STEP",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "LAND_GIT_STOP_READ_ONLY_STEP",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "land-git-stop-read-write.md",
		clause:   "landing=agent/issue-7 status=ready",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "land-git-stop-read-write.md",
		clause:   "Do not open a pull request and do not attempt to merge",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "land-git-stop-read-only.md",
		clause:   "landing=agent/issue-7 status=ready",
		reason:   codeForgeGitReason,
	},
	{
		fragment: "land-git-stop-read-only.md",
		clause:   "Do not open a pull request and do not attempt to merge",
		reason:   codeForgeGitReason,
	},

	{
		fragment: "issue-prompt.md",
		from:     "# OPEN A PULL REQUEST",
		to:       "# OUTCOME",
		clause:   "OPEN_PR_CREATE_DIRECT_STEP",
		reason:   openDraftReason,
	},
	{
		fragment: "github/open-pr-create.md",
		clause:   "gh pr create --draft",
		reason:   openDraftReason,
	},

	// issue-prompt.md: OUTCOME
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "OUTCOME_LANDING_READ_WRITE_STEP",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "OUTCOME_LANDING_READ_ONLY_STEP",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "OUTCOME_LANDING_READ_ONLY_SOCKET_STEP",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-landing-git.md",
		clause:   "status=ready",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-landing-outbox.md",
		clause:   "status=ready",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-landing-outbox-socket.md",
		clause:   "status=ready",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "signal pr-intent accepted",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "status=blocked",
		reason:   outcomeLandingReason,
	},
	{
		fragment: "outcome-landing-outbox-socket.md",
		clause:   "PR-intent line",
		absent:   true,
		reason:   socketNoMarkerReason,
	},
	{
		fragment: "outcome-landing-outbox-socket.md",
		clause:   "SPINDRIFT_PR_INTENT",
		absent:   true,
		reason:   socketNoMarkerReason,
	},
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "PR-intent line",
		absent:   true,
		reason:   socketNoMarkerReason,
	},
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "SPINDRIFT_PR_INTENT",
		absent:   true,
		reason:   socketNoMarkerReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		pattern:  regexp.MustCompile(`(?m)^gh pr ready`),
		verbatim: true,
		absent:   true,
		reason:   "the launcher, not the Driver, owns the draft to ready flip (issue #1651), so the OUTCOME step must never carry a bare `gh pr ready` invocation line; the prose forbidding it below is not an invocation (issue #1653).",
	},

	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<landing-ref> status=<status> note=<text>",
		reason:   outcomeGrammarReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "The only valid `status` values here are `ready` and `blocked`",
		reason:   outcomeGrammarReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "`status=already-resolved`",
		reason:   outcomeGrammarReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "the ALREADY RESOLVED gate",
		reason:   outcomeGrammarReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "SPINDRIFT_OUTCOME:",
		absent:   true,
		reason:   outcomeColonReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		pattern:  regexp.MustCompile(`(?i)trailing colon`),
		absent:   true,
		reason:   outcomeColonReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "a prefix before the token",
		reason:   outcomePrefixReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "starting at column one",
		reason:   outcomePrefixReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "status=SUCCESS",
		reason:   outcomeEnumReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "an out-of-enum status",
		reason:   outcomeEnumReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "still parses",
		reason:   outcomeEnumReason,
	},
	{
		fragment: "issue-prompt.md",
		from:     "status=<status> note=<text>`",
		to:       "# IF BLOCKED",
		pattern:  regexp.MustCompile(`(?m)^[^\n]*SPINDRIFT_OUTCOME[^\n]*status=[^\n]*note=`),
		verbatim: true,
		absent:   true,
		reason:   "an agent editing this prose parrots a fully-formed counter-example back out as its own final message, so only the canonical Grammar line may carry the full token, status= and note= shape; the scope starts after that line (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "Grammar:",
		pattern:  regexp.MustCompile(`(?m)^[^\n]*SPINDRIFT_OUTCOME[^\n]*status=[^\n]*note=`),
		verbatim: true,
		absent:   true,
		reason:   "the same parroting guard for the OUTCOME header lines before the Grammar line (issue #3224).",
	},
	{
		fragment: "issue-prompt.md",
		from:     "# OUTCOME",
		to:       "# IF BLOCKED",
		clause:   "This must be the literal final message — nothing after it",
		reason:   "the agent must be told, in the OUTCOME section itself, that nothing may follow the outcome line (issue #1901).",
	},

	// issue-prompt.md and fix-prompt.md: IF BLOCKED, fix pass overrides
	{
		fragment: "issue-prompt.md",
		from:     "# IF BLOCKED",
		clause:   "--undo",
		absent:   true,
		reason:   ifBlockedReason,
	},
	{
		fragment: "if-blocked-outcome-landing-git.md",
		clause:   "--undo",
		absent:   true,
		reason:   ifBlockedReason,
	},
	{
		fragment: "if-blocked-outcome-landing-outbox.md",
		clause:   "--undo",
		absent:   true,
		reason:   ifBlockedReason,
	},
	{
		fragment: "if-blocked-outcome-landing-git.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_OUTCOME .* status=blocked`),
		reason:   ifBlockedReason,
	},
	{
		fragment: "if-blocked-outcome-landing-outbox.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_OUTCOME .* status=blocked`),
		reason:   ifBlockedReason,
	},
	{
		fragment: "fix-prompt.md",
		pattern:  regexp.MustCompile(`(?i)leave the existing PR as-is`),
		absent:   true,
		reason:   fixDraftRevertReason,
	},
	{
		fragment: "fix-prompt.md",
		pattern:  regexp.MustCompile(`(?i)draft-revert`),
		absent:   true,
		reason:   fixDraftRevertReason,
	},
	{
		fragment: "fix-prompt.md",
		clause:   "do not run `gh pr create`",
		reason:   "the fix pass's override bullet keeps telling the agent not to open a second PR (issue #1653).",
	},

	// the harness-owned contract blocks: one distinctive sentence per block per injecting prompt;
	// the golden prompts pin the blocks' full bytes
	{
		fragment: "issue-prompt.md",
		clause:   "This grammar's leading token is load-bearing (ADR 0035)",
		reason:   contractBlockReason + " The outcome contract must reach issue-prompt.md.",
	},
	{
		fragment: "fix-prompt.md",
		clause:   "This grammar's leading token is load-bearing (ADR 0035)",
		reason:   contractBlockReason + " The outcome contract must reach fix-prompt.md.",
	},
	{
		fragment: "fix-prompt.md",
		clause:   "Human-quality prose is reserved exclusively for",
		reason:   contractBlockReason + " The comms contract must reach fix-prompt.md.",
	},
	{
		fragment: "fix-prompt.md",
		clause:   "Run every check or build gate in the foreground and block on it yourself",
		reason:   contractBlockReason + " The check contract must reach fix-prompt.md.",
	},
	{
		fragment: "research-prompt.md",
		clause:   "Carry the machine marker `<!-- spindrift-research -->` in the comment body",
		reason:   contractBlockReason + " The research outcome contract must reach research-prompt.md.",
	},

	// PR body issue reference: github closes the issue, a local tracker does not
	{
		fragment: "pr-body-closes.md",
		pattern:  regexp.MustCompile(`Closes #\d+`),
		reason:   "the PR body carries GitHub's closing keyword so merging closes the issue; without it the issue stays open after landing.",
	},
	{
		fragment: "local/pr-body-issue-ref.md",
		pattern:  regexp.MustCompile(`Local-issue: \S+`),
		reason:   "a local-tracker Consumer that opted in gets a plain breadcrumb naming the ticket, so the PR stays traceable to it.",
	},
	{
		fragment: "local/pr-body-issue-ref.md",
		clause:   "never an auto-closing reference",
		reason:   "a local ticket's slug can be numeric, so a closing keyword would silently close an unrelated GitHub issue on the Target repo.",
	},
	{
		fragment: "local/pr-body-issue-ref.md",
		clause:   "Closes #",
		absent:   true,
		reason:   "a local ticket's slug can be numeric, so a closing keyword would silently close an unrelated GitHub issue on the Target repo.",
	},
	{
		fragment: "local/pr-body-no-issue-ref.md",
		clause:   "must NOT reference the local ticket",
		reason:   "a local ticket is private and local-only, so by default the PR body must not leak it.",
	},
	{
		fragment: "local/pr-body-no-issue-ref.md",
		clause:   "Closes #",
		absent:   true,
		reason:   "a local ticket is not a GitHub issue, so the PR body must carry no closing keyword.",
	},
	{
		fragment: "local/pr-body-no-issue-ref.md",
		clause:   "Local-issue:",
		absent:   true,
		reason:   "the breadcrumb is the Consumer's opt-in; the default local PR body names no ticket at all.",
	},

	// issue read: the subject issue is injected, so the agent never fetches it
	{
		fragment: "issue-prompt.md",
		clause:   "# ISSUE TEXT section after the template body — read there, do not fetch it from the tracker",
		reason:   "the subject issue's body and comment snapshot are injected, so the agent must read them there rather than spend a tracker call on text it already holds.",
	},
	{
		fragment: "issue-read.md",
		clause:   "already in the # ISSUE TEXT section below",
		reason:   "the subject issue's own text is injected, so only the parent and linked issues are fetched.",
	},
	{
		fragment: "issue-read.md",
		pattern:  regexp.MustCompile(`via (GitHub|Forgejo)\b`),
		reason:   "the fragment is shared across trackers and names the active one, so the agent fetches parents and links through the right client.",
	},
	{
		fragment: "issue-read.md",
		clause:   "gh issue view",
		absent:   true,
		reason:   "the subject issue is injected, so the read step must not send the agent to fetch it.",
	},
	{
		fragment: "local/issue-read.md",
		clause:   "via GitHub",
		absent:   true,
		reason:   "a local tracker has no GitHub client, so the read step must not point at one.",
	},
	{
		fragment: "local/issue-read.md",
		clause:   "gh issue view",
		absent:   true,
		reason:   "a local tracker has no GitHub client, so the read step must not send the agent to fetch the issue.",
	},
	{
		fragment: "local/issue-read.md",
		clause:   "/issues",
		absent:   true,
		reason:   "the subject issue is injected, so the read step must not point the agent at a local issue folder.",
	},
	{
		fragment: "issue-read-unreadable.md",
		clause:   "cannot be read from this Box",
		reason:   "a tracker with no in-Box client (jira) leaves linked issues unreadable, and the agent must be told so rather than try a client that does not exist.",
	},
	{
		fragment: "research-issue-read-unreadable.md",
		clause:   "cannot be read from this Box",
		reason:   "a tracker with no in-Box client (jira) leaves linked issues unreadable, and the agent must be told so rather than try a client that does not exist.",
	},
	{
		fragment: "issue-read-unreadable.md",
		pattern:  regexp.MustCompile(`via (GitHub|Forgejo)\b`),
		absent:   true,
		reason:   "the unreadable arm must not name a client the Box does not have.",
	},
	{
		fragment: "research-issue-read-unreadable.md",
		pattern:  regexp.MustCompile(`via (GitHub|Forgejo)\b`),
		absent:   true,
		reason:   "the unreadable arm must not name a client the Box does not have.",
	},

	// research verdict: posted directly where the Box may write, relayed otherwise
	{
		fragment: "github/research-verdict.md",
		clause:   "gh issue comment",
		reason:   "a read-write github Box posts the verdict itself, as exactly one comment.",
	},
	{
		fragment: "github/research-verdict.md",
		clause:   "SPINDRIFT_COMMENT",
		absent:   true,
		reason:   "a Box that posts the verdict itself has no relayed line to print.",
	},
	{
		fragment: "forgejo/research-verdict.md",
		clause:   "fj issue comment",
		reason:   "a read-write forgejo Box posts the verdict through fj.",
	},
	{
		fragment: "forgejo/research-verdict.md",
		clause:   "gh issue comment",
		absent:   true,
		reason:   "gh targets GitHub, not a Forgejo instance.",
	},
	{
		fragment: "research-verdict-relay.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_COMMENT \S+ <base64-encoded verdict comment body`),
		reason:   "the log carrier relays the verdict as one nonce-guarded base64 line the launcher finds by this run's nonce.",
	},
	{
		fragment: "research-verdict-relay.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_COMMENT_(BEGIN|END)`),
		absent:   true,
		reason:   "the verdict is one nonce-guarded base64 line; a BEGIN/END block form is not the grammar the launcher reads.",
	},
	{
		fragment: "fragments/local/research-verdict-relay.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_COMMENT \S+ <base64-encoded verdict comment body`),
		reason:   "the log carrier relays the verdict as one nonce-guarded base64 line the launcher finds by this run's nonce.",
	},
	{
		fragment: "fragments/local/research-verdict-relay.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_COMMENT_(BEGIN|END)`),
		absent:   true,
		reason:   "the verdict is one nonce-guarded base64 line; a BEGIN/END block form is not the grammar the launcher reads.",
	},

	// research outcome landing
	{
		fragment: "github/research-outcome-landing.md",
		clause:   "`landing` is the URL of the comment you just posted",
		reason:   "a Box that posts the verdict itself reports the comment's URL as the landing.",
	},
	{
		fragment: "github/research-outcome-landing.md",
		clause:   "`gh issue comment` prints it",
		reason:   "the URL comes from the comment command's own output.",
	},
	{
		fragment: "github/research-outcome-landing.md",
		clause:   "landing=none",
		absent:   true,
		reason:   "landing=none belongs to the relayed arm, where the Box never learns the comment's URL.",
	},
	{
		fragment: "forgejo/research-outcome-landing.md",
		clause:   "posted with `fj issue comment`",
		reason:   "a forgejo Box that posts the verdict itself names fj as the source of the landing URL.",
	},
	{
		fragment: "forgejo/research-outcome-landing.md",
		clause:   "`landing=none` if it printed no URL",
		reason:   "fj issue comment may print no URL, and the landing is then none.",
	},
	{
		fragment: "forgejo/research-outcome-landing.md",
		clause:   "gh issue comment",
		absent:   true,
		reason:   "gh targets GitHub, not a Forgejo instance.",
	},
	{
		fragment: "research-outcome-landing-relayed.md",
		clause:   "the launcher posts your relayed verdict host-side once you exit",
		reason:   "a relayed verdict (jira research, Filer on or off, included) is posted by the launcher after the Box exits, so the Box never learns its URL (issue #5245).",
	},
	{
		fragment: "research-outcome-landing-relayed-socket.md",
		clause:   "the launcher posts your relayed verdict host-side once you exit",
		reason:   "a relayed verdict (jira research, Filer on or off, included) is posted by the launcher after the Box exits, so the Box never learns its URL (issue #5245).",
	},
	{
		fragment: "research-outcome-landing-relayed-socket.md",
		clause:   "`driver-exec signal comment` exits zero",
		reason:   "the socket carrier acknowledges a relay by the send's zero exit, which is what tells the agent the verdict is delivered.",
	},
	{
		fragment: "research-outcome-landing-relayed-socket.md",
		clause:   "Nothing acknowledges a relay",
		absent:   true,
		reason:   "the socket carrier does acknowledge a relay, unlike the log carrier.",
	},
	{
		fragment: "research-outcome-landing-relayed.md",
		clause:   "Nothing acknowledges a relay on the `log` carrier",
		reason:   "the log carrier gives no acknowledgement, so the agent must not wait for one or call a relayed verdict undelivered.",
	},
	{
		fragment: "research-outcome-landing-relayed.md",
		clause:   "signal comment accepted",
		absent:   true,
		reason:   "the log carrier has no acknowledgement line, so the agent must not wait for one.",
	},
	{
		fragment: "research-outcome-landing-relayed.md",
		clause:   "The relay failed only if the guard kept reporting over budget",
		reason:   "on the log carrier the only relay failure the agent can see is the guard reporting over budget, so a successful relay never trips the blocked escape hatch.",
	},
	{
		fragment: "research-outcome-landing-relayed.md",
		clause:   "relay failed as described above",
		absent:   true,
		reason:   "the log carrier names its one failure mode in place; an unspecific pointer leaves the agent to guess.",
	},
	{
		fragment: "research-prompt.md",
		clause:   "or the verdict was never delivered, use",
		reason:   "the blocked escape hatch fires on a verdict that was never delivered, not on a relayed one, so a successful relay is not reported blocked.",
	},
	{
		fragment: "research-prompt.md",
		clause:   "or the comment cannot be posted",
		absent:   true,
		reason:   "a relayed verdict is never posted by the Box, so the hatch must not key on the Box posting a comment.",
	},

	// blocked-run issue comment
	{
		fragment: "github/issue-blocked-comment.md",
		clause:   "gh issue comment",
		reason:   "a read-write github Box comments what is done and what remains itself.",
	},
	{
		fragment: "forgejo/issue-blocked-comment.md",
		clause:   "fj issue comment",
		reason:   "a read-write forgejo Box comments what is done and what remains through fj.",
	},
	{
		fragment: "issue-blocked-comment-relay.md",
		clause:   "the launcher posts it as the issue comment",
		reason:   "a read-only Box cannot comment, so the SPINDRIFT_OUTCOME note is the comment the launcher posts for it.",
	},
	{
		fragment: "local/issue-blocked-comment-relay.md",
		clause:   "the launcher posts the SPINDRIFT_OUTCOME",
		reason:   "a local tracker has no in-Box client, so the SPINDRIFT_OUTCOME note is the comment the launcher posts for the Box.",
	},
	{
		fragment: "issue-blocked-comment-unwritable.md",
		clause:   "no client that can post a comment to this tracker",
		reason:   "a tracker with no in-Box client (jira) relays the blocked comment through the host, and the agent must be told why it does nothing here.",
	},
	{
		fragment: "issue-blocked-comment-unwritable.md",
		clause:   "gh issue comment",
		absent:   true,
		reason:   "a jira work Box has no in-Box client, so the IF BLOCKED step relays through the host and never names `gh issue comment` (issue #5263).",
	},
	{
		fragment: "issue-blocked-comment-unwritable.md",
		clause:   "GitHub token",
		absent:   true,
		reason:   "the unwritable arm is not about a read-only GitHub token; it has no client at all.",
	},

	// push and PR open/blocked arms: a read-write Box acts, a read-only one hands off
	{
		fragment: "open-pr-push-git.md",
		clause:   "git push --force-with-lease --force-if-includes -u origin",
		reason:   "a read-write Box pushes its branch itself, and --force-if-includes keeps the lease from overwriting a commit someone else pushed.",
	},
	{
		fragment: "open-pr-push-outbox.md",
		clause:   "the harness relays your committed branch out",
		reason:   "a read-only Box takes no push action; the harness relays its committed branch and the launcher pushes it host-side.",
	},
	{
		fragment: "open-pr-push-outbox.md",
		clause:   "git push --force-with-lease",
		absent:   true,
		reason:   "a read-only Box holds no push-capable token, so the fragment must not instruct a push.",
	},
	{
		fragment: "commit-push-git.md",
		clause:   "git push --force-with-lease --force-if-includes -u origin",
		reason:   "a read-write Box pushes after rebasing, and --force-if-includes keeps the lease from overwriting a commit someone else pushed.",
	},
	{
		fragment: "commit-push-git.md",
		clause:   "one retry only",
		reason:   "a rejected push retries once and then follows IF BLOCKED, so the agent does not loop on a push it cannot land.",
	},
	{
		fragment: "commit-push-outbox.md",
		clause:   "the harness relays your committed branch out",
		reason:   "a read-only Box takes no push action; the harness relays its committed branch and the launcher pushes it host-side.",
	},
	{
		fragment: "commit-push-outbox.md",
		clause:   "git rebase origin/",
		reason:   "a read-only Box still hands off a branch on a fresh base, since the host push does not rebase it.",
	},
	{
		fragment: "commit-push-outbox.md",
		clause:   "git push --force-with-lease",
		absent:   true,
		reason:   "a read-only Box holds no push-capable token, so the fragment must not instruct a push.",
	},
	{
		fragment: "if-blocked-triage-git.md",
		clause:   "Push failure — check the actual cause before reporting it",
		reason:   "a read-write Box's rejected push has a real cause to diagnose before it is reported, not a guess.",
	},
	{
		fragment: "if-blocked-triage-git.md",
		clause:   ".github/workflows/",
		reason:   "the token lacks workflow scope by design, so a rejected push touching workflows is a human-review case, not a retry.",
	},
	{
		fragment: "if-blocked-triage-outbox.md",
		clause:   "A denied `git push` here is expected",
		reason:   "a read-only Box's denied push is the outcome it was always going to get, so it must not be diagnosed as a broken token.",
	},
	{
		fragment: "if-blocked-triage-outbox.md",
		clause:   "Push failure — check the actual cause before reporting it",
		absent:   true,
		reason:   "the push-failure triage belongs to the read-write arm; a read-only Box's denial is expected.",
	},
	{
		fragment: "if-blocked-triage-outbox.md",
		clause:   "git diff origin/",
		absent:   true,
		reason:   "the workflows diff triage belongs to the read-write arm, since a read-only Box pushes nothing to be rejected.",
	},
	{
		fragment: "if-blocked-triage-outbox.md",
		clause:   "**Genuine `.github/workflows/` change:**",
		absent:   true,
		reason:   "the workflow-scope diagnosis belongs to the read-write arm, since a read-only Box pushes nothing to be rejected.",
	},
	{
		fragment: "if-blocked-push-git.md",
		clause:   "Push what you have (or note if even that is impossible)",
		reason:   "a blocked read-write Box still pushes its work so none is stranded.",
	},
	{
		fragment: "if-blocked-push-git.md",
		clause:   "seam.bundle",
		absent:   true,
		reason:   "a read-write Box pushes directly; the bundle hand-off belongs to no arm of this fragment.",
	},
	{
		fragment: "if-blocked-push-outbox.md",
		clause:   "the harness relays your committed branch out",
		reason:   "a blocked read-only Box leaves its work committed and the harness relays it, so it takes no push or bundle action.",
	},
	{
		fragment: "if-blocked-push-outbox.md",
		clause:   "Push what you have (or note if even that is impossible)",
		absent:   true,
		reason:   "the push-what-you-have step belongs to the read-write arm.",
	},
	{
		fragment: "if-blocked-push-outbox.md",
		clause:   "/outbox/seam.bundle",
		absent:   true,
		reason:   "a read-only Box takes no bundle action; the harness relays the branch.",
	},

	// open-PR create arms
	{
		fragment: "github/open-pr-create.md",
		clause:   "fj pr create",
		absent:   true,
		reason:   "fj targets Forgejo, not GitHub.",
	},
	{
		fragment: "github/open-pr-create.md",
		clause:   "SPINDRIFT_PR_INTENT",
		absent:   true,
		reason:   "a Box that opens the PR itself has no PR-intent line to print.",
	},
	{
		fragment: "forgejo/open-pr-create.md",
		clause:   "2. `fj pr create",
		reason:   "a read-write forgejo Box opens its draft through fj as step 2.",
	},
	{
		fragment: "forgejo/open-pr-create.md",
		clause:   "2. `gh pr create",
		absent:   true,
		reason:   "gh targets GitHub, not a Forgejo instance; only the closing reminder may name it.",
	},
	{
		fragment: "open-pr-create-outbox.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_PR_INTENT \S+ <base64-encoded title`),
		reason:   "a read-only Box prints its PR as one nonce-guarded line the launcher finds by this run's nonce (issue #1938).",
	},
	{
		fragment: "open-pr-create-outbox.md",
		clause:   "gh pr create --draft --base",
		absent:   true,
		reason:   "a read-only Box cannot open the PR, so only the read-write fragment may carry the invocation.",
	},
	{
		fragment: "open-pr-create-outbox.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_PR_INTENT_(BEGIN|END)`),
		absent:   true,
		reason:   "the PR intent is one line; a BEGIN/END block form is not the grammar the launcher reads.",
	},
	{
		fragment: "github/if-blocked-pr.md",
		clause:   "gh pr view --json url",
		reason:   "a blocked read-write github Box checks for an existing PR before opening a draft.",
	},
	{
		fragment: "github/if-blocked-pr.md",
		clause:   "SPINDRIFT_PR_INTENT",
		absent:   true,
		reason:   "a Box that opens the PR itself has no PR-intent line to print.",
	},
	{
		fragment: "if-blocked-pr-outbox.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_PR_INTENT \S+ <base64-encoded title`),
		reason:   "a blocked read-only Box prints its draft PR as one nonce-guarded line the launcher finds by this run's nonce.",
	},
	{
		fragment: "if-blocked-pr-outbox.md",
		clause:   "gh pr view --json url",
		absent:   true,
		reason:   "a read-only Box has no PR client to check with.",
	},
	{
		fragment: "if-blocked-pr-outbox.md",
		clause:   "gh pr create --draft",
		absent:   true,
		reason:   "a read-only Box cannot open the PR, so only the read-write fragment may carry the invocation.",
	},
	{
		fragment: "if-blocked-pr-none.md",
		clause:   "2. Open no pull request",
		reason:   "a git Code Forge has no PR, so a blocked run opens none and keeps step 3 numbered after step 2 (issue #5233).",
	},
	{
		fragment: "if-blocked-pr-none.md",
		clause:   "gh pr view",
		absent:   true,
		reason:   "a git Code Forge has no PR to look up (issue #5233).",
	},
	{
		fragment: "if-blocked-pr-none.md",
		clause:   "--draft",
		absent:   true,
		reason:   "a git Code Forge has no PR to open as a draft (issue #5233).",
	},
	{
		fragment: "if-blocked-pr-none.md",
		clause:   "SPINDRIFT_PR_INTENT",
		absent:   true,
		reason:   "a git Code Forge has no PR whose intent could be relayed (issue #5233).",
	},
	{
		fragment: "issue-prompt.md",
		clause:   "3. Leave the issue in-progress — do NOT close it.",
		reason:   "the IF BLOCKED arms before this step end at step 2 or 3, so it must stay numbered 3 whichever arm renders.",
	},
	{
		fragment: "if-blocked-outcome-landing-git.md",
		clause:   "landing=<pr-url> status=blocked",
		reason:   "a blocked read-write Box has a PR, so its outcome line carries the PR URL placeholder.",
	},
	{
		fragment: "if-blocked-outcome-landing-branch.md",
		pattern:  regexp.MustCompile(`landing=agent/issue-\d+ status=blocked`),
		reason:   "a git Code Forge has no PR, so a blocked outcome line reports the branch as the landing (issue #5233).",
	},
	{
		fragment: "if-blocked-outcome-landing-branch.md",
		clause:   "landing=<pr-url>",
		absent:   true,
		reason:   "a git Code Forge has no PR URL to report (issue #5233).",
	},
	{
		fragment: "if-blocked-outcome-landing-outbox.md",
		pattern:  regexp.MustCompile(`landing=agent/issue-\d+ status=blocked`),
		reason:   "a read-only Box never learns the PR URL, so a blocked outcome line reports the branch as the landing.",
	},
	{
		fragment: "if-blocked-outcome-landing-outbox.md",
		clause:   "landing=<pr-url>",
		absent:   true,
		reason:   "a read-only Box never learns the PR URL.",
	},

	// Filer arms
	{
		fragment: "file-issues-direct.md",
		clause:   "use the filer's returned issue URLs",
		reason:   "a direct Filer returns real issue URLs, which replace the raw findings in the PR body.",
	},
	{
		fragment: "file-issues-relay.md",
		clause:   "queued for filing",
		reason:   "a relayed Filer knows no issue URL yet, so the PR body only notes that the findings were queued.",
	},
	{
		fragment: "file-issues-relay.md",
		clause:   "the filer's returned issue URLs",
		absent:   true,
		reason:   "a relayed Filer returns no issue URLs.",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "queued for filing",
		reason:   "a relayed Filer knows no issue URL yet, so the PR body only notes that the findings were queued.",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "the filer's returned issue URLs",
		absent:   true,
		reason:   "a relayed Filer returns no issue URLs.",
	},
	{
		fragment: "github/filer-direct-file.md",
		clause:   "gh issue create",
		reason:   "a direct github Filer files each finding with gh.",
	},
	{
		fragment: "github/filer-direct-file.md",
		clause:   "SPINDRIFT_ISSUE_INTENT",
		absent:   true,
		reason:   "a direct Filer files with its own token, so it prints no relayed intent line.",
	},
	{
		fragment: "github/filer-direct-file.md",
		clause:   "fj issue create",
		absent:   true,
		reason:   "fj targets Forgejo, not GitHub.",
	},
	{
		fragment: "github/filer-direct-label.md",
		clause:   "gh label create",
		reason:   "a direct github Filer ensures the provenance label exists with gh.",
	},
	{
		fragment: "forgejo/filer-direct-file.md",
		clause:   "fj issue create",
		reason:   "a direct forgejo Filer files each finding with fj.",
	},
	{
		fragment: "forgejo/filer-direct-file.md",
		clause:   "gh issue create",
		absent:   true,
		reason:   "gh targets GitHub, not a Forgejo instance.",
	},
	{
		fragment: "forgejo/filer-direct-label.md",
		clause:   "gh label create",
		absent:   true,
		reason:   "gh targets GitHub, not a Forgejo instance.",
	},
	{
		fragment: "filer-file-relay.md",
		pattern:  regexp.MustCompile(`SPINDRIFT_ISSUE_INTENT \S+ <base64-payload>`),
		reason:   "a relayed Filer prints one nonce-guarded base64 line per issue, which the launcher files host-side.",
	},
	{
		fragment: "filer-file-relay.md",
		clause:   "gh issue create",
		absent:   true,
		reason:   "a relayed Filer never files issues itself, even where its token could.",
	},
	{
		fragment: "filer-label-relay.md",
		clause:   "gh label create",
		absent:   true,
		reason:   "a relayed Filer cannot create labels; the launcher applies the provenance label host-side.",
	},
	{
		fragment: "github/filer-dedup.md",
		clause:   "gh issue list --state open --search",
		reason:   "a github Filer searches open issues before filing, so a finding already tracked is not refiled.",
	},
	{
		fragment: "github/filer-dedup.md",
		clause:   "run no tracker search at all",
		absent:   true,
		reason:   "the no-search arm belongs to trackers with no in-Box client.",
	},
	{
		fragment: "filer-relay-reason-readonly.md",
		clause:   "Your token is read-only",
		reason:   "a read-only Box's relay states its real reason, the token it was given (issue #6354).",
	},
	{
		fragment: "filer-relay-reason-readonly.md",
		clause:   "no in-Box client",
		absent:   true,
		reason:   "the read-only arm is about the Box's token, not the tracker's missing client (issue #6354).",
	},
	{
		fragment: "filer-relay-reason-unwritable.md",
		clause:   "This issue tracker has no in-Box client",
		reason:   "a read-write Box on a tracker with no in-Box client relays because of the tracker, since its prompt later tells it to push with a token that is not read-only (issue #6354).",
	},
	{
		fragment: "filer-relay-reason-unwritable.md",
		clause:   "token is read-only",
		absent:   true,
		reason:   "a read-write Box must never be told its token is read-only, which contradicts the push it is later told to make (issue #6354).",
	},
	{
		fragment: "filer-dedup-unwritable.md",
		clause:   "run no tracker search at all",
		reason:   "a tracker with no in-Box client has nothing to search, and gh would query the wrong tracker.",
	},
	{
		fragment: "filer-dedup-unwritable.md",
		clause:   "exact site key and overlapping line range",
		reason:   "with no in-Box search, the host's dedup by site key and line range is the whole check, and the agent must know it is never by subject.",
	},
	{
		fragment: "filer-dedup-unwritable.md",
		clause:   "gh issue list",
		absent:   true,
		reason:   "gh would query the wrong tracker.",
	},
	{
		fragment: "local/filer-dedup.md",
		clause:   "run no tracker search at all",
		reason:   "a local tracker has no in-Box search, and gh would query the wrong tracker.",
	},
	{
		fragment: "local/filer-dedup.md",
		clause:   "exact site key and overlapping line range",
		reason:   "with no in-Box search, the host's dedup by site key and line range is the whole check, and the agent must know it is never by subject.",
	},
	{
		fragment: "local/filer-dedup.md",
		clause:   "gh issue list",
		absent:   true,
		reason:   "gh would query the wrong tracker.",
	},

	// Knob- and skill-gated steps point at their skill instead of restating it.
	// AUTO_FORMAT and AUTO_LINT are Consumer knobs off in every golden cell, so
	// their fragments are checked as raw source (nixRenderedSources).
	{
		fragment: "fragments/auto-format.md",
		clause:   "invoke the `/auto-format` skill",
		reason:   "the AUTO-FORMAT step points at the baked skill, which owns the procedure.",
	},
	{
		fragment: "fragments/auto-format.md",
		clause:   "`nix fmt` when the target flake defines a formatter",
		absent:   true,
		reason:   "the formatter selection lives in the auto-format skill; a restated copy in the step is the duplication baking the skill removed.",
	},
	{
		fragment: "fragments/auto-format.md",
		clause:   "store-lock permission error",
		absent:   true,
		reason:   "the store-lock workaround lives in the auto-format skill; a restated copy in the step is the duplication baking the skill removed.",
	},
	{
		fragment: "fragments/auto-lint.md",
		clause:   "invoke the `/auto-lint` skill",
		reason:   "the AUTO-LINT step points at the baked skill, which owns the procedure.",
	},
	{
		fragment: "fragments/auto-lint.md",
		clause:   "Apply the linter's safe auto-fix mode",
		absent:   true,
		reason:   "the linter procedure lives in the auto-lint skill; a restated copy in the step is the duplication baking the skill removed.",
	},
	{
		fragment: "tdd-baked.md",
		clause:   "Work test-first: run `/tdd` for each slice.",
		reason:   "baking the tdd skill leaves a bare anchor pointing at /tdd (issue #3219).",
	},
	{
		fragment: "tdd-baked.md",
		clause:   "RED: write ONE failing test",
		absent:   true,
		reason:   "baking the tdd skill subtracts the inline red/green/refactor fallback instead of restating it beside the anchor (issue #3219).",
	},
	{
		fragment: "tdd-unbaked.md",
		clause:   "RED: write ONE failing test",
		reason:   "with no tdd skill baked, the red/green/refactor rule renders inline so the discipline survives (issue #3219).",
	},
	{
		fragment: "tdd-unbaked.md",
		clause:   "Work test-first: run `/tdd` for each slice.",
		absent:   true,
		reason:   deadSkillReason,
	},
	{
		fragment: "issue-prompt.md",
		clause:   "RED: write ONE failing test",
		absent:   true,
		reason:   "the red/green/refactor fallback moved out of the template into the unbaked arm, so a baked run does not carry it (issue #3219).",
	},
	{
		fragment: "commit-unbaked.md",
		clause:   "hard-wrapped (subject",
		reason:   "with no commit skill baked, the Conventional Commits format rules render inline (issue #3222).",
	},
	{
		fragment: "commit-unbaked.md",
		clause:   "Use the `/commit` skill to write every commit message",
		absent:   true,
		reason:   deadSkillReason,
	},
	{
		fragment: "commit-baked.md",
		clause:   "hard-wrapped (subject",
		absent:   true,
		reason:   "baking the commit skill subtracts the inline format rules instead of restating them beside the anchor (issue #3222).",
	},
	{
		fragment: "code-review-baked.md",
		clause:   "Run the `/code-review` skill and fold its two-axis (Standards + Spec) findings",
		reason:   "the baked arm orders the skill's two-axis fan-out and folds its findings back into the hunt (issue #3222).",
	},
	{
		fragment: "fragments/code-review-baked.md",
		clause:   "agent type `${REVIEW_FANOUT_AGENT}`",
		reason:   "the anchor must name an agent type the run actually provisions, resolved per run from the roster, so a Consumer pinning the historical four-entry roster or taking reviewModel=\"\" gets the skill's own default instead of an agent type its session never defines (issues #3447, #392); checked as raw source, since the rendered fragment has the name substituted.",
	},
	{
		fragment: "fragments/code-review-baked.md",
		clause:   "agent type `review-axis`",
		absent:   true,
		reason:   "a baked literal is the drift that made a non-provisioned agent type reachable (issue #3447).",
	},
	{
		fragment: "fragments/code-review-baked.md",
		clause:   "agent type `general-purpose`",
		absent:   true,
		reason:   "a baked literal is the drift that made a non-provisioned agent type reachable (issue #3447).",
	},
	{
		fragment: "code-review-unbaked.md",
		clause:   "Run the `/code-review` skill",
		absent:   true,
		reason:   deadSkillReason,
	},
	{
		fragment: "code-review-unbaked.md",
		clause:   "run every dimension below yourself in this one context",
		reason:   "with no review skill baked, the reviewer hunts solo; there are no reviewer subagents to fan out to (issue #3222).",
	},
	{
		fragment: "review-prompt.md",
		clause:   "Hunt every dimension",
		reason:   "the hunt dimensions render unconditionally, outside both code-review arms, so a depth obligation does not vanish on unbaked runs (issue #3226).",
	},
	{
		fragment: "review-prompt.md",
		clause:   "VERDICT: APPROVE | BLOCK",
		reason:   "the verdict grammar scanPassLog parses stays in the template whichever code-review arm renders (ADR 0035).",
	},

	// coordinator.md
	{
		fragment: "coordinator.md",
		clause:   "**coordinator**",
		reason:   "a provisioned worker makes the main session the coordinator rather than the implementor (issue #2056).",
	},
	{
		fragment: "coordinator.md",
		clause:   "delegate each slice",
		reason:   "the coordinator's job is to hand each slice to the worker (issue #2056).",
	},
	{
		fragment: "coordinator.md",
		clause:   "gitignore",
		absent:   true,
		reason:   "the coordinator step must not touch the Consumer repo's .gitignore.",
	},
	{
		fragment: "coordinator.md",
		clause:   "WORK_DIR",
		absent:   true,
		reason:   "the coordinator step must not name an unexported $WORK_DIR.",
	},

	// review-loop-orchestrator.md: the code-owned review pass
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "Review is handled by the orchestrator as a separate, code-owned pass",
		reason:   "the Driver does not run the review; the orchestrator's code-owned pass does (issue #2037).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "spawn a fresh `reviewer` subagent",
		absent:   true,
		reason:   "the reviewer subagent was replaced by the code-owned review pass, so the prompt must not order one (issue #2037).",
	},
	{
		fragment: "issue-prompt.md",
		clause:   "spawn a fresh `reviewer` subagent",
		absent:   true,
		reason:   "the reviewer subagent was replaced by the code-owned review pass, so the template must not order one (issue #2037).",
	},
	{
		fragment: "review-loop-orchestrator-no-filer.md",
		clause:   "Do not run LAND THE CHANGE, OPEN A",
		reason:   "with no Filer, the stop-after-COMMIT bullet names the landing steps without FILE ISSUES (issue #5300).",
	},
	{
		fragment: "review-loop-orchestrator-no-filer.md",
		clause:   "then continue straight into LAND THE CHANGE,",
		reason:   "with no Filer, the APPROVE bullet continues straight into LAND THE CHANGE (issue #5300).",
	},
	{
		fragment: "issue-prompt.md",
		clause:   "FILE ISSUES",
		absent:   true,
		reason:   "the heading and every step mention live in Filer-gated fragments, so a run with no Filer never names a step it does not render (issue #5300).",
	},

	// scout
	{
		fragment: "scout-delegate.md",
		clause:   "Delegate exploration to the `scout` subagent",
		reason:   "a provisioned scout maps the repo before the main session reads source (issue #3157).",
	},
	{
		fragment: "fragments/scout-issue-read.md",
		clause:   "via ${FORGE_DISPLAY_NAME}",
		reason:   "the scout's issue-read step is shared across trackers and names the active one, so linked issues are fetched through the right client; it renders into the agents JSON, which no checked prompt captures, so it is checked as raw source.",
	},
	{
		fragment: "fragments/scout-issue-read-unreadable.md",
		clause:   "cannot be read from this Box",
		reason:   "a tracker with no in-Box client (jira) leaves linked issues unreadable, and the scout must be told so rather than try a client that does not exist (issue #6344); it renders into the agents JSON, so it is checked as raw source.",
	},

	// ci-failure.md: a fix-pass step no golden cell renders, so checked as raw source
	{
		fragment: "fragments/ci-failure.md",
		clause:   "failure itself",
		reason:   "the summary only names which checks failed, so the step must frame it as a pointer and send the agent to the job log rather than present it as the failure.",
	},
	{
		fragment: "fragments/ci-failure.md",
		clause:   "known failure",
		absent:   true,
		reason:   "the summary is a pointer, not a diagnosis, so it must not be framed as a known failure the agent can act on without reading the log.",
	},
	{
		fragment: "fix-prompt.md",
		clause:   "skip anything CI FAILURE",
		absent:   true,
		reason:   "the CI FAILURE summary does not replace reading the failing job's log, so the template must not say to skip anything on its account.",
	},

	// issue-read.md
	{
		fragment: "issue-read.md",
		pattern:  regexp.MustCompile(`references too via [A-Z]\w+`),
		reason:   "the parent/linked-issue pull names the tracker's forge through FORGE_DISPLAY_NAME, so the agent reads linked issues from the right backend.",
	},

	// land-git-stop-read-write.md
	{
		fragment: "land-git-stop-read-write.md",
		clause:   "2. Print exactly one line",
		reason:   "a read-write CODE_FORGE=git Box pushes first, so its stop step is step 2; the numbering must follow the push step it renders after (issue #2526).",
	},

	// land-git-stop-read-only.md
	{
		fragment: "land-git-stop-read-only.md",
		clause:   "1. Print exactly one line",
		reason:   "a read-only CODE_FORGE=git Box has no push step before it, so its stop step is step 1 (issue #2526).",
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
		fragments: []string{"skill-preamble.md", "worker-scout-brief.md", "tdd-baked.md", "tdd-unbaked.md", "principle-laziness-protocol-default.md", "principle-redesign-from-first-principles-default.md"},
		clauseContract: clauseContract{
			pattern: regexp.MustCompile(`SPINDRIFT_OUTCOME|VERDICT:|\$\{`),
			absent:  true,
			reason:  "the worker's prompt is its template plus these spliced fragments (the template has its own entry), and none may carry outcome or verdict grammar, so a stray marker from a misbehaving worker can never terminate the run or be read as a review result, nor an unsubstituted ${...} token (issues #2059, #2491, #6182).",
		},
	},
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
	{
		fragments: []string{"research-verdict-relay.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md", "fragments/local/research-verdict-relay.md", "research-outcome-landing-relayed.md", "research-outcome-landing-relayed-socket.md"},
		clauseContract: clauseContract{
			clause: "gh issue comment",
			absent: true,
			reason: "a relayed verdict is posted by the launcher host-side, so the Box must not be told to run a tracker client.",
		},
	},
	{
		fragments: []string{"research-verdict-relay.md", "research-verdict-relay-socket.md", "local/research-verdict-relay-socket.md", "fragments/local/research-verdict-relay.md", "research-outcome-landing-relayed.md", "research-outcome-landing-relayed-socket.md"},
		clauseContract: clauseContract{
			clause: "fj issue comment",
			absent: true,
			reason: "a relayed verdict is posted by the launcher host-side, so the Box must not be told to run a tracker client.",
		},
	},
	{
		fragments: []string{"research-outcome-landing-relayed.md", "research-outcome-landing-relayed-socket.md"},
		clauseContract: clauseContract{
			clause: "always use `landing=none`",
			reason: "a relayed verdict is posted host-side, so the Box never learns the comment's URL.",
		},
	},
	{
		fragments: []string{"research-outcome-landing-relayed.md", "research-outcome-landing-relayed-socket.md"},
		clauseContract: clauseContract{
			clause: "a relayed verdict is a delivered verdict",
			reason: "a relayed verdict is not a failure, so the agent must not report it blocked.",
		},
	},
	{
		fragments: []string{"research-outcome-landing-relayed.md", "research-outcome-landing-relayed-socket.md"},
		clauseContract: clauseContract{
			clause: "comment you just posted",
			absent: true,
			reason: "the Box posts no comment on a relayed run, so there is no URL of one to report.",
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

// nixRenderedSources names the pseudo-cell of Nix-rendered source files.
// The assembler records only fragment spans, so a contract on a base prompt
// template or a skill body checks the file as the seam fixtures hold it: the
// templates under prompts/ keyed by file name, nix-checks-skill.md, and every
// fragments/<path> file keyed by its path. Their step placeholders
// (${CHECK_HYGIENE_STEP}) are unsubstituted.
const nixRenderedSources = "nix-rendered-sources"

// nixRenderedSourceRenders reads the pseudo-cell's files, failing if a key
// would shadow a fragment name any golden cell rendered.
func nixRenderedSourceRenders(t *testing.T, renders renderedFragments) map[string][]string {
	t.Helper()
	sources := map[string][]string{}
	dir := seamtest.Path(t, "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read prompt %s: %v", e.Name(), err)
		}
		sources[e.Name()] = []string{string(b)}
	}
	skill, err := os.ReadFile(seamtest.Path(t, "nix-checks-skill.md"))
	if err != nil {
		t.Fatalf("read nix-checks skill: %v", err)
	}
	sources["nix-checks-skill.md"] = []string{string(skill)}
	// Raw "fragments/<path>" keys are for a fragment no golden cell renders (it
	// would fail the vacuity guard), an obligation on the unsubstituted source
	// (code-review-baked.md's ${REVIEW_FANOUT_AGENT}), or a fragment that
	// renders only into the agents JSON. Otherwise contract on the golden-cell
	// name: raw text is unsubstituted.
	fragDir := filepath.Join(dir, "fragments")
	err = filepath.WalkDir(fragDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(rel)] = []string{string(b)}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", fragDir, err)
	}
	for name := range sources {
		for cell, fragments := range renders {
			if _, clash := fragments[name]; clash {
				t.Fatalf("source %s shadows the fragment of that name rendered in cell %s", name, cell)
			}
		}
	}
	return sources
}

// Reasons several source-prompt contracts share.
const (
	coherenceReason      = "issue #3224's pass shrank the ISSUE COHERENCE GATE, and each obligation of its pre-tightening prose must survive."
	commsReason          = "output is a machine-parsed log except on the parts that stay human prose; the COMMS section must say so."
	backgroundGateReason = "the never-background rule (issue #571) must cover CHECK's long build and test gates: an agent that backgrounds a gate and ends its turn never emits SPINDRIFT_OUTCOME (issues #3221, #3505)."
	checkRestatedReason  = "the elaborated check guidance lives in the check-hygiene skill (issues #3220, #3223); a restated copy in CHECK is the duplication that move removed, so its absence is pinned, not just the anchor's presence."
	codeCommentsReason   = "the code-comments policy renders inside the pass that writes code; the ${CODE_COMMENTS_STEP} anchor, its fragment and the skill pointer are gone, and prompt-code-comments-inlined owns equality against SKILL.md (issue #3505)."
	codeForgeGitReason   = "CODE_FORGE=git (issue #330) skips PR creation and CI-watch and emits a branch ref where a PR URL would go; the stop step lives in a read-write and read-only fragment pair, so the section carries only the LAND_GIT_STOP_*_STEP references (issue #2526)."
	openDraftReason      = "the draft bit is the readiness signal the entrypoint backstop and launcher trust, so the PR must never open non-draft (issue #1614); the invocation lives in github/open-pr-create.md, so the section carries only the OPEN_PR_CREATE_DIRECT_STEP reference (issue #1919)."
	outcomeLandingReason = "the Driver's OUTCOME step prints status=ready without ever calling `gh pr ready`, because the launcher owns the draft to ready flip (issue #1651); the print instruction lives in the OUTCOME_LANDING_READ_*_STEP fragments (issues #1653, #3224)."
	socketNoMarkerReason = "the socket carrier prints no PR-intent marker line, so its fragments must not point back at one (issue #1653)."
	outcomeGrammarReason = "an agent following the prompt must see the accepted grammar and status enum, not infer it from one worked example; landing is <landing-ref> because it carries a branch name, not a PR URL, under read-only (issues #1901, #1919)."
	outcomeColonReason   = "the parser tolerates a colon after the token, so the prompt must not list it as an invalid (lost) form (issues #2012, #5298)."
	outcomePrefixReason  = "lastInLog only recognizes a line that starts with the prefix, so text before the token hides the whole line (issue #1901); the rule's own wording and the column-one consequence are pinned (issue #3224)."
	outcomeEnumReason    = "Parse() does not enforce a status enum, so ready or blocked is a prompt-level contract and the prompt must call a value like SUCCESS wrong, without claiming it is lost since it still parses (issues #1901, #3224)."
	ifBlockedReason      = "the Driver never flips a PR to ready (only the launcher does, at green; issue #1651), so a blocked run has nothing to revert to draft; the closing SPINDRIFT_OUTCOME line renders from a read-write and read-only fragment pair, so both are checked (issues #1653, #1933)."
	fixDraftRevertReason = "the Driver never flips a PR to ready, so the fix prompt's override bullet must not tell a blocked fix pass to convert the PR back to draft; it never left draft (issue #1653)."
	deadSkillReason      = "a prompt naming a skill the Box does not carry is dead text (issue #120)."
	contractBlockReason  = "a prompt missing a harness-owned contract block ships an agent that never emits the outcome line (issue #419)."
)

// goldenRenders assembles every golden cell once and returns the fragment
// text each rendered, plus the nixRenderedSources pseudo-cell.
func goldenRenders(t *testing.T) renderedFragments {
	t.Helper()
	renders := renderedFragments{}
	for _, cell := range goldenCells() {
		c := defaultCell()
		cell.setup(c)
		result, _ := assembleCell(t, c, t.TempDir())
		renders[cell.name] = promptassembly.FragmentRenders(result)
	}
	renders[nixRenderedSources] = nixRenderedSourceRenders(t, renders)
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
