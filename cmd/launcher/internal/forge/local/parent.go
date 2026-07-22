package local

import "strings"

// SanitizedParent is a seam issue's resolved Integration-branch key — a
// ref-safe token already run through SanitizeParent. Its only mint point is
// ResolveParent: the unexported field keeps a caller from fabricating one
// from an arbitrary string, turning the previous "callers must pass
// pre-sanitized input" doc-comment invariant on IntegrationBranch and
// SurfaceIntegrationBranch into a compile error (issue #1810).
type SanitizedParent struct {
	token string
}

// String returns p's sanitized token, the git-ref-safe component
// IntegrationBranch and its siblings compose into a full ref name.
func (p SanitizedParent) String() string {
	return p.token
}

// ResolveParent returns the sanitized Integration-branch key for a seam
// issue: rawParent (the issue's own parent: frontmatter field), sanitized,
// or — when rawParent is unset, or sanitizes to empty (a parent: value made
// entirely of non-[a-z0-9] characters) — issueNumber itself, sanitized, so
// a parentless seam is its own broad ticket (ADR 0033, issue #1734).
// issueNumber sanitizing to empty too (both a parent: value and the
// issue's own slug made entirely of non-[a-z0-9] characters) is not
// reachable through the local tracker in practice — every issue's number
// comes from a non-empty ".md" filename basename — so this is left
// unguarded rather than invented a third fallback with no natural value.
func ResolveParent(issueNumber, rawParent string) SanitizedParent {
	if sanitized := SanitizeParent(rawParent); sanitized != "" {
		return SanitizedParent{token: sanitized}
	}
	return SanitizedParent{token: SanitizeParent(issueNumber)}
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
