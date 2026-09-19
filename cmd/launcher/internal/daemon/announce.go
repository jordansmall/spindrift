package daemon

import "regexp"

// announceRe is coupled to internal/dispatch's announceLine helper (box.go),
// the single place a child launcher's dispatch.Dispatch builds the line
// announcing an issue it is about to run. There is no machine-readable
// channel naming the dispatched issue; this regex is the only signal.
// internal/dispatch's TestAnnounceLine_ParsesBack (announce_test.go) feeds
// announceLine's own output through ParseAnnouncedIssue, so a future edit to
// the announce line's shape fails there instead of silently here.
var announceRe = regexp.MustCompile(`^    -> #([0-9]+)(?: \([^)]*\))?: `)

// ParseAnnouncedIssue reads one line of a child launcher's stdout and
// returns the issue number it announced a Box for.
func ParseAnnouncedIssue(line string) (string, bool) {
	m := announceRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}
