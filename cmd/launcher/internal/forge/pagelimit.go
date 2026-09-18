package forge

import (
	"fmt"
	"os"
)

// ResultPageLimit bounds a single issue-tracker list/search page across
// adapters. A backlog larger than this drains over successive dispatch runs
// rather than in one unbounded response.
const ResultPageLimit = 100

// WarnPageMayTruncateBacklog warns when a page of list/search results from
// source hit ResultPageLimit, so the real backlog may be larger.
func WarnPageMayTruncateBacklog(source string, count int) {
	if count >= ResultPageLimit {
		fmt.Fprintf(os.Stderr, "WARNING: %s returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			source, count, ResultPageLimit)
	}
}

// FullyPaginated is the optional IssueTracker interface for adapters that walk
// every page of the forge API (forgejo, jira) instead of returning one page
// capped at ResultPageLimit. When WalksAllPages reports true, a caller such as
// issueInState's page-limit fail-safe (#707/#986) can treat a result of
// len >= ResultPageLimit as complete rather than truncated.
type FullyPaginated interface {
	// WalksAllPages reports whether this tracker's list results are always
	// a complete, non-truncated set.
	WalksAllPages() bool
}
