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
// space, so these checks survive a harmless re-wrap of review-prompt.md's
// prose across lines without asserting anything substantive changed.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestReviewPromptClassifiesProseFindingsNonBlocking is a content-invariant
// guard (issue #2458): the severity contract must state, discretion-free,
// that wording/style/redundancy/ordering findings on prose the diff touches
// are always Non-blocking, never one of the Blocking categories.
func TestReviewPromptClassifiesProseFindingsNonBlocking(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	reviewPrompt := readPromptFile(t, repoRoot, "review-prompt.md")
	const proseNonBlockingRule = "Wording, style, redundancy, and ordering findings on prose the diff touches — commit messages, comments, and docs — are always Non-blocking, never one of the Blocking categories above"
	if !strings.Contains(normalizeWhitespace(reviewPrompt), normalizeWhitespace(proseNonBlockingRule)) {
		t.Errorf("review-prompt.md no longer states %q, the discretion-free rule keeping prose wording/style/redundancy/ordering findings Non-blocking", proseNonBlockingRule)
	}
}

// TestReviewPromptKeepsProseExamples guards the three concrete #2436
// examples named in the severity contract's prose-non-blocking rule -- a
// phrase repeated within one sentence, a tautological clause, and where a
// trailer sits among the commits -- so those can't be silently deleted
// while the general rule sentence survives.
func TestReviewPromptKeepsProseExamples(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	reviewPrompt := readPromptFile(t, repoRoot, "review-prompt.md")
	normalized := normalizeWhitespace(reviewPrompt)

	examples := []string{
		"a phrase repeated within one sentence",
		"a tautological clause",
		"where a trailer sits among the commits",
	}
	for _, example := range examples {
		if !strings.Contains(normalized, normalizeWhitespace(example)) {
			t.Errorf("review-prompt.md no longer contains the #2436 example %q in its prose-non-blocking rule", example)
		}
	}
}
