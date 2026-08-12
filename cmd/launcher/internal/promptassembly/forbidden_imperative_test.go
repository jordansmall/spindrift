package promptassembly

import "testing"

// TestForbiddenMarkerIsImperative_FencedCodeBlock covers the fenced-block
// shape: a marker appearing on a line inside a fenced code block is always
// imperative, negation or not -- a fence is presented as literal commands to
// run.
func TestForbiddenMarkerIsImperative_FencedCodeBlock(t *testing.T) {
	text := "Run this:\n```\ngit push\n```\n"
	if !ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, %q) = false, want true", "git push", text)
	}
}

// TestForbiddenMarkerIsImperative_NumberedStepNoNegation covers a numbered
// list item whose command is exactly the marker, no negation -- modeled on
// the three-line numbered list that existed in
// templates/default/prompts/fragments/commit-push-git.md before #2462 split
// it out (see `git show b7523422 -- templates/default/prompts/issue-prompt.md`).
func TestForbiddenMarkerIsImperative_NumberedStepNoNegation(t *testing.T) {
	text := "1. `git fetch origin`\n" +
		"2. `git rebase origin/${BASE_BRANCH}` -- resolve any conflicts, re-run checks.\n" +
		"3. `git push --force-with-lease` -- one retry only.\n"
	if !ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, %q) = false, want true", "git push", text)
	}
}

// TestForbiddenMarkerIsImperative_BulletedStepNoNegation covers the same
// shape as the numbered-step case, but with a `-` bullet.
func TestForbiddenMarkerIsImperative_BulletedStepNoNegation(t *testing.T) {
	text := "- `git fetch origin`\n" +
		"- `git rebase origin/${BASE_BRANCH}` -- resolve any conflicts, re-run checks.\n" +
		"- `git push --force-with-lease` -- one retry only.\n"
	if !ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, %q) = false, want true", "git push", text)
	}
}

// TestForbiddenMarkerIsImperative_NegatedNumberedStepPasses is the literal
// text from the shipped
// templates/default/prompts/fragments/if-blocked-push-outbox.md, which wraps
// a negation ("do NOT") across a physical line break before the marker --
// this must NOT trip the check.
func TestForbiddenMarkerIsImperative_NegatedNumberedStepPasses(t *testing.T) {
	text := "1. Your token is read-only and you take no code-out action yourself — do NOT\n" +
		"   `git push` and do NOT run `git bundle create` (or note if you have nothing\n" +
		"   committed to hand off). Leave what you have committed on the branch: after\n" +
		"   you exit the harness relays your committed branch out and the launcher\n" +
		"   pushes it host-side with its own token.\n"
	if ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, text) = true, want false", "git push")
	}
}

// TestForbiddenMarkerIsImperative_PlainProseNeverImperative covers a plain
// paragraph (no list, no fence) mentioning the marker un-negated -- modeled
// on templates/default/prompts/fragments/if-blocked-triage-outbox.md. Shape
// doesn't match either forbidden shape, so this must never trip the check,
// negation or not.
func TestForbiddenMarkerIsImperative_PlainProseNeverImperative(t *testing.T) {
	text := "**A denied `git push` here is expected, not itself the blocker.** A read-only\n" +
		"Box holds no push-capable token in the failure path any more than in the\n" +
		"happy path, so a 403 or other permission denial on a write is the outcome you\n" +
		"were always going to get — never diagnose it as a broken or under-scoped\n" +
		"token, and never report it to a human as a token-permission problem needing\n" +
		"`workflow` or any other scope.\n"
	if ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, text) = true, want false", "git push")
	}
}

// TestForbiddenMarkerIsImperative_ConditionalBranchExemption is the literal
// CODE_FORGE=git branch from templates/default/prompts/issue-prompt.md
// (lines ~146-157), which contains a genuinely un-negated numbered
// `git push --force-with-lease -u origin ${BRANCH}` step -- this is not a
// contract gap because the launcher's own startup capability gate
// (cmd/launcher/internal/settle/gate.go) already refuses to run a read-only
// Box under CODE_FORGE=git at all, so this branch never actually applies to
// a read-only Box. The conditional-branch exemption must suppress it.
func TestForbiddenMarkerIsImperative_ConditionalBranchExemption(t *testing.T) {
	text := "Check `$CODE_FORGE` (already in your environment — run `echo $CODE_FORGE` if\n" +
		"unsure):\n" +
		"\n" +
		"**`CODE_FORGE=git`** (push-only Code Forge — no PR, no CI-watch, no merge\n" +
		"gate): skip OPEN A PULL REQUEST below entirely.\n" +
		"\n" +
		"1. `git push --force-with-lease -u origin ${BRANCH}` (if not already pushed).\n" +
		"2. Print exactly one line as your final output and stop — raw plain text, not\n" +
		"   wrapped in backticks, a code fence, or any other markdown formatting:\n" +
		"\n" +
		"   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason>\n" +
		"\n" +
		"   The launcher applies `MERGE_MODE` after this line (push straight to the\n" +
		"   target branch on `immediate`; leave the branch as pushed on `manual`).\n" +
		"   Do NOT run `gh pr create` and do NOT attempt to merge.\n"
	if ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, text) = true, want false", "git push")
	}
}

// TestForbiddenMarkerIsImperative_AbsentMarkerFalse covers a marker that
// doesn't appear anywhere in text.
func TestForbiddenMarkerIsImperative_AbsentMarkerFalse(t *testing.T) {
	text := "Nothing forbidden here, just ordinary prose.\n"
	if ForbiddenMarkerIsImperative("git push", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, text) = true, want false", "git push")
	}
}

// TestForbiddenMarkerIsImperative_MultilineNegationAcrossWrap is the literal
// text from templates/default/prompts/fragments/if-blocked-pr-outbox.md,
// which wraps a negation ("do NOT") across a physical line break before two
// markers on the continuation line -- this must NOT trip the check for
// either marker.
func TestForbiddenMarkerIsImperative_MultilineNegationAcrossWrap(t *testing.T) {
	text := "2. Your token is read-only — do NOT\n" +
		"   `gh pr view` or `gh pr create`. Print\n" +
		"   your intended draft PR's title and body as a single nonce-guarded line\n" +
		"   instead — the launcher finds it by this run's nonce, decodes it, and\n" +
		"   opens the draft PR host-side, once you exit:\n"
	if ForbiddenMarkerIsImperative("gh pr create", text) {
		t.Fatalf("ForbiddenMarkerIsImperative(%q, text) = true, want false", "gh pr create")
	}
}
