package forge

import (
	"regexp"
	"strings"
)

var (
	blockKeyword    = regexp.MustCompile(`(?i)(?:depends on|blocked by)\s*:?\s*`)
	issueRef        = regexp.MustCompile(`#([0-9]+)`)
	blockedByHeader = regexp.MustCompile(`(?i)^#+\s*blocked by\s*:?\s*$`)
	anyHeading      = regexp.MustCompile(`^#+`)
	bulletItem      = regexp.MustCompile(`^[ \t]*[-*][ \t]*`)
	refListPrefix   = regexp.MustCompile(`^(?:#[0-9]+|[,/]|\s+|\band\b)+`)
)

// IsBlockedByHeader reports whether line is a "## Blocked by" section header.
// The local adapter's parseLocalBlockers calls this to reuse the same
// section-parsing grammar for slug-based (rather than "#N") blocker refs.
func IsBlockedByHeader(line string) bool {
	return blockedByHeader.MatchString(strings.TrimSpace(line))
}

// IsAnyHeading reports whether line is a markdown heading of any level.
func IsAnyHeading(line string) bool {
	return anyHeading.MatchString(line)
}

// IsBulletItem reports whether line is a "-" or "*" bullet list item.
func IsBulletItem(line string) bool {
	return bulletItem.MatchString(line)
}

// ExtractBulletContent strips the bullet prefix from line and trims
// surrounding whitespace, returning the item's content.
func ExtractBulletContent(line string) string {
	return strings.TrimSpace(bulletItem.ReplaceAllString(line, ""))
}

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

		if IsBlockedByHeader(line) {
			inSection = true
			continue
		}
		if IsAnyHeading(line) {
			inSection = false
		}

		if inSection && IsBulletItem(line) {
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
