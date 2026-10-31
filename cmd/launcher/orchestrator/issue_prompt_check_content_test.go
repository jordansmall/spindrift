package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Issue #3215 extended the CHECK section's redirect-to-file rule from build
// logs to diffs, because a bare `git diff` streamed to the conversation hits
// the same tool-result truncation cap. mkHarness injects the CHECK block
// verbatim into fix-prompt.md and mkharness-prompt-fix-check-no-drift pins
// that propagation, so this test only checks issue-prompt.md.
func TestIssuePromptCheckDiffRedirectDiscipline(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "issue-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "never stream a bare git diff",
			clause: "never stream a bare `git diff` into the conversation",
		},
		{
			name:   "write the diff to a file and read --stat first",
			clause: "Write the diff to a file, read `--stat` first for shape",
		},
		{
			name:   "then read targeted hunks or grep the file",
			clause: "then read targeted hunks or grep that file",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("issue-prompt.md CHECK section no longer states %q", c.clause)
			}
		})
	}
}

// Issue #3505 inlines the code-comments policy into IMPLEMENT in place of the
// ${CODE_COMMENTS_STEP} anchor.
func TestIssuePromptCodeCommentsPolicyPhrase(t *testing.T) {
	assertPromptClauses(t, "issue-prompt.md", []promptClause{
		{
			name:   "#3505 code-comments policy phrase present",
			clause: codeCommentsPolicyPhrase,
		},
	})
}

// Issue #5129: the CHECK block is also baked into fix-prompt.md, and the
// orchestrator stops implement/fix passes right after COMMIT with no outcome,
// so the terminal-outcome mandate must be scoped to the pass that owns it.
func TestIssuePromptCheckOutcomeScopedToOwningPass(t *testing.T) {
	assertPromptClauses(t, "issue-prompt.md", []promptClause{
		{
			name:   "obligation attaches to a pass that reaches a terminal stop",
			clause: "Do not stop a pass that reaches OUTCOME, IF BLOCKED, or an earlier gate's terminal stop until its `SPINDRIFT_OUTCOME` line has been printed",
		},
		{
			name:   "a pass told to stop after COMMIT prints none unless blocked",
			clause: "a pass told to stop right after COMMIT prints none unless it hit IF BLOCKED",
		},
	})

	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "issue-prompt.md"))
	lowered := strings.ToLower(normalized)
	for _, stale := range []string{
		"until a terminal `SPINDRIFT_OUTCOME` line (`status=ready` or `status=blocked`)",
		"do not stop this run",
	} {
		if strings.Contains(lowered, strings.ToLower(normalizeWhitespace(stale))) {
			t.Errorf("issue-prompt.md still carries the unscoped outcome mandate %q", stale)
		}
	}
}

// Issue #5285: the IF BLOCKED intro is ungated, so it must not name a push
// retry a CODE_FORGE=local read-write Box never performs. The push case lives
// in the gated fragments/if-blocked-triage-git.md.
func TestIssuePromptIfBlockedIntroIsPushNeutral(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "issue-prompt.md"))

	start := strings.Index(normalized, "# IF BLOCKED")
	if start < 0 {
		t.Fatal("issue-prompt.md has no # IF BLOCKED heading")
	}
	end := strings.Index(normalized[start:], "${IF_BLOCKED_")
	if end < 0 {
		t.Fatal("issue-prompt.md has no ${IF_BLOCKED_ placeholder after # IF BLOCKED")
	}
	if intro := normalized[start : start+end]; strings.Contains(strings.ToLower(intro), "push") {
		t.Errorf("issue-prompt.md IF BLOCKED intro must not mention a push; got %q", intro)
	}
}

// Issue #6238: the push-triage fragment renders on every BOX_PUSH_READ_WRITE
// tracker, including jira and local where the Box cannot comment. Reporting is
// deferred to step 4 of IF BLOCKED, which forks per tracker.
func TestIfBlockedTriageGitDefersReportToStep4(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/if-blocked-triage-git.md"))

	if strings.Contains(strings.ToLower(normalized), "comment on the issue") {
		t.Error("if-blocked-triage-git.md tells the agent to comment on the issue; jira/local Boxes cannot, so defer to step 4")
	}
	if !strings.Contains(normalized, "step 4") {
		t.Error("if-blocked-triage-git.md must defer the workflow-scope report to step 4")
	}
}
