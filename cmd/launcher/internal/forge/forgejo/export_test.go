package forgejo

import (
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// SetNow swaps the clock behind the undefined-label verdict's expiry.
func SetNow(t forge.IssueTracker, now func() time.Time) { t.(*forgejoClient).now = now }

// SetMergeRecheckDelay swaps the pause before Merge re-reads a pull that
// reported mergeable=false after a refusal.
func SetMergeRecheckDelay(cf forge.CodeForge, d time.Duration) {
	cf.(*forgejoCodeForge).mergeRecheckDelay = d
}
