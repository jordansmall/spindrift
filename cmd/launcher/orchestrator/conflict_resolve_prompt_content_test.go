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
			name:   "#3225 no narration between tool calls",
			clause: "Do not narrate between tool calls; the only text you output is the short explanation described below if the conflict is unresolvable",
		},
		{
			name:   "#3225 rebase complete signal is rebase-merge/rebase-apply gone",
			clause: "The rebase is complete when `.git/rebase-merge` and `.git/rebase-apply` directories no longer exist",
		},
		{
			name:   "#3225 unresolvable conflict: exit and explain",
			clause: "If the conflict is genuinely unresolvable (e.g. the two changes are semantically incompatible), exit and explain in a short message",
		},
	})
}

// Issue #3505 inlines the code-comments skill's policy body verbatim before
// # SIGNALS instead of routing through the ${CODE_COMMENTS_STEP} fragment
// anchor (the same shape worker-prompt.md already uses, issue #3419).
func TestConflictResolvePromptCodeCommentsPolicyInlined(t *testing.T) {
	assertInlinesCodeCommentsPolicy(t, "conflict-resolve-prompt.md")
}
