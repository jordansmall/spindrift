package forge

import "strings"

const commentsHeading = "## Comments"

// commentsSectionStart returns the byte index of the first line that is the
// "## Comments" heading after trimming whitespace, or -1. Line-anchored (not a
// substring match) so "### Comments" or a prose mention does not count;
// AppendComment and Description share it so detection and split cannot drift.
// Code fences are not tracked, so a bare "## Comments" line inside one in the
// description also counts and ends the description there.
func commentsSectionStart(body string) int {
	for off := 0; off < len(body); {
		line, _, _ := strings.Cut(body[off:], "\n")
		if strings.TrimSpace(line) == commentsHeading {
			return off
		}
		off += len(line) + 1
	}
	return -1
}

// Description returns the part of body above its "## Comments" section, or
// body unchanged when it has none.
func Description(body string) string {
	i := commentsSectionStart(body)
	if i < 0 {
		return body
	}
	return strings.TrimRight(body[:i], " \t\r\n")
}

// AppendComment adds comment under a trailing "## Comments" section of body,
// creating the section if it is missing. A multi-line comment goes in verbatim
// after a "---" separator rather than as a "- " bullet, so block-level Markdown
// such as ATX headings and GFM tables still renders.
func AppendComment(body, comment string) string {
	trimmed := strings.TrimRight(body, "\n")
	if commentsSectionStart(trimmed) < 0 {
		trimmed += "\n\n" + commentsHeading
	}
	comment = strings.TrimRight(comment, "\n")
	if strings.Contains(comment, "\n") {
		trimmed += "\n\n---\n\n" + comment
	} else {
		trimmed += "\n\n- " + comment
	}
	return trimmed + "\n"
}
