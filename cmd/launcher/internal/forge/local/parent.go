package local

import "strings"

// ResolveParent returns the sanitized Integration-branch key for a seam
// issue: rawParent (the issue's own parent: frontmatter field), sanitized,
// or — when rawParent is unset, or sanitizes to empty (a parent: value made
// entirely of non-[a-z0-9] characters) — issueNumber itself, sanitized, so
// a parentless seam is its own broad ticket (ADR 0033, issue #1734).
func ResolveParent(issueNumber, rawParent string) string {
	if sanitized := SanitizeParent(rawParent); sanitized != "" {
		return sanitized
	}
	return SanitizeParent(issueNumber)
}

// SanitizeParent normalizes an operator-authored parent: value (or an
// issue's own slug) into a git-ref-safe token: lowercased, with each run of
// non-[a-z0-9] characters collapsed to a single dash and leading/trailing
// dashes trimmed. It is the sole gate between free-form frontmatter text
// (a GitHub URL, a Jira key, a plain name) and a branch name component —
// IntegrationBranch and SurfaceIntegrationBranch both require their input
// pre-sanitized.
func SanitizeParent(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
