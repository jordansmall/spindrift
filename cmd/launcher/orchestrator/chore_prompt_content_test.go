package main

import "testing"

// TestDocsDriftChoreSiteKeys pins the docs-drift Chore's one Markdown site
// key form (issue #6395): without it each sweep improvised a different key
// (`file.md:pi`, `file.md:1320`) and the host's exact-match dedup filed the
// same site three times. A `wrong-code-comment` finding keys on the commented
// symbol alone (issue #6483), since an evidence-file term varied by sweep and
// refiled the finding as a partial overlap.
func TestDocsDriftChoreSiteKeys(t *testing.T) {
	assertPromptClauses(t, "chores/docs-drift.md", []promptClause{
		{
			name:   "keys on the nearest heading's anchor",
			clause: "`path/to/file.md:<anchor>` (the anchor of the nearest heading above the stale text: lowercased, punctuation dropped but hyphens and underscores kept, then each run of spaces and hyphens folded to one hyphen, a repeated heading taking a `-1`, `-2` suffix: `#### The pi Driver` is `the-pi-driver`). Text above a file's first heading keys on the bare path `path/to/file.md`.",
		},
		{
			name:   "keys the doc site alongside the code site, never instead of it",
			clause: "doc-site key goes alongside the code-site key, never instead of it",
		},
		{
			name:   "never a line number or free word",
			clause: "never a line number or a free word like `pi`",
		},
		{
			name:   "keys a wrong-code-comment on the commented symbol alone",
			clause: "A `wrong-code-comment` finding has no separate doc site: key it on the commented symbol alone, `path/to/file.go:Symbol`, never on the files it cites as evidence of the contradiction.",
		},
		{
			name:   "keys a symbol-less comment on the bare path",
			clause: "A comment that documents no symbol, such as a package doc comment or a file header, keys on the bare path `path/to/file.go`.",
		},
		{
			name:   "counts a stale code comment as drifted documentation",
			clause: "or a code comment the code beside it contradicts.",
		},
	})
}

// TestTuningChoreKeysMarkdownTargetOnHeadingAnchor pins the tuning Chore's
// Markdown dedup term to the same anchor recipe as the docs-drift Chore (issue
// #6395): the host drops a term containing "--", so an unfolded GitHub slug
// of a heading with a spaced dash would lose the target file.
func TestTuningChoreKeysMarkdownTargetOnHeadingAnchor(t *testing.T) {
	assertPromptClauses(t, "chores/tuning.md", []promptClause{
		{
			name:   "keys a Markdown target on the folded heading anchor",
			clause: "`-dedup <path>:<symbol>` (`<path>:<anchor>`, the nearest heading's anchor, for a Markdown target: lowercased, punctuation dropped but hyphens and underscores kept, then each run of spaces and hyphens folded to one hyphen, a repeated heading taking a `-1`, `-2` suffix; text above the first heading keys on the bare path)",
		},
	})
}
