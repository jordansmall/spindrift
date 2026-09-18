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
	fenceDelimiter  = regexp.MustCompile("^(```+|~~~+)")
	inlineCodeSpan  = regexp.MustCompile("`[^`]*`")
	// In sentinelBullet a dash or colon may abut the word, but an ASCII
	// hyphen must have whitespace before it so "None-existent" is not read
	// as "None" plus a dash.
	sentinelBullet = regexp.MustCompile(`(?i)^(?:none|n/a)(?:\s*[—–:]\s*.*|\s+-\s*.*)?$`)
)

// IsFenceDelimiter reports whether line opens or closes a fenced code block.
// It does not recognise indented 4-space code blocks.
func IsFenceDelimiter(line string) bool {
	return fenceDelimiter.MatchString(line)
}

// StripInlineCode blanks out inline code spans in line, so the parsers do not
// read a trigger phrase or ref quoted inside backticks as a real declaration.
func StripInlineCode(line string) string {
	return inlineCodeSpan.ReplaceAllString(line, "")
}

// IsSentinelBullet reports whether content is a "no blockers" sentinel.
// local.go's parseLocalBlockers calls it too, so both backends agree on what
// "no blockers" means.
func IsSentinelBullet(content string) bool {
	return sentinelBullet.MatchString(content)
}

// IsBlockedByHeader reports whether line is a "## Blocked by" section header.
// It trims line first, so an indented header still opens a section, while
// IsAnyHeading does not trim and so an indented heading never closes one.
// That asymmetry predates #680 and is deliberate; touches.go repeats it.
func IsBlockedByHeader(line string) bool {
	return blockedByHeader.MatchString(strings.TrimSpace(line))
}

// IsAnyHeading reports whether line is a markdown heading of any level. It
// does not trim line; see IsBlockedByHeader for what that implies.
func IsAnyHeading(line string) bool {
	return anyHeading.MatchString(line)
}

// IsBulletItem reports whether line is a "-" or "*" bullet list item.
func IsBulletItem(line string) bool {
	return bulletItem.MatchString(line)
}

// ExtractBulletContent strips the bullet prefix from line and trims whitespace.
func ExtractBulletContent(line string) string {
	return strings.TrimSpace(bulletItem.ReplaceAllString(line, ""))
}

// ParseBlockerRefs extracts blocker issue numbers from a body, both inline
// ("depends on #N", "blocked by #N") and under a "## Blocked by" header. An
// inline ref list ends at the first prose token, so prose that follows cannot
// add false blockers.
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
	inFence := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")

		if IsFenceDelimiter(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = StripInlineCode(line)

		if IsBlockedByHeader(line) {
			inSection = true
			continue
		}
		if IsAnyHeading(line) {
			inSection = false
		}

		if inSection && IsBulletItem(line) {
			if IsSentinelBullet(ExtractBulletContent(line)) {
				continue
			}
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
