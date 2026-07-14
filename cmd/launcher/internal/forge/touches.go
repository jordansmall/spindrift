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

		if touchesHeader.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		if AnyHeading.MatchString(line) {
			inSection = false
		}

		if inSection && BulletItem.MatchString(line) {
			item := BulletItem.ReplaceAllString(line, "")
			addPath(strings.TrimSpace(item))
		}
	}
	return paths
}
