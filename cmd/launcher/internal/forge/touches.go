package forge

import (
	"regexp"
	"strings"
)

var touchesHeader = regexp.MustCompile(`(?i)^#+\s*touches\s*:?\s*$`)

// ParseTouchPaths extracts the declared touch-set from a "## Touches" section
// — a bullet list of path globs — following the same section-parsing
// conventions as ParseBlockerRefs. Unlike blockers, touches have no inline
// form; only the header section is recognised. Order is preserved and
// duplicates are dropped.
func ParseTouchPaths(body string) []string {
	seen := map[string]bool{}
	var paths []string
	addPath := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}

	inSection := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")

		// Trims before matching, unlike IsAnyHeading below (IsBulletItem's
		// own regex tolerates leading whitespace) — same trim/no-trim
		// split as IsBlockedByHeader in blockers.go.
		if touchesHeader.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		if IsAnyHeading(line) {
			inSection = false
		}

		if inSection && IsBulletItem(line) {
			addPath(ExtractBulletContent(line))
		}
	}
	return paths
}
