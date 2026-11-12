package settle

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmanifest"
)

// pushOnlyLander lands a push-only Code Forge. It embeds Settle for config,
// tracker and Code Forge access but never reads the PR forge.
type pushOnlyLander struct{ *Settle }

// land lands a push-only forge, where there is no PR or CI to watch, so
// the issue goes Complete once MERGE_MODE has applied straight against the
// forge's Merge and Rebase. Complete commits after the merge, through
// completeLanding, so a mark landing during the merge's retry backoff abandons
// instead of committing tracker state (issue #4879). A merge failure leaves
// the issue Complete with a merge-blocked note (unless Unclaimed, which has no
// state to move), never demoted to Failed (ADR 0012). A relay that fails with
// its bundle still in the outbox parks the issue Failed instead (issue #4651)
// and returns landingFailed, the transition and comment already done. An
// immediate merge drops the landed bundle here, since only the PR Lander
// runs verifyMerged.
func (s pushOnlyLander) land(_ dispatch.Dispatcher, num string, gen uint64, branch string, _ bool, passes []passmanifest.Entry) (landingResult, string, error) {
	// No CI watch here, so this is the only checkpoint before landing —
	// an aborted run must not merge or commit Complete (issue #3523).
	if s.terminated(num, gen) {
		return landingFailed, "", errAbandoned
	}
	// Merge needs the ref to exist as a branch, but the read-only Box bundled it
	// instead of pushing, so relay it in first (ADR 0033). It runs ahead of the
	// Complete commit because a stop during the relay's retry backoff (issue
	// #4649) has already released num (issue #3523). branch is re-derived from
	// cf.AgentBranch, never trusted from the Agent-controlled outcome line
	// (#1949).
	relayedRef, relayErr := s.relayBoxBundle(num, gen)
	if relayedRef != "" {
		branch = relayedRef
		// Host-derived placeholder the post-merge LandingRef upgrades, so
		// reconcile can still report a merge-blocked branch stuck (#1809).
		s.recordLanding(num, relayedRef, passes)
	}
	if errors.Is(relayErr, errAbandoned) {
		return landingFailed, "", errAbandoned
	}
	if relayFailureParkable(relayErr) {
		s.parkRelayFailure(num, branch, relayErr)
		return landingFailed, "", nil
	}
	err := relayErr
	mergeReason := dispatchrecord.ReasonMergeBlocked
	if err == nil {
		mergeReason, err = s.applyMode(num, gen, branch)
	}
	if errors.Is(err, errAbandoned) {
		return landingFailed, "", errAbandoned
	}
	if err != nil {
		if _, cerr := s.completeLanding(num, gen, landingManual, dispatchrecord.ReasonMergeBlocked); cerr != nil {
			return landingFailed, "", cerr
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusMergeBlocked, Detail: fmt.Sprintf("!! %v", err)})
		s.it.Comment(num, fmt.Sprintf("landing blocked: %v", err))
		return landingManual, "", nil
	}
	landed := mergedLanding(mergeReason)
	if landed != landingMerged {
		landing, err := s.completeLanding(num, gen, landed, mergeReason)
		return landing, "", err
	}
	// Ahead of the re-check: the merge happened and the outbox bundle is
	// not tracker state, so an abandoned landing must still drop it.
	s.removeLandedBundle(num)
	if _, cerr := s.completeLanding(num, gen, landed, mergeReason); cerr != nil {
		return landingFailed, "", cerr
	}
	// CODE_FORGE=local needs the resolved Integration ref and commit sha
	// (ADR 0029/0033), not the placeholder agent branch recorded above, so
	// overwrite it now that Merge has landed. Best-effort: a resolution
	// failure must never turn a successful land into a failure.
	if lr, ok := forge.AsCodeForge[forge.LandingRef](s.cfForNum(num)); ok {
		if landing, err := lr.LandingRef(); err == nil {
			// Re-recording passes is deliberate: it keeps the pair with the
			// upgraded landing.
			s.recordLanding(num, landing, passes)
		} else {
			s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusLandingRefUnresolved, Detail: fmt.Sprintf("!! %v", err)})
		}
	}
	return landed, "", nil
}

// applyMode is applyMergeMode for a push-only forge. It has no auto arm: there
// is no PR to enqueue, and `spindrift recover` skips the startup preflight that
// rejects MERGE_MODE=auto off a PR forge, so the error is raised here too.
func (s pushOnlyLander) applyMode(num string, gen uint64, branch string) (string, error) {
	switch s.cfg.MergeMode {
	case "immediate":
		if err := s.merge(num, gen, branch); err != nil {
			return "", err
		}
		return dispatchrecord.ReasonMerged, nil
	case "auto":
		return "", fmt.Errorf("MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)")
	case "manual":
		s.cfg.Progress.Step(Step{Key: num, Landing: branch, Status: StatusAgentComplete, Detail: "merge-mode=" + s.cfg.MergeMode})
		return dispatchrecord.ReasonManual, nil
	default:
		return "", fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// merge merges branch, rebasing on conflict. No Dispatcher reaches a push-only
// landing, so a rebase that conflicts fails outright.
func (s pushOnlyLander) merge(num string, gen uint64, branch string) error {
	var budget rebaseBudget
	cf := s.cfForNum(num)
	return s.mergeLoop(num, gen, branch, cf, func(conflictErr error) error {
		_, err := s.rebaseOnConflict(num, branch, cf, conflictErr, &budget, nil)
		return err
	})
}

// landingRef returns the outcome's landing as is: land re-derives the branch
// itself, and a read-only Box's bundle is relayed there (ADR 0033).
func (s pushOnlyLander) landingRef(_ dispatch.Dispatcher, _ string, _ uint64, result dispatch.Result) (string, bool) {
	return result.Resolved.Outcome.Landing, true
}

// latchOpenPR is a no-op: a push-only forge has no PR to latch.
func (s pushOnlyLander) latchOpenPR(string) {}

// settleReportedMerge has no PR state to verify. It prints the host-derived
// branch, never the Agent-controlled o.Landing, so landing= carries the same
// provenance as the PR arm.
func (s pushOnlyLander) settleReportedMerge(num string) {
	s.cfg.Progress.Step(Step{Key: num, Landing: s.agentBranch(num), Status: Status("merged")})
}

// landRelayed lands a relayed branch from a bundle alone (ADR 0039, #2254,
// #2378). A forge without forge.BundleRelay has no relayed branch to land.
func (s pushOnlyLander) landRelayed(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) (bool, error) {
	if _, ok := forge.AsCodeForge[forge.BundleRelay](s.cfForNum(num)); !ok {
		return false, nil
	}
	if sit.SelfReportSuccess {
		s.landRelayedBranch(d, num, gen, "genuine success self-report; relayed branch landed", result.Passes)
		return true, nil
	}
	if sit.BundlePresent {
		s.landRelayedBranch(d, num, gen, "bundle present in outbox; relayed branch landed", result.Passes)
		return true, nil
	}
	return false, nil
}

// landRelayedBranch lands num's relayed branch directly, the counterpart to
// the PR Lander's adoptAndGate. The branch comes from the issue's own
// derived agent branch, never an outcome-line field (issue #1949): there may
// be no parsed outcome line at all on this path. note is printed verbatim.
func (s pushOnlyLander) landRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, note string, passes []passmanifest.Entry) {
	s.gateAdopted(d, num, gen, s.agentBranch(num), note, passes)
}

// salvage promotes a local push-only issue to Recoverable (ADR 0039) when
// either a genuine success self-report or a signal kill (#2378)
// coincides with a bundle in the outbox, leaving the land itself to `spindrift
// recover`. It only stats the outbox, so no local issue is ever
// fast-forwarded on unauthenticated evidence alone. A success self-report
// that is unreviewed, or a signal-killed run whose loop stopped before its
// land pass, fails the issue instead, since recover would decline that bundle
// (issues #5973, #5984). True means the issue is settled, Recoverable or
// Failed.
func (s pushOnlyLander) salvage(d dispatch.Dispatcher, num string, _ uint64, result dispatch.Result) bool {
	cf := s.cfForNum(num)
	selfReportOK := result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status)
	if _, ok := forge.AsCodeForge[forge.BundleRelay](cf); !ok ||
		(!selfReportOK && !result.KilledBySignal) ||
		!s.bundlePresent(num) {
		return false
	}
	if s.situationFor(num, false, result).Unreviewed {
		s.failUnreviewed(d, num, s.unreviewedLanding(num, result))
		return true
	}
	reason := "genuine success self-report"
	if !selfReportOK {
		reason = "killed by signal"
	}
	s.cfg.Progress.Step(Step{Key: num, Status: StatusRecoverable, Detail: fmt.Sprintf("note=%s; bundle present in outbox; run `spindrift recover %s` to land it", reason, num)})
	s.transitionState(num, ForReason(dispatchrecord.ReasonRecoverable), reason)
	return true
}
