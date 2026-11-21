package settle

import (
	"errors"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/retry"
)

// errAbandoned marks a settle that stopped because another actor now owns the
// issue: a Reclaim (ADR 0024, issue #649), which already did the transition,
// comment and log line, or the issue's own live Box, which made a fix-pass or
// conflict-resolve Box skip (issue #3655). The caller must post, print and
// commit nothing further.
var errAbandoned = errors.New("settle: abandoned (terminated, or a live run owns the issue)")

// selfHeal lands num through the Lander New chose.
func (s *Settle) selfHeal(d dispatch.Dispatcher, num string, gen uint64, pr string, passes []passmanifest.Entry) (landingResult, string, error) {
	return s.lander.land(d, num, gen, pr, false, passes)
}

// selfHealAdopted is selfHeal's counterpart for a landing discovered
// independently of this process's own push (settleAdopted's resume path); the
// PR Lander's registration-evidence requirement (see selfHealGate) applies
// only there.
func (s *Settle) selfHealAdopted(d dispatch.Dispatcher, num string, gen uint64, pr string) (landingResult, string, error) {
	return s.lander.land(d, num, gen, pr, true, nil)
}

// completeLanding is the single Complete-commit gate for the gateGreen arm and
// the push-only Lander. The Registry mark is sticky, so re-checking here
// catches a mark that landed during the forge calls before it (MarkReady and
// applying the merge mode): Reclaim already moved num to Dispatchable, and a
// Complete commit on top would leave agent-complete that Reconcile's
// InProgress-only sweep never clears (issue #3523).
func (s *Settle) completeLanding(num string, gen uint64, landed landingResult, reason string) (landingResult, error) {
	if s.terminated(num, gen) {
		return landingFailed, errAbandoned
	}
	// An unclaimed issue (issue #4076) has no InProgress state to leave, so
	// only an actual merge commits Complete.
	if s.cfg.Unclaimed && landed != landingMerged {
		return landed, nil
	}
	s.transitionState(num, ForReason(reason), "")
	return landed, nil
}

// mergedLanding classifies an applyMergeMode reason: only a reason the
// dispatchrecord table marks Landed is a merge that actually landed.
func mergedLanding(reason string) landingResult {
	if dispatchrecord.ReasonLanded(reason) {
		return landingMerged
	}
	return landingManual
}

// catalogRows is the Chore catalog settle consults for merge-by-hand and
// marker facts; a var so tests can swap in synthetic rows.
var catalogRows = chore.Rows

// wearsFindingLabel reports whether labels include row's finding label.
func wearsFindingLabel(row chore.Row, labels []string) bool {
	return row.FindingLabel != "" && containsLabel(labels, row.FindingLabel)
}

// rebaseBudget counts a caller's rebase attempts and transient-push retries
// across its respondToConflict calls.
type rebaseBudget struct{ attempts, pushRetries int }

// rebaseOnConflict is the Code Forge half of a conflict response, shared by
// both Landers so neither needs s.pr: spend one of budget's rebase attempts
// on ref (returning conflictErr once spent), retrying a transient push
// failure. A rebase that itself conflicts runs resolve when non-nil and
// reports resolved. A clean rebase leaves the caller to wait out whatever the
// force-push reset.
func (s *Settle) rebaseOnConflict(num, ref string, cf forge.CodeForge, conflictErr error, budget *rebaseBudget, resolve func() error) (resolved bool, err error) {
	if budget.attempts >= s.cfg.MaxRebaseAttempts {
		return false, conflictErr
	}
	budget.attempts++
	s.cfg.Progress.Step(Step{Key: num, Landing: ref, Status: StatusRebaseRetry, Detail: fmt.Sprintf("attempt=%d/%d", budget.attempts, s.cfg.MaxRebaseAttempts)})
	rbErr := s.rebase(cf, num, ref)
	for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && budget.pushRetries < s.cfg.MaxRebaseAttempts {
		budget.pushRetries++
		s.cfg.Progress.Step(Step{Key: num, Landing: ref, Status: StatusRebasePushRetry, Detail: fmt.Sprintf("attempt=%d/%d  !! %v", budget.pushRetries, s.cfg.MaxRebaseAttempts, rbErr)})
		s.transientBackoff().Do(budget.pushRetries)
		rbErr = s.rebase(cf, num, ref)
	}
	if rbErr != nil {
		if errors.Is(rbErr, forge.ErrTransientPushFailure) {
			s.cfg.Progress.Step(Step{Key: num, Landing: ref, Status: StatusRebasePushRetriesExhausted, Detail: fmt.Sprintf("attempts=%d  !! %v", budget.pushRetries, rbErr)})
			return false, rbErr
		}
		if errors.Is(rbErr, forge.ErrMergeConflict) && resolve != nil {
			if err := resolve(); err != nil {
				return false, err
			}
			return true, nil
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: ref, Status: StatusRebaseFailed, Detail: fmt.Sprintf("!! %v", rbErr)})
		return false, rbErr
	}
	return false, nil
}

// mergeLoop merges ref through cf, the Code Forge half of an immediate merge
// shared by the PR and push-only Landers. onConflict answers each
// forge.ErrMergeConflict: nil retries the merge, an error ends the loop with
// it. cf is num's own parent-keyed instance when Config.CodeForgeForIssue is
// set (CODE_FORGE=local, issue #1734), otherwise New's cf unchanged.
func (s *Settle) mergeLoop(num string, gen uint64, ref string, cf forge.CodeForge, onConflict func(conflictErr error) error) error {
	checksBlockedAttempts := 0
	transients := s.newRetryLoop(num, gen) // one loop: the transient budget spans every Merge below
	for {
		if s.terminated(num, gen) {
			return errAbandoned
		}
		err := transients.run(StatusMergeTransientRetry, "", ref,
			func() error { return cf.Merge(ref) },
			func(err error) bool { return !errors.Is(err, forge.ErrMergeTransient) }, nil)
		if err == nil {
			return nil
		}
		if errors.Is(err, forge.ErrMergeBlockedByChecks) {
			if checksBlockedAttempts >= s.cfg.MaxRebaseAttempts {
				return err
			}
			checksBlockedAttempts++
			s.cfg.Progress.Step(Step{Key: num, Landing: ref, Status: StatusMergeBlockedByChecks, Detail: fmt.Sprintf("attempt=%d/%d", checksBlockedAttempts, s.cfg.MaxRebaseAttempts)})
			s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if err := onConflict(err); err != nil {
			return err
		}
	}
}

// transientBackoff builds the jittered linear backoff the rebase-push, merge
// transient, bundle-relay, and verifyMerged read retry loops share, so the call
// sites cannot drift apart (issue #2095, issue #4649).
func (s *Settle) transientBackoff() retry.LinearBackoff {
	b := s.cfg.Policy.Backoff(s.clock)
	b.Jitter = s.cfg.Policy.Jitter
	return b
}

// relayBoxBundle relays num's outbox bundle in via the Code Forge's optional
// forge.BundleRelay hook (issue #1919), for callers whose Box may have bundled
// instead of pushing. ref is the branch it relays, empty (and a no-op) when the
// Code Forge is not a BundleRelay.
func (s *Settle) relayBoxBundle(num string, gen uint64) (ref string, err error) {
	cf := s.cfForNum(num)
	br := forge.ResolveCodeForge(cf).BundleRelay
	if br == nil {
		return "", nil
	}
	ref = s.agentBranch(num)
	if s.cfg.OutboxDir == nil {
		return ref, fmt.Errorf("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay — every CODE_FORGE=local construction site must supply an OutboxDir resolver")
	}
	if err := s.retryingRelay(num, gen, br).RelayBundle(s.cfg.OutboxDir(num), ref); err != nil {
		return ref, fmt.Errorf("%w: %w", errRelayBundle, err)
	}
	return ref, nil
}
