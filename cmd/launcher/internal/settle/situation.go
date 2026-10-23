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

	// Unreviewed means the self-reported success came from a review-loop pass
	// other than the land pass, and no land pass outcome followed it (issue
	// #5973).
	Unreviewed bool
}

// situationFor computes num's Situation for one hand-off decision, reading the
// driver-log self-report added in issue #2223 and the pass manifest.
// openPRFound passes through unchanged.
func (s *Settle) situationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	success := result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status)
	return Situation{
		OpenPRFound:       openPRFound,
		BundlePresent:     s.bundlePresent(num),
		SelfReportSuccess: success,
		Unreviewed:        success && passmanifest.UnreviewedReady(result.Passes),
	}
}

// SituationFor is situationFor's exported form for callers outside this package.
func (s *Settle) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return s.situationFor(num, openPRFound, result)
}
