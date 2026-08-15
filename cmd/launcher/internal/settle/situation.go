package settle

import "spindrift.dev/launcher/internal/dispatch"

// Situation is the shared read of relayed-branch adoption evidence both
// recover's adopt policy (SettleRelayedBranch) and settle's decline policy
// (tryAdoptRelayedBranch/tryAdoptRelayedBranchNoOutcome) consult — computed
// once per call instead of re-derived ad hoc at each inline check site.
// OpenPRFound is a real fact the caller must supply, not one this package
// auto-resolves — see SettleRelayedBranch's own doc comment for that
// precondition's full reasoning. BundlePresent and SelfReportSuccess are
// derived host-side by situationFor.
type Situation struct {
	OpenPRFound       bool
	BundlePresent     bool
	SelfReportSuccess bool
}

// situationFor computes num's Situation for one hand-off decision: whether
// its outbox holds a relayable bundle (a plain stat, see bundlePresent) and
// whether result's driver-log self-report (issue #2223) itself claims
// success (isSuccessSelfReport). openPRFound is passed through unchanged —
// see Situation's own doc comment for why it's the caller's fact to supply,
// not this function's to derive.
func (s *Settle) situationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return Situation{
		OpenPRFound:       openPRFound,
		BundlePresent:     s.bundlePresent(num),
		SelfReportSuccess: result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status),
	}
}

// SituationFor is situationFor's exported form, letting a caller outside
// this package (main.go's recoverByNumber) compute a Situation once and
// thread it into SettleRelayedBranch without duplicating situationFor's own
// logic.
func (s *Settle) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return s.situationFor(num, openPRFound, result)
}
