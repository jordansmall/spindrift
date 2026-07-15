package settle

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// This file holds the ready path end to end — gate, guard, merge, re-wait —
// so a reader can follow the common green path (selfHeal → gateToGreen →
// mergeGuardHit → applyMergeMode → mergeImmediate) top-to-bottom in one
// place instead of jumping between files.

// errAbandoned is mergeImmediate's signal that a Terminate (ADR 0024, issue
// #649) landed mid-retry: distinct from a genuine merge failure so the
// caller skips the merge-blocked print/comment instead of reporting one on
// an issue Terminate already reclaimed.
var errAbandoned = errors.New("settle: abandoned by terminate")

// errLandingNeverGreen marks a force-pushed head (rebase or conflict-resolve)
// that never reached green — a conflict-resolve dispatch failure, or a
// post-force-push re-wait that ends red or times out. Distinct from a merge
// failure on an already-green PR: there, a green PR genuinely exists and the
// issue stays agent-complete (ADR 0012). Here there is no green PR at the
// current head, so selfHeal demotes to agent-failed instead (issue #758).
var errLandingNeverGreen = errors.New("settle: force-pushed head never went green")

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// MaxFixAttempts times. On green it applies the merge mode, then swaps
// agent-complete once the landing path settles (issue #757) — merged,
// auto-merge enqueued, manual hand-off, merge-blocked-with-note, or a merge
// guard downgrade all count as settled. Until then (rebase-retry,
// conflict-resolve, post-force-push-wait) the issue stays agent-in-progress.
// A merge failure on a still-green PR (unmet approval, guard, unresolvable
// pre-rebase conflict) leaves the issue agent-complete, never demoted; but a
// force-pushed head that never re-confirms green (a failed conflict-resolve
// dispatch, or a red/timed-out post-force-push re-wait) demotes to
// agent-failed instead — there is no green PR left at that head (issue #758).
//
// Returns landingFailed when CI never reached green (genuine red exhausted,
// a gate timeout, or a force-pushed head that never went green — the issue
// is swapped to failedLabel). Otherwise CI reached green: landingMerged when
// immediate mode completed an actual merge, landingManual for every other
// green outcome (manual/auto mode, a guard hit, or a merge failure on a
// still-green PR — the issue stays at agent-complete with a merge-blocked
// note).
//
// d dispatches fix passes and, when a rebase conflict arises, an
// agent-assisted conflict resolution -- both subject to dispatch's own
// in-session transient retry (issue #441).
func (s *Settle) selfHeal(d dispatch.Dispatcher, num, pr string) landingResult {
	if s.pr == nil {
		return s.landPushOnly(num, pr)
	}
	for attempt := 0; ; attempt++ {
		switch s.gateToGreen(num, pr) {
		case gateAbandoned:
			return landingAbandoned
		case gateGreen:
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual
			}
			if err := s.applyMergeMode(num, pr, d); err != nil {
				if errors.Is(err, errAbandoned) {
					return landingAbandoned
				}
				if errors.Is(err, errLandingNeverGreen) {
					fmt.Printf("    #%s  landing=%s  status=landing-failed  !! %v\n", num, pr, err)
					s.it.Comment(num, fmt.Sprintf("landing failed: %v — no green PR exists at the current head", err))
					s.transitionState(num, forge.InProgress, forge.Failed)
					return landingFailed
				}
				fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual
			}
			// The landing path has settled — merged, auto-merge enqueued, or
			// manual hand-off. Only now does agent-complete claim the agent
			// has nothing left to do.
			s.transitionState(num, forge.InProgress, forge.Complete)
			if s.cfg.MergeMode == "immediate" {
				return landingMerged
			}
			return landingManual
		case gateTerminal:
			s.transitionState(num, forge.InProgress, forge.Failed)
			return landingFailed
		case gateRedRetry:
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					fmt.Printf("    #%s  landing=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
						num, pr, s.cfg.MaxFixAttempts)
				}
				s.transitionState(num, forge.InProgress, forge.Failed)
				return landingFailed
			}
			fmt.Printf("    #%s  landing=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
			// Best-effort: a failure to fetch the CI failure detail must
			// never block the fix pass — fall back to an empty summary.
			detail, detailErr := s.pr.FailureDetail(pr)
			if detailErr != nil {
				fmt.Printf("    #%s  landing=%s  status=failure-detail-unavailable  !! %v\n", num, pr, detailErr)
				detail = ""
			}
			if result := d.Fix(attempt+1, detail); !result.Success {
				fmt.Printf("    !! #%s fix-pass-%d exited non-zero\n", num, attempt+1)
			}
		}
	}
}

// landPushOnly is the push-only-forge counterpart to the gateToGreen+
// applyMergeMode pair: there is no PR or CI to watch (the Box already pushed
// branch to the remote), so the issue is marked Complete immediately and
// MERGE_MODE is applied straight against the push-only forge's Merge/Rebase.
// A merge failure leaves the issue Complete with a merge-blocked note,
// matching the github adapter's post-green contract (ADR 0012) — it is never
// demoted to Failed.
func (s *Settle) landPushOnly(num, branch string) landingResult {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, branch, nil); err != nil {
		fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("merge blocked after push: %v", err))
		return landingManual
	}
	if s.cfg.MergeMode == "immediate" {
		return landingMerged
	}
	return landingManual
}

// gateToGreen polls CheckState on the PR's head commit until the state
// reaches confirmed SUCCESS, a terminal failure, or MergePollTimeout seconds
// elapse. It performs no label swap itself — the caller (selfHeal) owns
// agent-complete, swapping it only once the landing path settles (issue
// #757), since gateToGreen also re-runs mid-landing (rewaitAfterForcePush)
// where a swap would be premature.
//
// Returns:
//   - gateGreen     — CI confirmed green.
//   - gateRedRetry  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box.
//   - gateTerminal  — non-retriable outcome (timeout, API error). Caller
//     must swap to failedLabel.
func (s *Settle) gateToGreen(num, pr string) gateResult {
	pollIv := s.cfg.MergePollInterval
	deadline := s.cfg.MergePollTimeout
	// actualIv is used for elapsed tracking; floor to 1 so we don't
	// hot-spin. When pollIv is 0 (test mode) the sleep duration is also 0,
	// so elapsed still advances and the loop terminates.
	actualIv := pollIv
	if actualIv <= 0 {
		actualIv = 1
	}
	elapsed := 0

	for {
		if s.terminated(num) {
			return gateAbandoned
		}
		state, stateErr := s.pr.CheckState(pr)
		if stateErr != nil {
			fmt.Printf("    #%s  landing=%s  status=check-state-error  !! %v\n", num, pr, stateErr)
			return gateTerminal
		}

		switch state {
		case forge.StateSuccess:
			// Pause before confirming — back-to-back GraphQL calls return the
			// same snapshot, so a late-registered job would not yet appear.
			time.Sleep(time.Duration(pollIv) * time.Second)
			// Re-poll to confirm the snapshot is stable. A partial check
			// registration can briefly show SUCCESS before all jobs appear.
			confirm, confirmErr := s.pr.CheckState(pr)
			if confirmErr != nil {
				fmt.Printf("    #%s  landing=%s  status=check-state-error  !! %v\n", num, pr, confirmErr)
				return gateTerminal
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return gateRedRetry
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			return gateGreen
		case forge.StateFailure, forge.StateError:
			// Genuine red — signal caller so it can dispatch a fix pass.
			return gateRedRetry
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		time.Sleep(time.Duration(pollIv) * time.Second)
		elapsed += actualIv
	}
	return gateTerminal
}

// mergeGuardHit checks a green PR's changed files against MergeGuardPaths,
// returning the subset that hit a guarded glob. A nil, nil result means the
// guard is disabled (empty patterns) or found no match; a non-nil error means
// the changed-file list could not be read at all.
func (s *Settle) mergeGuardHit(pr string) ([]string, error) {
	if strings.TrimSpace(s.cfg.MergeGuardPaths) == "" {
		return nil, nil
	}
	files, err := s.pr.ListPRFiles(pr)
	if err != nil {
		return nil, err
	}
	return matchedGuardPaths(s.cfg.MergeGuardPaths, files), nil
}

// applyMergeMode performs the mode-specific action after CI reaches green.
// agent-complete is already set; a merge failure is returned as an error but
// does not revert the label.
//
// d, when non-nil, resolves rebase conflicts (via d.ResolveConflict) that
// arise while mergeImmediate retries. When nil, a rebase conflict is
// immediately non-retriable.
func (s *Settle) applyMergeMode(num, pr string, d dispatch.Dispatcher) error {
	switch s.cfg.MergeMode {
	case "immediate":
		return s.mergeImmediate(num, pr, d)
	case "auto":
		if s.pr == nil {
			return fmt.Errorf("MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)")
		}
		if err := s.pr.EnqueueAutoMerge(pr); err != nil {
			fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		fmt.Printf("    #%s  landing=%s  status=agent-complete  merge-mode=%s\n", num, pr, s.cfg.MergeMode)
		return nil
	default:
		return fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// mergeImmediate attempts to merge the green PR with rebase retry on conflict.
// It embodies the existing rebase-retry and agent conflict-resolve behaviors.
//
// A successful conflict-resolve already rebased and force-pushed the branch,
// so the next Merge conflict is retried directly (after a brief settle wait
// for the forge's mergeability snapshot to catch up) instead of invoking
// Rebase a second time.
//
// A Rebase force-push failure that forge.ErrTransientPushFailure wraps (an
// infra or network fault, not a genuine stale-lease rejection) is retried up
// to MaxRebaseAttempts times before it's treated as terminal.
func (s *Settle) mergeImmediate(num, pr string, d dispatch.Dispatcher) error {
	rebaseAttempts := 0
	pushRetries := 0
	checksBlockedAttempts := 0
	skipRebase := false
	for {
		if s.terminated(num) {
			return errAbandoned
		}
		err := s.cf.Merge(pr)
		if err == nil {
			return nil
		}
		if errors.Is(err, forge.ErrMergeBlockedByChecks) {
			if checksBlockedAttempts >= s.cfg.MaxRebaseAttempts {
				return err
			}
			checksBlockedAttempts++
			fmt.Printf("    #%s  landing=%s  status=merge-blocked-by-checks  attempt=%d/%d\n",
				num, pr, checksBlockedAttempts, s.cfg.MaxRebaseAttempts)
			time.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			skipRebase = false
			fmt.Printf("    #%s  landing=%s  status=merge-retry-settle\n", num, pr)
			time.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if rebaseAttempts >= s.cfg.MaxRebaseAttempts {
			return err
		}
		rebaseAttempts++
		fmt.Printf("    #%s  landing=%s  status=rebase-retry  attempt=%d/%d\n",
			num, pr, rebaseAttempts, s.cfg.MaxRebaseAttempts)
		rbErr := s.cf.Rebase(pr)
		for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts {
			pushRetries++
			fmt.Printf("    #%s  landing=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
				num, pr, pushRetries, s.cfg.MaxRebaseAttempts, rbErr)
			rbErr = s.cf.Rebase(pr)
		}
		if rbErr != nil {
			if errors.Is(rbErr, forge.ErrTransientPushFailure) {
				fmt.Printf("    #%s  landing=%s  status=rebase-push-retries-exhausted  attempts=%d  !! %v\n",
					num, pr, pushRetries, rbErr)
				return rbErr
			}
			if errors.Is(rbErr, forge.ErrMergeConflict) && d != nil {
				fmt.Printf("    #%s  landing=%s  status=conflict-resolve\n", num, pr)
				if crErr := d.ResolveConflict(pr); crErr != nil {
					fmt.Printf("    #%s  landing=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
					return fmt.Errorf("%w: conflict-resolve dispatch failed: %v", errLandingNeverGreen, crErr)
				}
				if rwErr := s.rewaitAfterForcePush(num, pr); rwErr != nil {
					return rwErr
				}
				skipRebase = true
			} else {
				fmt.Printf("    #%s  landing=%s  status=rebase-failed  !! %v\n", num, pr, rbErr)
				return rbErr
			}
			continue
		}
		// Rebase succeeded: the force-push reset the PR's required checks, so
		// the next merge attempt must wait for the new head to go green
		// rather than retrying against checks the push itself just reset.
		if rwErr := s.rewaitAfterForcePush(num, pr); rwErr != nil {
			return rwErr
		}
	}
}

// rewaitAfterForcePush blocks for CI to reach green on the PR's current head
// after a gate-driven force-push (rebase or conflict-resolve) reset its
// required checks. It reuses gateToGreen's timeout/poll bounds, so a wait
// that ends in genuine CI failure or a timeout returns an error distinct from
// forge.ErrMergeConflict — the caller's conflict-retry path is never
// re-entered for it.
func (s *Settle) rewaitAfterForcePush(num, pr string) error {
	fmt.Printf("    #%s  landing=%s  status=post-force-push-wait\n", num, pr)
	if s.gateToGreen(num, pr) != gateGreen {
		return fmt.Errorf("%w: CI did not reach green after force-push on %s", errLandingNeverGreen, pr)
	}
	return nil
}
