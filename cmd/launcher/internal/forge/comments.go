package forge

import "strings"

// AppendComment adds comment under a trailing "## Comments" section of body,
// creating the section if it isn't already present. A single-line comment is
// added as a "- " bullet; a multi-line comment is emitted verbatim as a
// top-level block set off by a "---" separator, so block-level Markdown
// (ATX headings, GFM tables) in the comment still renders. Shared by
// adapters (github, jira, local) whose Comment method appends to an issue's
// body text rather than posting to a separate comment API.
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
