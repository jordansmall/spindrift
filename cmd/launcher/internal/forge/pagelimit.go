package forge

import (
	"fmt"
	"os"
)

// ResultPageLimit bounds a single issue-tracker list/search page across
// adapters; a backlog larger than this drains over successive dispatch runs
// rather than in one unbounded response.
const ResultPageLimit = 100

// WarnPageMayTruncateBacklog prints a warning when a page of list/search
// results from source hit ResultPageLimit, since the tracker's actual
// backlog may be larger than what was returned.
func WarnPageMayTruncateBacklog(source string, count int) {
	if count >= ResultPageLimit {
		fmt.Fprintf(os.Stderr, "WARNING: %s returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			source, count, ResultPageLimit)
	}
}

// FullyPaginated is the optional IssueTracker surface for adapters whose
// ListIssues/ListOpenIssues walk every page of the underlying forge API
// (forgejo, jira) rather than returning a single page capped at
// ResultPageLimit (github's gh-exec adapter, and the Fake test double,
// which stay single-page and therefore don't implement this). A tracker
// that reports WalksAllPages() true has already proven its result set
// complete by draining every page itself, so a caller like
// issueInState's page-limit fail-safe (#707/#986) can trust a
// full-looking result (len >= ResultPageLimit) as exhaustive instead of
// suspecting truncation — the size crossing the cap is coincidental, not
// evidence of a dropped tail. Callers discover it with a type assertion —
// `fp, ok := tracker.(FullyPaginated)` — the same optional-interface
// pattern LabeledTracker, IssueCloser, and LandingRecorder use.
type FullyPaginated interface {
	// WalksAllPages reports whether this tracker's list results are always
	// a complete, non-truncated set.
	WalksAllPages() bool
}
