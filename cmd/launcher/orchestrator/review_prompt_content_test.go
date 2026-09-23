package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// This file is a content-invariant guard for issue #2458 (the
// review-prompt.md severity contract), distinct from the marker-parity
// guard in markers_test.go: these tests assert prose the model reads, not
// literals a Go constant must match.

// assertRawOrder checks raw byte-offset ordering (issue #3228) because a
// normalizeWhitespace-based Contains check sees only whether a clause is
// present, not where in the file it lands. The why argument goes into the
// failure message so each call site states its own rationale.
func assertRawOrder(t *testing.T, raw, first, second, why string) {
	t.Helper()

	firstIdx := strings.Index(raw, first)
	if firstIdx == -1 {
		t.Fatalf("review-prompt.md no longer contains %q", first)
	}
	secondIdx := strings.Index(raw, second)
	if secondIdx == -1 {
		t.Fatalf("review-prompt.md no longer contains %q", second)
	}
	if firstIdx >= secondIdx {
		t.Errorf("%q must appear strictly before %q %s, got first at byte %d, second at byte %d", first, second, why, firstIdx, secondIdx)
	}
}

// TestReviewPromptSeverityContract guards the Blocking/Non-blocking severity
// contract in review-prompt.md (issue #2458). Each load-bearing clause is its
// own case so a harmless reword fails only that case instead of one brittle
// whole-paragraph string.
func TestReviewPromptSeverityContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "default is BLOCK prior",
			clause: "your default is BLOCK, and APPROVE must be earned",
		},
		{
			name:   "rubber-stamp warning",
			clause: "A rubber-stamp that misses a real defect is a worse failure than a false alarm",
		},
		{
			name:   "BLOCK reserved for categories above",
			clause: "BLOCK stays reserved for the categories above",
		},
		{
			name:   "prose findings are Non-blocking, save egregious comment-to-code disproportion",
			clause: "wording, style, redundancy, and ordering findings on prose the diff touches — commit messages, comments, and docs — are Non-blocking, with one exception",
		},
		{
			name:   "#2880 egregious comment-to-code disproportion may be Blocking",
			clause: "an egregious comment-to-code disproportion, where comment volume plainly dwarfs the change it documents (not merely longer than the reviewer would have written), may be Blocking",
		},
		{
			name:   "#2436 example: repeated phrase",
			clause: "a phrase repeated within one sentence",
		},
		{
			name:   "#2436 example: tautological clause",
			clause: "a tautological clause",
		},
		{
			name:   "#2436 example: trailer placement",
			clause: "where a trailer sits among the commits",
		},
		{
			name:   "#2550 seeded section is not narrative to discard",
			clause: `A "## Prior-round claims to verify" section below this prompt`,
		},
		{
			name:   "#2696 Severity Blocking: new-logic coverage still blocks, points at the exemption",
			clause: "**Blocking** — spec violations, correctness bugs, security issues, missing or inadequate tests for the new logic (untested new logic blocks on its own — the one exemption is in the Non-blocking bullet below), standards violations that break the build or documented rules",
		},
		{
			name:   "#2696 Severity Non-blocking: already-covered exemption routed here explicitly",
			clause: "missing or inadequate tests for a pure relocation, refactor, or comment/doc change whose behaviour is already covered under test",
		},
		{
			name:   "#3228 Blocking demands a one-line failure scenario",
			clause: "State every Blocking finding as one concrete failure scenario: the triggering input or state, and the wrong outcome it produces",
		},
		{
			name:   "#3228 Blocking: constructing the scenario is the depth-forcing exercise",
			clause: "constructing that scenario is the depth-forcing exercise, not a label",
		},
		{
			name:   "#3228 Non-blocking corollary: no scenario means Non-blocking by definition",
			clause: "A finding that cannot state that one-line failure scenario is Non-blocking by definition",
		},
		{
			name:   "#3228 Non-blocking corollary: stops weaker models stretching the fix loop over nits",
			clause: "the rule keeps a weaker model from blocking on nits and stretching the fix loop",
		},
		{
			// #3226 slice 2: these Non-blocking carve-outs had no pin of
			// their own, so an editorial tightening pass could drop them
			// silently.
			name:   "#3226 Non-blocking: smells/nits/style/suggestions named as their own bucket",
			clause: "smells, nits, style, suggestions",
		},
		{
			name:   "#3226 Non-blocking: Conventional Commits named as the worked Blocking-standards example",
			clause: "a Conventional Commits format violation, say, is a standards violation",
		},
		{
			name:   "#3226 Non-blocking: ordinary verbosity is not a finding on its own",
			clause: "Ordinary verbosity stays Non-blocking",
		},
		{
			name:   "#3226 Non-blocking: every finding surfaces, none gate the merge",
			clause: "Surface every finding — they don't gate the merge",
		},
		{
			// #3610 widened this clause from the binary fix/escalate pair to
			// the triage's three outcomes, so the pin tracks the new wording.
			name:   "#3226/#3610 Non-blocking: fix, drop, or escalate as the three outcomes",
			clause: "the work loop fixes cheap, in-scope ones, drops a correct but trivial, out-of-scope one without filing it, and escalates only what genuinely needs a human",
		},
		{
			name:   "#3226 Non-blocking: don't dress a cheap fix up as Blocking",
			clause: "don't dress a one-line fix up as a blocking finding",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}

// TestReviewPromptCorrectnessCoverageClause guards the #2696 CORRECTNESS
// coverage-scoping clause, which #3226 keeps in review-prompt.md as
// always-inline prose: an obligation gated behind the
// CODE_REVIEW_BAKED/UNBAKED pair vanishes on exactly the baked runs that
// defer to a pinned upstream skill.
func TestReviewPromptCorrectnessCoverageClause(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	clause := "A pure relocation, refactor, or comment/doc change whose behaviour is already covered under test is not a coverage defect — note it under Non-blocking rather than Blocking"
	if !strings.Contains(normalized, normalizeWhitespace(clause)) {
		t.Errorf("review-prompt.md no longer states %q", clause)
	}
}

// TestReviewPromptInputsDiffDiscipline guards the review pass's Inputs block
// (issue #3215): the main loop reads a --stat summary plus targeted hunks from
// a diff written to disk, never the whole diff into its own conversation. The
// `/code-review` reviewer subagents still each read the full diff in their own
// context, so this pins the main loop's prose only.
func TestReviewPromptInputsDiffDiscipline(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "--stat summary read first",
			clause: "git diff origin/${BASE_BRANCH}...HEAD --stat",
		},
		{
			name:   "full diff redirected to a file on disk",
			clause: "git diff origin/${BASE_BRANCH}...HEAD > /tmp/review-diff.patch",
		},
		{
			name:   "targeted hunks, never the whole file",
			clause: "grep or read targeted hunks out of /tmp/review-diff.patch — never read that file whole into context",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}

// TestReviewPromptStandardsGrepGuidance guards the STANDARDS & SMELLS
// dimension (issue #3215): reviewers used to read the whole
// contributing-guidelines document fresh every pass (12,233 chars on the
// dogfooded Target repo). #3226 keeps the paragraph inline in review-prompt.md
// because the baked arm defers to a pinned upstream skill spindrift cannot edit.
func TestReviewPromptStandardsGrepGuidance(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "grep the standards for the implicated rule",
			clause: "Grep that document for the rule the diff implicates",
		},
		{
			name:   "read only the relevant section, not the whole document",
			clause: "read only that section — do not read the whole document fresh",
		},
		{
			name:   "#3215 finding 1: 'that document' has a real antecedent",
			clause: "whatever document the repo records them in",
		},
		{
			name:   "#3215 finding 2: carry the grep-don't-read-whole rule into composed subagent prompts",
			clause: "If you compose a subagent prompt for this dimension (e.g. when driving `/code-review`'s Standards axis), carry the same grep-don't-read-whole rule into that prompt too",
		},
		{
			name:   "#2880 STANDARDS & SMELLS names comment-to-code disproportion as a smell to hunt",
			clause: "misleading names, swallowed errors, magic values, comments that lie, comment-to-code disproportion, and anything that will rot",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}

// TestReviewPromptPhasedHunt guards the hunt-dimension ordering rule (issue
// #3228). Without it, a smell noticed early crowds the reviewer's attention and
// the ~40-line output cap ahead of the defects CORRECTNESS and SECURITY exist
// to catch.
func TestReviewPromptPhasedHunt(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	clause := "Hunt CORRECTNESS and SECURITY to completion before you record a single STANDARDS & SMELLS finding"
	if !strings.Contains(normalized, normalizeWhitespace(clause)) {
		t.Errorf("review-prompt.md no longer orders the hunt (%q)", clause)
	}
}

// TestReviewPromptTraceObligations guards the four trace obligations (issue
// #3228), the diff shapes a reviewer must read beyond the hunk to judge. The
// #3142 escape behind the rename bullet was a `ReplaceAll` whose new form
// collided with an existing host name, which only a tree-wide search for both
// forms would have caught. Each obligation is a separate case.
func TestReviewPromptTraceObligations(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "rename or mass replacement: search old AND new forms",
			clause: "grep the tree for both the old and new forms",
		},
		{
			name:   "changed signature: read every caller",
			clause: "read every caller, not just the definition",
		},
		{
			name:   "concurrency-adjacent: name shared state and walk one interleaving",
			clause: "name the shared state and walk one interleaving by hand",
		},
		{
			name:   "new error path: trace propagation",
			clause: "trace where it propagates to",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}

// TestReviewPromptBlockingOutputShapeCarriesFailureScenario guards the
// `## Blocking` example line in the Output block (issue #3228): the example
// itself, not just the severity-rule prose above it, carries the failure
// scenario, so a model pattern-matching the line lands on the right shape.
func TestReviewPromptBlockingOutputShapeCarriesFailureScenario(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	clause := "file:line — the failure scenario: input/state → wrong outcome"
	if !strings.Contains(normalized, normalizeWhitespace(clause)) {
		t.Errorf("review-prompt.md's Output block no longer shapes the Blocking example line around a failure scenario (%q)", clause)
	}
}

// TestReviewPromptApproveProbedSection guards the APPROVE probed section
// (issue #3228). Without it, APPROVE is a bare assertion the model can emit
// without having done the hunt. Heading, body and governing prose are separate
// cases so a reword that drops one piece fails only that case.
func TestReviewPromptApproveProbedSection(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "Probed heading, scoped to APPROVE",
			clause: "## Probed (APPROVE only)",
		},
		{
			name:   "Probed body names dimensions and trace obligations run clean",
			clause: "name each hunt dimension and trace obligation you ran that came back clean",
		},
		{
			name:   "governing prose: Probed section is the receipt, not an assertion",
			clause: "this is the receipt that turns APPROVE into work done, not an assertion taken on faith",
		},
		{
			name:   "governing prose: Probed section omitted on BLOCK",
			clause: "Omit the Probed section on BLOCK; the Blocking findings already are that receipt",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}

// TestReviewPromptApproveProbedSectionAfterVerdictLine pins the probed section
// strictly below the `VERDICT: APPROVE | BLOCK` line (issue #3228), keeping the
// verdict the first line of the final message (ADR 0035).
func TestReviewPromptApproveProbedSectionAfterVerdictLine(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	raw := readPromptFile(t, repoRoot, "review-prompt.md")

	assertRawOrder(t, raw, "VERDICT: APPROVE | BLOCK", "## Probed (APPROVE only)", "so the verdict stays the first line of the final message")
}
