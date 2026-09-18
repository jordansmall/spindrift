package forge

import "strings"

// AppendComment adds comment under a trailing "## Comments" section of body,
// creating the section if it is missing. A multi-line comment goes in verbatim
// after a "---" separator rather than as a "- " bullet, so block-level Markdown
// such as ATX headings and GFM tables still renders.
func AppendComment(body, comment string) string {
	trimmed := strings.TrimRight(body, "\n")
	if !strings.Contains(trimmed, "## Comments") {
		trimmed += "\n\n## Comments"
	}
	comment = strings.TrimRight(comment, "\n")
	if strings.Contains(comment, "\n") {
		trimmed += "\n\n---\n\n" + comment
	} else {
		trimmed += "\n\n- " + comment
	}
	return trimmed + "\n"
}
