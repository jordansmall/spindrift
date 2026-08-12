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
			name:   "prose findings are Non-blocking, discretion-free",
			clause: "wording, style, redundancy, and ordering findings on prose the diff touches — commit messages, comments, and docs — are always Non-blocking",
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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("review-prompt.md no longer states %q", c.clause)
			}
		})
	}
}
