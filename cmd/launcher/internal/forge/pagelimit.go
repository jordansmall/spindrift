package forge

import "fmt"

// ResultPageLimit bounds a single issue-tracker list/search page across
// adapters; a backlog larger than this drains over successive dispatch runs
// rather than in one unbounded response.
const ResultPageLimit = 100

// WarnPageMayTruncateBacklog prints a warning when a page of list/search
// results from source hit ResultPageLimit, since the tracker's actual
// backlog may be larger than what was returned.
func WarnPageMayTruncateBacklog(source string, count int) {
	if count >= ResultPageLimit {
		fmt.Printf("WARNING: %s returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			source, count, ResultPageLimit)
	}
}
