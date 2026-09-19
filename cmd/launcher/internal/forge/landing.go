package forge

import (
	"errors"
	"strings"
)

// LandingKind discriminates Landing's sealed variants.
type LandingKind int

const (
	// LandingPRURL is a github PR URL, the landing grammar CODE_FORGE=github records.
	LandingPRURL LandingKind = iota
	// LandingBranchRef is a raw, pre-merge branch name: CODE_FORGE=local's record
	// before its post-merge upgrade to LandingIntegrationRef, and CODE_FORGE=git's
	// only landing shape.
	LandingBranchRef
	// LandingIntegrationRef is CODE_FORGE=local's immutable post-merge reference
	// (ADR 0029/0033), "<branch>@<sha>".
	LandingIntegrationRef
)

// Landing is the typed form of the landing reference stored as a plain string in
// issue frontmatter, the outcome line, and every remote-tracker interface (ADR
// 0029, issue #1809). ParseLanding is the only constructor, so consumers match
// on Kind rather than re-deriving the three grammars from the raw string.
type Landing struct {
	Kind LandingKind
	URL  string
	// Branch holds the branch name for both LandingBranchRef and LandingIntegrationRef.
	Branch string
	SHA    string
}

// ParseLanding parses a stored landing string into its typed Landing value. An
// http:// or https:// string is a LandingPRURL, a "<branch>@<sha>" ref is a
// LandingIntegrationRef, and any other non-empty string is a LandingBranchRef.
// Only the empty string is rejected: recordLanding's callers already guard
// against writing one, so reaching it is a caller bug.
func ParseLanding(s string) (Landing, error) {
	if s == "" {
		return Landing{}, errors.New("forge: empty landing")
	}
	// Checked ahead of the "@" cut below so a URL wins over any IntegrationRef
	// reading. Real branch names never carry an "http(s)://" prefix, so this
	// never misclassifies a genuine one.
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return Landing{Kind: LandingPRURL, URL: s}, nil
	}
	if branch, sha, ok := strings.Cut(s, "@"); ok && branch != "" && sha != "" && !strings.HasPrefix(sha, "-") {
		return Landing{Kind: LandingIntegrationRef, Branch: branch, SHA: sha}, nil
	}
	return Landing{Kind: LandingBranchRef, Branch: s}, nil
}

// String renders l back into the grammar ParseLanding parses, so
// ParseLanding(l.String()) reproduces every Landing ParseLanding can produce.
func (l Landing) String() string {
	switch l.Kind {
	case LandingPRURL:
		return l.URL
	case LandingIntegrationRef:
		return l.Branch + "@" + l.SHA
	case LandingBranchRef:
		return l.Branch
	default:
		return ""
	}
}
