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

// DedupScanLimit bounds each Connector tracker.labeled page (issue #3609); the
// Connector pages on past it by cursor. It is larger than ResultPageLimit: the
// label filter already keeps the scanned population small, and a truncated
// dedup scan files the exact duplicate the scan exists to prevent, worse than
// ResultPageLimit's "rerun to drain", which only delays triage.
const DedupScanLimit = 500

// WarnDedupScanMayTruncate warns when a single label's page of dedup-scan
// results from source hit DedupScanLimit, so older findings under label may
// have fallen off the scan and get re-filed as duplicates.
func WarnDedupScanMayTruncate(source, label string, count int) {
	if count >= DedupScanLimit {
		fmt.Fprintf(os.Stderr, "WARNING: %s returned %d %s issues (limit %d); older findings fell off the dedup scan and may be re-filed\n",
			source, count, label, DedupScanLimit)
	}
}

// WarnDedupScanStopped warns when a cursor walk of label's issues from source
// stopped at core's bound (named by bound, e.g. "page") after pages pages and
// count issues, before the end, so older findings may have fallen off the scan.
func WarnDedupScanStopped(source, label, bound string, pages, count int) {
	fmt.Fprintf(os.Stderr, "WARNING: %s stopped at the %s bound after %d pages (%d %s issues) without reaching the end; older findings fell off the dedup scan and may be re-filed\n",
		source, bound, pages, count, label)
}

// FullyPaginated is the optional IssueTracker interface for adapters that walk
// every page of the forge API instead of returning one page capped at
// ResultPageLimit. When WalksAllPages reports true, a page-limit fail-safe
// (#707/#986) may treat a result of len >= ResultPageLimit as complete rather
// than truncated. The Connector tracker reports false.
type FullyPaginated interface {
	// WalksAllPages reports whether this tracker's list results are always
	// a complete, non-truncated set.
	WalksAllPages() bool
}
