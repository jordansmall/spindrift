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

// RelayClone exposes the clone closure PushBranch, Relay and CommitSubjects share.
func RelayClone(cf forge.CodeForge, op string) func(dir string) error {
	return cf.(*forgejoCodeForge).relayClone(op)
}

// SetJobLogsBudget swaps the cap on one FailureDetail's job-log reads and
// returns a restore func.
func SetJobLogsBudget(d time.Duration) (restore func()) {
	old := jobLogsBudget
	jobLogsBudget = d
	return func() { jobLogsBudget = old }
}
