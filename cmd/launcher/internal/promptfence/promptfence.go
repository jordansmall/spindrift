// Package promptfence fences untrusted text for inline injection into an
// assembled prompt, per CLAUDE.md's comment-injection trust boundary: an
// issue body or comment is data, never host-authored prompt structure, and
// a fixed-width fence a payload could close early with its own backtick run
// would let it escape and impersonate that structure.
package promptfence

import "strings"

// Block wraps content in a markdown code fence sized one backtick longer
// than the longest run of consecutive backticks content itself contains
// (minimum three) -- the same rule CommonMark uses for a fence that must
// stay unbreakable by its own content. Moved from
// cmd/launcher/orchestrator/run.go's fenceBlock (issue #2550 review
// finding) so promptassembly's issue-text injection (issue #3445) can share
// it without an orchestrator -> promptassembly import.
func Block(content string) string {
	longest := 0
	run := 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fenceLen := longest + 1
	if fenceLen < 3 {
		fenceLen = 3
	}
	fence := strings.Repeat("`", fenceLen)
	return fence + "\n" + content + "\n" + fence
}
