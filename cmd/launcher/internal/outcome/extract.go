package outcome

import (
	"regexp"
	"strings"
)

// Per-line markdown strippers. POSIX leftmost-longest, so a trailing "** "
// peels whole rather than leaving the "**" behind.
var (
	leadingMarkdown  = regexp.MustCompilePOSIX("^[[:space:]]*(\\*\\*|`)?")
	trailingMarkdown = regexp.MustCompilePOSIX("(\\*\\*|`)?[[:space:]]*$")
)

// StripResultText strips the surrounding whitespace and one layer of markdown
// bold/code wrapping from every line of a Driver's result text, so a line the
// agent wrote as "**SPINDRIFT_OUTCOME ...**" reaches ExtractOutcomeLine bare
// (issue #1611). A trailing newline, or its absence, is preserved.
func StripResultText(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = leadingMarkdown.ReplaceAllString(l, "")
		lines[i] = trailingMarkdown.ReplaceAllString(l, "")
	}
	return strings.Join(lines, "\n")
}

// ExtractOutcomeLine returns the last line of strippedText (StripResultText's
// output) that leads with the SPINDRIFT_OUTCOME token and carries both a
// landing= and a status= field, with a colon after the token normalized to a
// space (issue #2012), or "" when none does. Keys inside the trailing note do
// not count. It does not reuse hasOutcomeFields: each key must follow a
// literal space (fieldsPart space-prefixes its input), where
// hasOutcomeFields splits on any whitespace. A near miss is deliberately not
// returned here.
func ExtractOutcomeLine(strippedText string) string {
	var last string
	for _, line := range strings.Split(strippedText, "\n") {
		rest, ok := stripToken(line, Token)
		if !ok {
			continue
		}
		if !strings.HasPrefix(line, Token+" ") {
			rest = strings.TrimLeft(rest, " \t\n\v\f\r")
			line = Token + " " + rest
		}
		fields := fieldsPart(rest)
		if strings.Contains(fields, " landing=") && strings.Contains(fields, " status=") {
			last = line
		}
	}
	return last
}
