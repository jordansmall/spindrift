package settle

import "spindrift.dev/launcher/internal/dispatch"

// Situation is the shared read of relayed-branch adoption evidence that
// recover's adopt policy and settle's decline policy both consult.
type Situation struct {
	// OpenPRFound is the caller's fact to supply, not one this package
	// resolves; SettleRelayedBranch documents that precondition.
	OpenPRFound       bool
	BundlePresent     bool
	SelfReportSuccess bool
}

// situationFor computes num's Situation for one hand-off decision, reading the
// driver-log self-report added in issue #2223. openPRFound passes through
// unchanged.
func (s *Settle) situationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return Situation{
		OpenPRFound:       openPRFound,
		BundlePresent:     s.bundlePresent(num),
		SelfReportSuccess: result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status),
	}
}

// SituationFor is situationFor's exported form for callers outside this package.
func (s *Settle) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return s.situationFor(num, openPRFound, result)
}
