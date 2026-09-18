// Package promptfence fences untrusted text for inline injection into an
// assembled prompt. Per CLAUDE.md's comment-injection trust boundary, an issue
// body or comment is data, and a fence the payload could close early would let
// it impersonate host-authored prompt structure.
package promptfence

import "strings"

// Block wraps content in a markdown code fence one backtick longer than the
// longest backtick run in content (minimum three), the CommonMark rule for a
// fence its own content cannot close early. It lives outside the orchestrator
// (#2550) so promptassembly can share it without that import (#3445).
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
