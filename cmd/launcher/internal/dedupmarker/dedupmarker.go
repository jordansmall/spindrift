// Package dedupmarker is the grammar of the hidden dedup marker line a filed
// finding's body ends with (issue #3609): "<!-- spindrift-dedup: t1, t2 -->".
// It owns only the line shape -- which line is the marker, how its terms are
// joined and split. What a term may contain is the caller's rule (settle
// normalizes before Build and after Parse), so a tracker adapter that merely
// moves the line (the Jira adapter, ADR 0072) shares the grammar without
// importing settle.
package dedupmarker

import "strings"

const (
	// Prefix opens a marker line.
	Prefix = "<!-- spindrift-dedup: "
	// Suffix closes a marker line.
	Suffix = " -->"

	// Bare is the marker carrying no terms. Build returns "" for no terms
	// rather than Bare so a caller decides whether a term-less body still
	// needs the line (settle's last-wins rule says it does).
	Bare = Prefix + Suffix
)

// Build renders terms, already normalized by the caller, as a marker line, or
// "" when there are none.
func Build(terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	return Prefix + strings.Join(terms, ", ") + Suffix
}

func isMarkerLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, Prefix) && strings.HasSuffix(line, Suffix)
}

// lastMarkerLine returns the index of body's last marker line within lines,
// or -1. Last wins because the filer appends its own marker last; an earlier
// marker line is untrusted body prose (issue #3609).
func lastMarkerLine(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if isMarkerLine(lines[i]) {
			return i
		}
	}
	return -1
}

// Parse returns the comma-separated terms of body's last marker line, trimmed
// and otherwise unvalidated, or nil when body has no marker or the marker
// carries none.
func Parse(body string) []string {
	lines := strings.Split(body, "\n")
	i := lastMarkerLine(lines)
	if i < 0 {
		return nil
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(lines[i]), Prefix), Suffix)
	var terms []string
	for _, t := range strings.Split(inner, ",") {
		if t = strings.TrimSpace(t); t != "" {
			terms = append(terms, t)
		}
	}
	return terms
}

// Split removes body's last marker line, the one Parse reads, and returns the
// rest with that line verbatim. rest is body unchanged, marker "", when
// there is none. When the marker closes the body, the blank lines that only
// separated it from the text are dropped too, so Append restores them.
func Split(body string) (rest, marker string) {
	lines := strings.Split(body, "\n")
	i := lastMarkerLine(lines)
	if i < 0 {
		return body, ""
	}
	before := strings.Join(lines[:i], "\n")
	after := strings.Join(lines[i+1:], "\n")
	if strings.TrimSpace(after) == "" {
		return strings.TrimRight(before, " \t\r\n"), lines[i]
	}
	if before == "" {
		return after, lines[i]
	}
	return before + "\n" + after, lines[i]
}

// Append writes marker back as body's last line, after a blank line as the
// filer lays it out. An empty marker leaves body untouched.
func Append(body, marker string) string {
	switch {
	case marker == "":
		return body
	case body == "":
		return marker
	}
	return body + "\n\n" + marker
}
