package local

import "strings"

// SanitizedParent is a seam issue's resolved Integration-branch key, a ref-safe
// token. The unexported field makes ResolveParent the only mint point, so the
// pre-sanitized input IntegrationBranch and SurfaceIntegrationBranch require is
// enforced by the compiler instead of a doc comment (issue #1810).
type SanitizedParent struct {
	token string
}

// String returns p's sanitized token.
func (p SanitizedParent) String() string {
	return p.token
}

// ResolveParent returns the sanitized Integration-branch key for a seam issue,
// taken from the issue's parent: frontmatter field. When rawParent is unset or
// sanitizes to empty it falls back to issueNumber, so a parentless seam is its
// own broad ticket (ADR 0033, issue #1734). issueNumber sanitizing to empty too
// has no third fallback: in practice every issue number is a ".md" basename.
func ResolveParent(issueNumber, rawParent string) SanitizedParent {
	if sanitized := SanitizeParent(rawParent); sanitized != "" {
		return SanitizedParent{token: sanitized}
	}
	return SanitizedParent{token: SanitizeParent(issueNumber)}
}

// SanitizeParent lowercases s, collapses each run of non-[a-z0-9] characters to
// a single dash, and trims leading and trailing dashes. It is the only gate
// between free-form frontmatter text and a branch name component, which
// IntegrationBranch and SurfaceIntegrationBranch both require pre-sanitized.
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
