package main

import (
	"testing"
)

// Issue #3225 reviewed conflict-resolve-prompt.md and found no design-history
// prose to cut: the file is all contract and sequencing. Pinning each clause of
// its resolution procedure here means a later cut cannot silently take a rule
// with it.
func TestConflictResolvePromptOperativeContract(t *testing.T) {
	assertPromptClauses(t, "conflict-resolve-prompt.md", []promptClause{
		{
			name:   "#3225 never hand-merge conflict markers inside a generated file",
			clause: "never hand-merge the conflict markers inside it",
		},
		{
			name:   "#3225 resolve in the source of truth merging both sides' intent",
			clause: "Resolve the conflict in its source of truth instead — the input(s) the header or the repo's own documentation say it's generated from — merging both sides' intent there",
		},
		{
			name:   "#3225 regenerate using the command the header or docs specify and stage the output",
			clause: "regenerate the artifact using whatever command the file's header or the repo's documentation specifies, and stage the regenerated output in place of the conflicted file",
		},
		{
			name:   "#3225 ordinary file: resolve directly, choose or merge as history demands",
			clause: "resolve it directly — choose the correct version or merge both sides as the change history demands",
		},
		{
			name:   "#3225 complete the rebase with GIT_EDITOR=true git rebase --continue",
			clause: "Complete the rebase: `GIT_EDITOR=true git rebase --continue`",
		},
		{
			name:   "#3225 repeat if more conflicts from subsequent commits",
			clause: "Repeat if `git status` shows more conflicts from subsequent commits",
		},
		{
			name:   "#3225 do not open a PR or push, the caller handles that",
			clause: "Do NOT open a PR or push — the caller handles that",
		},
		{
			name:   "#5096 the Box ships no python",
			clause: "The Box ships no `python` or `python3`",
		},
		{
			name:   "#5096 resolve hunks with Edit, sed, or git checkout --ours/--theirs",
			clause: "Resolve hunks with the Edit tool, `sed`, or, to take one whole side of a file, `git checkout --ours <file>` / `git checkout --theirs <file>`",
		},
		{
			name:   "#5096 rebase swaps --ours and --theirs",
			clause: "`--ours` is the base you are rebasing onto and `--theirs` is the branch's own commit",
		},
		{
			name:   "#5096 grep for leftover conflict markers before rebase --continue",
			clause: "Grep the tree for leftover conflict markers before continuing, e.g. `git grep -nE '^(<<<<<<<|=======|>>>>>>>)( |$)'`, and resolve every hit that is a conflict marker",
		},
		{
			name:   "#5096 run the full check gate after the rebase finishes",
			clause: "run the repo's full check gate — the same gate the main run uses (its scoped in-Box form where the repo's docs define one)",
		},
		{
			name:   "#5096 check-skill anchors precede the numbered steps",
			clause: "${CHECK_HYGIENE_STEP}${NIX_CHECKS_STEP}1. Run `git status`",
		},
		{
			name:   "#5096 fix what the check surfaces and amend into the owning commit",
			clause: "Fix what it surfaces and amend each fix into the commit it belongs to: `git commit --amend` for the tip commit, or `git commit --fixup=<sha>` then `GIT_SEQUENCE_EDITOR=true git rebase -i --autosquash origin/${BASE_BRANCH}` for an older one",
		},
		{
			name:   "#5096 a stalled autosquash rebase is aborted back to the completed rebase",
			clause: "If the step 6 autosquash rebase itself stalls on a conflict, run `git rebase --abort`: it restores the completed rebase with the fixup commit on top",
		},
		{
			name:   "#5096 still-red check: finish anyway with the rebase complete",
			clause: "If the check is still red after your own attempts, finish anyway with the rebase complete",
		},
		{
			name:   "#5096 never leave a rebase in progress to signal a red check",
			clause: "Never leave a rebase in progress to signal a red check",
		},
		{
			name:   "#5096 the host's post-force-push fix pass owns the remaining repair",
			clause: "the host's post-force-push fix pass owns the remaining repair",
		},
		{
			name:   "#5096 the final message is full prose for the human reading the Box log",
			clause: "Write that final message as full, uncompressed prose",
		},
		{
			name:   "#5096 final message is the pass summary naming the check and its color",
			clause: "one short final message: the check you ran and whether it ended green or red (naming each still-red check)",
		},
		{
			name:   "#3225 no narration between tool calls",
			clause: "Do not narrate between tool calls; the only text you output is one short final message",
		},
		{
			name:   "#3225 rebase complete signal is rebase-merge/rebase-apply gone",
			clause: "The rebase is complete when `.git/rebase-merge` and `.git/rebase-apply` directories no longer exist",
		},
		{
			name:   "#3225 unresolvable conflict: exit and explain",
			clause: "If the conflict is genuinely unresolvable (e.g. the two changes are semantically incompatible), exit and explain in a short message",
		},
		{
			name:   "#3505 code-comments policy phrase present",
			clause: codeCommentsPolicyPhrase,
		},
	})
}
