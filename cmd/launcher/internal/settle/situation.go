package settle

import "spindrift.dev/launcher/internal/dispatch"

// Situation is the shared read of relayed-branch adoption evidence both
// recover's adopt policy (SettleRelayedBranch) and settle's decline policy
// (tryAdoptRelayedBranch/tryAdoptRelayedBranchNoOutcome) consult — computed
// once per call instead of re-derived ad hoc at each inline check site
// (issue #2501). OpenPRFound is a real fact the caller must supply — this
// package never auto-resolves it (main.go's recoverByNumber resolves it
// itself via forge.ResolveOpenPRWithRetry before ever reaching
// SettleRelayedBranch; settle's own blocked/no-outcome paths always pass
// false, since they're adopting a branch precisely because no PR exists
// yet). SettleRelayedBranch enforces the caller-supplied value as a hard
// precondition (returning false immediately when it's true) rather than
// merely documenting it, so a caller that gets this fact wrong fails safe
// instead of silently double-adopting. The two policies stay deliberately
// inverted — recover adopts on this evidence, settle declines without it —
// and are not unified by this type; each still makes its own decision from
// the same three facts.
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
// this package (main.go's recoverByNumber, issue #2501) compute a Situation
// once and thread it into SettleRelayedBranch without duplicating
// situationFor's own logic.
func (s *Settle) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	return s.situationFor(num, openPRFound, result)
}
