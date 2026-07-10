package forge

import "strings"

// appendComment adds comment as a bullet under a trailing "## Comments"
// section of body, creating the section if it isn't already present.
func appendComment(body, comment string) string {
	trimmed := strings.TrimRight(body, "\n")
	if !strings.Contains(trimmed, "## Comments") {
		trimmed += "\n\n## Comments\n"
	}
	trimmed += "\n- " + strings.ReplaceAll(comment, "\n", " ")
	return trimmed + "\n"
}
