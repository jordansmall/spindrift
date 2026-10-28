package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// Situation is the shared read of relayed-branch adoption evidence that
// recover's adopt policy and settle's decline policy both consult.
type Situation struct {
	// OpenPRFound is the caller's fact to supply, not one this package
	// resolves; SettleRelayedBranch documents that precondition.
	OpenPRFound       bool
	BundlePresent     bool
	SelfReportSuccess bool

	// Unreviewed means the review loop stopped before its land pass finished:
	// with a success self-report, the ready came from a pass other than the
	// land pass and no land pass outcome followed it (issue #5973); without a
	// success self-report, the loop stopped in implement or fix (issue #5984).
	Unreviewed bool
}

// situationFor computes num's Situation for one hand-off decision, reading the
// driver-log self-report added in issue #2223 and the pass manifest.
// openPRFound passes through unchanged.
func (s *Settle) situationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	success := result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status)
	// A run with no success self-report (none, or blocked/ambiguous) has no
	// stale ready to judge, so only where the loop stopped matters; a crashed
	// land pass already cleared review (#2378, #2363) and stays trusted.
	var unreviewed bool
	if success {
		unreviewed = passmanifest.UnreviewedReady(result.Passes)
	} else {
		unreviewed = passmanifest.StoppedBeforeLand(result.Passes)
	}
	return Situation{
		OpenPRFound:       openPRFound,
		BundlePresent:     s.bundlePresent(num),
		SelfReportSuccess: success,
		Unreviewed:        unreviewed,
	}
}

// SituationFor is situationFor's exported form for callers outside this package.
func (s *Settle) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return s.situationFor(num, openPRFound, result)
}
