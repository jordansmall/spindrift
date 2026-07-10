package forge

import "fmt"

// resultPageLimit bounds a single issue-tracker list/search page across
// adapters; a backlog larger than this drains over successive dispatch runs
// rather than in one unbounded response.
const resultPageLimit = 100

// warnPageMayTruncateBacklog prints a warning when a page of list/search
// results from source hit resultPageLimit, since the tracker's actual
// backlog may be larger than what was returned.
func warnPageMayTruncateBacklog(source string, count int) {
	if count >= resultPageLimit {
		fmt.Printf("WARNING: %s returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			source, count, resultPageLimit)
	}
}
