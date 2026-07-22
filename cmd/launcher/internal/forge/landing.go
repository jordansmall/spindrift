package forge

import (
	"errors"
	"strings"
)

// LandingKind discriminates Landing's sealed variants.
type LandingKind int

const (
	// LandingPRURL is a github PR URL, the landing grammar CODE_FORGE=github
	// records.
	LandingPRURL LandingKind = iota
	// LandingBranchRef is a raw, pre-merge branch name — CODE_FORGE=local's
	// landing record before its post-merge upgrade to LandingIntegrationRef,
	// and CODE_FORGE=git's only landing shape.
	LandingBranchRef
	// LandingIntegrationRef is CODE_FORGE=local's immutable post-merge
	// reference (ADR 0029/0033): the Integration branch name plus the commit
	// sha the merge landed at, "<branch>@<sha>".
	LandingIntegrationRef
)

// Landing is the sealed, typed form of the landing reference stored as a
// plain string in issue frontmatter, the outcome line, and every
// remote-tracker interface (ADR 0029, issue #1809). ParseLanding is the
// single seam that produces one from a stored string; String is its inverse.
// Consumers match on Kind instead of re-deriving the three grammars from the
// raw string themselves.
type Landing struct {
	Kind LandingKind
	// URL holds the PR URL for LandingPRURL.
	URL string
	// Branch holds the branch name for LandingBranchRef and LandingIntegrationRef.
	Branch string
	// SHA holds the landed commit sha for LandingIntegrationRef.
	SHA string
}

// ParseLanding parses a stored landing string into its typed Landing value.
// A github PR URL (http:// or https://) parses as LandingPRURL; a
// "<branch>@<sha>" ref (ADR 0029/0033) parses as LandingIntegrationRef; any
// other non-empty string — a raw branch name — parses as LandingBranchRef.
// Only an empty string is rejected: recordLanding's callers already guard
// against writing one, so ParseLanding treats reaching one as a caller bug
// worth erroring on rather than a landing shape to represent.
func ParseLanding(s string) (Landing, error) {
	if s == "" {
		return Landing{}, errors.New("forge: empty landing")
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return Landing{Kind: LandingPRURL, URL: s}, nil
	}
	if branch, sha, ok := strings.Cut(s, "@"); ok && branch != "" && sha != "" && !strings.HasPrefix(sha, "-") {
		return Landing{Kind: LandingIntegrationRef, Branch: branch, SHA: sha}, nil
	}
	return Landing{Kind: LandingBranchRef, Branch: s}, nil
}

// String renders l back into the stored-string grammar ParseLanding parses.
// ParseLanding(l.String()) reproduces l for every Landing ParseLanding itself
// can produce.
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
