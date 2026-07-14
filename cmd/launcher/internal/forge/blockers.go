package forge

import (
	"regexp"
	"strings"
)

var (
	blockKeyword = regexp.MustCompile(`(?i)(?:depends on|blocked by)\s*:?\s*`)
	issueRef     = regexp.MustCompile(`#([0-9]+)`)
	// BlockedByHeader, AnyHeading, and BulletItem are exported for the local
	// adapter's parseLocalBlockers, which reuses this section-parsing grammar
	// for slug-based (rather than "#N") blocker refs.
	BlockedByHeader = regexp.MustCompile(`(?i)^#+\s*blocked by\s*:?\s*$`)
	AnyHeading      = regexp.MustCompile(`^#+`)
	BulletItem      = regexp.MustCompile(`^[ \t]*[-*][ \t]*`)
	refListPrefix   = regexp.MustCompile(`^(?:#[0-9]+|[,/]|\s+|\band\b)+`)
)

// ParseBlockerRefs extracts all blocker issue numbers referenced in a body.
// Recognises two formats:
//   - Inline: "depends on #N" or "blocked by #N" anywhere in the body.
//     Refs in the contiguous list after the keyword are captured;
//     the list ends at the first prose token to prevent false blockers.
//   - Section: a "## Blocked by" header followed by "- #N" list items.
func ParseBlockerRefs(body string) []string {
	seen := map[string]bool{}
	var refs []string
	addRef := func(n string) {
		if !seen[n] {
			seen[n] = true
			refs = append(refs, n)
		}
	}

	inSection := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")

		if BlockedByHeader.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		if AnyHeading.MatchString(line) {
			inSection = false
		}

		if inSection && BulletItem.MatchString(line) {
			for _, m := range issueRef.FindAllStringSubmatch(line, -1) {
				addRef(m[1])
			}
		}

		remaining := line
		for {
			loc := blockKeyword.FindStringIndex(remaining)
			if loc == nil {
				break
			}
			after := remaining[loc[1]:]
			listStr := refListPrefix.FindString(after)
			for _, m := range issueRef.FindAllStringSubmatch(listStr, -1) {
				addRef(m[1])
			}
			remaining = after
		}
	}
	return refs
}
