package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConflictResolvePromptOperativeContract is a content-invariant guard
// (issue #3225) for conflict-resolve-prompt.md's numbered resolution
// procedure: the generated-file never-hand-merge rule and its
// resolve-in-source-of-truth/regenerate/stage sequence, the ordinary-file
// rule, the rebase-continue and repeat-on-further-conflicts steps, the
// no-PR-or-push boundary, the no-narration rule, and the two completion/
// unresolvable signals. Issue #3225 reviewed this prompt and found it
// already all contract and sequencing with no design-history prose to cut;
// pinning each clause here first means a future cut can't silently take a
// rule with it. One case per clause, so a reword of one clause fails only
// its own case instead of the whole paragraph.
func TestConflictResolvePromptOperativeContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "conflict-resolve-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("conflict-resolve-prompt.md no longer states %q", c.clause)
			}
		})
	}
}
