package forge

import "strings"

// AppendComment adds comment as a bullet under a trailing "## Comments"
// section of body, creating the section if it isn't already present. Shared
// by adapters (github, jira, local) whose Comment method appends to an
// issue's body text rather than posting to a separate comment API.
func AppendComment(body, comment string) string {
	trimmed := strings.TrimRight(body, "\n")
	if !strings.Contains(trimmed, "## Comments") {
		trimmed += "\n\n## Comments\n"
	}
	trimmed += "\n- " + strings.ReplaceAll(comment, "\n", " ")
	return trimmed + "\n"
}
