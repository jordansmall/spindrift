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

// DedupScanLimit bounds a single LabeledBacklogLister page (issue #3609
// review), larger than ResultPageLimit: the label filter already keeps the
// scanned population small (only review/research finding issues, not the
// whole backlog), and a truncated dedup scan that drops the newest finding
// issues files the exact duplicate the scan exists to prevent -- worse than
// ResultPageLimit's "rerun to drain" tradeoff, which just delays triage.
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
