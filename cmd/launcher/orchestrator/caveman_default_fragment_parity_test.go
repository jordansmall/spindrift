package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCavemanDefaultFragmentParity guards the four caveman-default fragments
// against drift (issue #2753). It asserts only the spans they genuinely share,
// not whole-file equality: worker narrows the commit-message exemption and drops
// the marker paragraphs (#3419, #2706), review omits SPINDRIFT_ISSUE_INTENT
// (#2707), and research paraphrases base (#2708).
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
	// Base is checked here too, not just review: TestCavemanDefaultFragmentContract
	// does not pin every clause in full, so a base-only drift would pass silently.
	baseAndReview := []fragment{baseFragment, reviewFragment}

	assertClauseIn := func(t *testing.T, fragments []fragment, clause string) {
		t.Helper()
		normalizedClause := normalizeWhitespace(clause)
		for _, f := range fragments {
			if !strings.Contains(f.content, normalizedClause) {
				t.Errorf("%s no longer states %q", f.name, clause)
			}
		}
	}

	// Named rather than inlined into its table row below: the worker and
	// research subtests at the end of this function reuse it as a negative
	// assertion.
	const commitMessageClause = "Code, commands, error messages, and commit messages are exempt and stay " +
		"verbatim. Never route a commit message through `/caveman` or otherwise " +
		"compress it — commit messages are always full human-quality prose."

	// Worker (issue #3419) and research (issue #2708) both narrow the opening
	// exemption to this shorter line instead of the commit-message clause above,
	// because neither role ever writes a commit message.
	const narrowerOpeningExemption = "Code, commands, and error messages are exempt and stay verbatim."

	cases := []struct {
		name      string
		clause    string
		fragments []fragment
	}{
		{
			name:      "opening /caveman directive is shared verbatim by all four fragments",
			clause:    "Default to the `/caveman` skill for all narration and prose output this run.",
			fragments: allFragments,
		},
		{
			name:      "commit-message exemption clause is shared verbatim by base and review",
			clause:    commitMessageClause,
			fragments: baseAndReview,
		},
		{
			// Stops short of the `or SPINDRIFT_ISSUE_INTENT` clause that follows,
			// which review legitimately omits.
			name: "marker-grammar-exemption intro is shared verbatim by base and review",
			clause: "The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME` " +
				"line, the `VERDICT: APPROVE` / `VERDICT: BLOCK` line, and any host-relay signal line such as " +
				"`SPINDRIFT_PR_INTENT`",
			fragments: baseAndReview,
		},
		{
			name: "marker-grammar note=/blocked-stop clause is shared verbatim by base and review",
			clause: "Never route these through `/caveman`. Specifically, the `note=` field of " +
				"the SPINDRIFT_OUTCOME line is exempt and stays human-quality prose, same tier as a commit message " +
				"— on a blocked or ambiguous stop it is posted verbatim as a comment on the tracker issue, so " +
				"caveman-compressing it ships caveman prose straight to a human reader.",
			fragments: baseAndReview,
		},
		{
			name: "marker-shape requirement clause is shared verbatim by base and review",
			clause: "Where a marker line is the carrier, it must keep its required shape exactly intact " +
				"— never reworded, reflowed, or line-wrapped: the leading token, the outcome line's key=value " +
				"pairs, and the PR-intent line's nonce and base64 payload as one unbroken token. The verdict line " +
				"must additionally remain the first line of the agent's final message.",
			fragments: baseAndReview,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertClauseIn(t, c.fragments, c.clause)
		})
	}

	// Worker and research legitimately lack the commit-message clause: the
	// coordinator owns COMMIT (issue #3419) and a research dispatch never
	// commits. Each asserts its narrower wording directly, plus an absence check
	// so a regrown commit-message clause cannot pass silently.
	narrowedFragments := []struct {
		name      string
		fragment  fragment
		rationale string
	}{
		{
			name:      "worker's narrower opening exemption stands in for the commit-message clause",
			fragment:  workerFragment,
			rationale: "if a worker now writes commit messages, update this test's exclusion rationale",
		},
		{
			name:      "research's narrower opening exemption stands in for the commit-message clause",
			fragment:  researchFragment,
			rationale: "if research now commits, update this test's exclusion rationale",
		},
	}

	for _, c := range narrowedFragments {
		t.Run(c.name, func(t *testing.T) {
			assertClauseIn(t, []fragment{c.fragment}, narrowerOpeningExemption)
			if strings.Contains(c.fragment.content, normalizeWhitespace(commitMessageClause)) {
				t.Errorf("%s unexpectedly gained the commit-message exemption clause; %s", c.fragment.name, c.rationale)
			}
		})
	}
}
