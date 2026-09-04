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

// normalizeWhitespace collapses all runs of whitespace/newlines to a single
// space, so these checks survive a harmless re-wrap of a prompt or fragment
// file's prose across lines without asserting anything substantive changed.
// Shared with code_review_default_fragment_content_test.go.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestReviewPromptSeverityContract is a content-invariant guard (issue
// #2458) for the Blocking/Non-blocking severity contract in
// review-prompt.md. Each case is a load-bearing clause the prose must keep
// verbatim (modulo line-wrap whitespace); asserting them separately, rather
// than pinning the whole paragraph as one string, lets a harmless reword of
// one clause fail only that case instead of the entire brittle sentence.
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
			clause: `A "## Prior-round claims to verify" section above this prompt`,
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
			// #3226 slice 2: these Non-blocking carve-outs were prose in
			// review-prompt.md but had no dedicated pin, so an editorial
			// tightening pass could drop them silently. Pinning them here
			// before the tightening is this slice's red step.
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
			name:   "#3226 Non-blocking: work loop fixes cheap findings in place, escalates only what needs a human",
			clause: "the work loop fixes cheap, in-scope ones and escalates only what needs a human",
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

// TestReviewPromptCorrectnessCoverageClause is a content-invariant guard for
// the #2696 CORRECTNESS coverage-scoping clause. Issue #3222 moved this out
// of review-prompt.md into code-review-unbaked.md's CORRECTNESS dimension
// paragraph when the dimensions became the code-review skill's off-arm
// fallback; issue #3226 moved the whole dimension-hunting paragraph back
// into review-prompt.md as always-inline prose, since a depth obligation
// gated behind the CODE_REVIEW_BAKED/UNBAKED pair vanishes on exactly the
// baked runs that defer to a pinned upstream skill. The Severity Non-blocking
// case above still pins the matching routing clause.
func TestReviewPromptCorrectnessCoverageClause(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "review-prompt.md"))

	clause := "A pure relocation, refactor, or comment/doc change whose behaviour is already covered under test is not a coverage defect — note it under Non-blocking rather than Blocking"
	if !strings.Contains(normalized, normalizeWhitespace(clause)) {
		t.Errorf("review-prompt.md no longer states %q", clause)
	}
}

// TestReviewPromptInputsDiffDiscipline is a content-invariant guard (issue
// #3215) for the review pass's own Inputs block: the main loop must read a
// --stat summary plus targeted hunks from a full diff written to disk, never
// stream the whole diff into its own conversation. The Standards/Spec
// reviewer subagents spawned by the `/code-review` skill still each read the
// full diff in their own context — this pins the main loop's prose only.
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

// TestReviewPromptStandardsGrepGuidance is a content-invariant guard (issue
// #3215) for the STANDARDS & SMELLS dimension: reviewers previously read the
// full contributing-guidelines document fresh every pass (12,233 chars on
// the dogfooded Target repo); the dimension must instead point at grepping
// the repo's documented standards for the rule the diff implicates and
// reading only that section. Issue #3222 moved the STANDARDS & SMELLS
// paragraph out of review-prompt.md into code-review-unbaked.md (the
// code-review skill's off-arm fallback); issue #3226 moved it back into
// review-prompt.md as always-inline prose, since the baked arm defers to a
// pinned upstream skill spindrift cannot edit and this depth obligation must
// hold on every run, not only the runs where the skill is absent.
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

// TestReviewPromptIssueReadStepStaysInsideInputsBlock is a content-invariant
// guard (issue #3215) for the Inputs: block's placement. Unlike the prose
// clauses above, ${REVIEW_ISSUE_READ_GITHUB_STEP} is not prose to grep for —
// it expands to an indented `gh issue view ...` input line (see
// fragments/review-issue-read-github.md), so it must render as the fourth
// line inside the Inputs: list, immediately after the `git log` line, not
// after the "Read the --stat summary" paragraph that follows the block. A
// normalized-whitespace Contains check can't see this: it would pass even
// with the placeholder stranded outside Inputs. So this test reads the raw
// (unnormalized) file and asserts byte-offset order directly.
func TestReviewPromptIssueReadStepStaysInsideInputsBlock(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	raw := readPromptFile(t, repoRoot, "review-prompt.md")

	placeholder := "${REVIEW_ISSUE_READ_GITHUB_STEP}"
	prose := "Read the --stat summary"

	placeholderIdx := strings.Index(raw, placeholder)
	if placeholderIdx == -1 {
		t.Fatalf("review-prompt.md no longer contains %q", placeholder)
	}
	proseIdx := strings.Index(raw, prose)
	if proseIdx == -1 {
		t.Fatalf("review-prompt.md no longer contains %q", prose)
	}
	if placeholderIdx >= proseIdx {
		t.Errorf("%q must appear before %q so the issue-read input line stays inside the Inputs: block, got placeholder at byte %d, prose at byte %d", placeholder, prose, placeholderIdx, proseIdx)
	}
}
