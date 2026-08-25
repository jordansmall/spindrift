package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCavemanDefaultFragmentParity is a cross-fragment parity guard (issue
// #2753) for the four caveman-default fragment variants in
// templates/default/prompts/fragments/: caveman-default.md (base),
// caveman-default-worker.md, caveman-default-review.md, and
// caveman-default-research.md.
//
// A naive "assert all four fragments are byte-identical" test would be
// wrong, because three of the four legitimately diverge from base:
//
//   - caveman-default-worker.md keeps only base's first four lines (the
//     /caveman directive and the code/commands/error-messages/commit-messages
//     exemption); it drops the marker-grammar-exemption and
//     marker-shape-requirement paragraphs entirely, because worker-prompt.md
//     is structurally quarantined from the outcome/verdict marker grammar
//     (issue #2706).
//   - caveman-default-review.md keeps the marker-grammar-exemption and
//     marker-shape-requirement paragraphs from base (issue #2707), modulo
//     whitespace/re-wrapping, minus one clause: it never names
//     `SPINDRIFT_ISSUE_INTENT`, because the review agent itself never emits
//     that marker (a review run's non-blocking findings instead reach a
//     Filer subagent, whose own prompt -- filer-file-relay.md -- is what
//     emits SPINDRIFT_ISSUE_INTENT). It also appends a whole
//     `## Blocking`/`## Non-blocking` exemption paragraph base does not have.
//   - caveman-default-research.md is a deliberate paraphrase (added by issue
//     #2708, after this issue was filed): it drops the commit-message
//     exemption clause entirely -- a research dispatch never commits, so
//     there is nothing to exempt -- and substitutes `SPINDRIFT_COMMENT` for
//     `SPINDRIFT_PR_INTENT` / `SPINDRIFT_ISSUE_INTENT` as its host-relay
//     signal line, since research posts a comment rather than a PR-intent or
//     verdict marker.
//
// That is why this test scopes its verbatim-clause assertions to only the
// spans genuinely shared across the relevant fragments, rather than diffing
// whole files: the opening /caveman directive line (all four); the
// code/commands/error-messages/commit-messages exemption clause (base,
// worker, and review); and the marker-grammar-exemption-intro and
// marker-shape-requirement paragraphs in caveman-default.md (base and
// review only, minus the SPINDRIFT_ISSUE_INTENT clause review legitimately
// omits). Each of these spans is shared real content, not incidental
// overlap -- either base's own source text, a verbatim-modulo-whitespace
// duplicate of it, or (for research) a deliberate narrower paraphrase -- so
// none is exempt from being checked, even where
// TestCavemanDefaultFragmentContract also happens to pin base's own copy of
// the same clause: mutation-testing this file by deleting live sentences
// from base while leaving worker/review's copies intact confirmed that
// skipping base here would let a base-only drift pass silently, since
// TestCavemanDefaultFragmentContract does not pin every clause in full.
// Research's narrower wording is asserted separately in its own subtest
// below, and confirmed there to omit the commit-message clause the other
// three share.
func TestCavemanDefaultFragmentParity(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	type fragment struct {
		name    string
		content string
	}
	readFragment := func(name string) fragment {
		return fragment{name, normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/"+name))}
	}

	baseFragment := readFragment("caveman-default.md")
	workerFragment := readFragment("caveman-default-worker.md")
	reviewFragment := readFragment("caveman-default-review.md")
	researchFragment := readFragment("caveman-default-research.md")

	allFragments := []fragment{baseFragment, workerFragment, reviewFragment, researchFragment}
	baseAndReview := []fragment{baseFragment, reviewFragment}
	baseWorkerReview := []fragment{baseFragment, workerFragment, reviewFragment}

	assertClauseIn := func(t *testing.T, fragments []fragment, clause string) {
		t.Helper()
		normalizedClause := normalizeWhitespace(clause)
		for _, f := range fragments {
			if !strings.Contains(f.content, normalizedClause) {
				t.Errorf("%s no longer states %q", f.name, clause)
			}
		}
	}

	const openingDirective = "Default to the `/caveman` skill for all narration and prose output this run."

	t.Run("opening /caveman directive is shared verbatim by all four fragments", func(t *testing.T) {
		assertClauseIn(t, allFragments, openingDirective)
	})

	const commitMessageClause = "Code, commands, error messages, and commit messages are exempt and stay " +
		"verbatim. Never route a commit message through `/caveman` or otherwise " +
		"compress it — commit messages are always full human-quality prose."

	t.Run("commit-message exemption clause is shared verbatim by base, worker, and review", func(t *testing.T) {
		// Deliberately excludes caveman-default-research.md -- see the
		// function doc comment above.
		assertClauseIn(t, baseWorkerReview, commitMessageClause)
	})

	const markerGrammarIntro = "The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME` " +
		"line, the `VERDICT: APPROVE` / `VERDICT: BLOCK` line, and any host-relay signal line such as " +
		"`SPINDRIFT_PR_INTENT`"

	t.Run("marker-grammar-exemption intro is shared verbatim by base and review", func(t *testing.T) {
		// Deliberately stops short of the `or SPINDRIFT_ISSUE_INTENT` clause
		// that follows: review legitimately omits it -- see the function doc
		// comment above.
		assertClauseIn(t, baseAndReview, markerGrammarIntro)
	})

	const markerGrammarNoteClause = "Never route these through `/caveman`. Specifically, the `note=` field of " +
		"the SPINDRIFT_OUTCOME line is exempt and stays human-quality prose, same tier as a commit message " +
		"— on a blocked or ambiguous stop it is posted verbatim as a comment on the tracker issue, so " +
		"caveman-compressing it ships caveman prose straight to a human reader."

	t.Run("marker-grammar note=/blocked-stop clause is shared verbatim by base and review", func(t *testing.T) {
		assertClauseIn(t, baseAndReview, markerGrammarNoteClause)
	})

	const markerShapeClause = "Every exempted marker line above must keep its required shape exactly intact " +
		"— never reworded, reflowed, or line-wrapped: the leading token, the outcome line's key=value " +
		"pairs, and the PR-intent line's nonce and base64 payload as one unbroken token. The verdict line " +
		"must additionally remain the first line of the agent's final message."

	t.Run("marker-shape requirement clause is shared verbatim by base and review", func(t *testing.T) {
		assertClauseIn(t, baseAndReview, markerShapeClause)
	})

	t.Run("research's narrower opening exemption stands in for the commit-message clause", func(t *testing.T) {
		// caveman-default-research.md legitimately has no commit-message
		// clause (research dispatches never commit); it instead exempts
		// only code, commands, and error messages. Assert that narrower
		// clause directly, rather than silently asserting nothing at all for
		// research's second line, and confirm research did not unexpectedly
		// gain the commit-message clause it deliberately lacks.
		const researchOpeningExemption = "Code, commands, and error messages are exempt and stay verbatim."
		assertClauseIn(t, []fragment{researchFragment}, researchOpeningExemption)
		if strings.Contains(researchFragment.content, normalizeWhitespace(commitMessageClause)) {
			t.Errorf("caveman-default-research.md unexpectedly gained the commit-message exemption clause; " +
				"if research now commits, update this test's exclusion rationale")
		}
	})
}
