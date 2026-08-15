package settle

import "spindrift.dev/launcher/internal/dispatch"

// Situation is the shared read of relayed-branch adoption evidence both
// recover's adopt policy (SettleRelayedBranch) and settle's decline policy
// (tryAdoptRelayedBranch/tryAdoptRelayedBranchNoOutcome) consult -- computed
// once per call instead of re-derived ad hoc at each inline check site
// (issue #2501). OpenPRFound is supplied by the caller (main.go's
// recoverByNumber already resolves it via forge.ResolveOpenPR before ever
// reaching SettleRelayedBranch; settle's own blocked/no-outcome paths never
// probe for one -- they're adopting a branch precisely because no PR exists
// yet) rather than re-queried here, so this never adds a network call this
// refactor didn't already make. The two policies stay deliberately inverted
// -- recover adopts on this evidence, settle declines without it -- and are
// not unified by this type; each still makes its own decision from the same
// three facts.
type Situation struct {
	OpenPRFound       bool
	BundlePresent     bool
	SelfReportSuccess bool
}

// situationFor computes num's Situation for one hand-off decision: whether
// its outbox holds a relayable bundle (a plain stat, see bundlePresent) and
// whether result's driver-log self-report (issue #2223) itself claims
// success (isSuccessSelfReport). openPRFound is passed through unchanged --
// see Situation's own doc comment for why it's the caller's fact to supply,
// not this function's to derive.
func (s *Settle) situationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return Situation{
		OpenPRFound:       openPRFound,
		BundlePresent:     s.bundlePresent(num),
		SelfReportSuccess: result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status),
	}
}
