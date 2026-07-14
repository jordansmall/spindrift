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

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// MaxFixAttempts times. On green it swaps agent-complete (via gateToGreen)
// then applies the merge mode; a merge failure after green leaves the issue
// agent-complete and is never demoted to agent-failed.
//
// Returns LandingFailed when CI never reached green (genuine red exhausted
// or a gate timeout — the issue is swapped to failedLabel). Otherwise CI
// reached green: LandingMerged when immediate mode completed an actual
// merge, LandingManual for every other green outcome (manual/auto mode, a
// guard hit, or a merge failure — the issue stays at agent-complete with a
// merge-blocked note).
//
// d dispatches fix passes and, when a rebase conflict arises, an
// agent-assisted conflict resolution -- both subject to dispatch's own
// in-session transient retry (issue #441).
func (s *Settle) selfHeal(d dispatch.Dispatcher, num, pr string) LandingResult {
	if s.pr == nil {
		return s.landPushOnly(num, pr)
	}
	for attempt := 0; ; attempt++ {
		switch s.gateToGreen(num, pr) {
		case GateGreen:
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				return LandingManual
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				return LandingManual
			}
			if err := s.applyMergeMode(num, pr, d); err != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				return LandingManual
			}
			if s.cfg.MergeMode == "immediate" {
				return LandingMerged
			}
			return LandingManual
		case GateTerminal:
			s.transitionState(num, forge.InProgress, forge.Failed)
			return LandingFailed
		case GateRedRetry:
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					fmt.Printf("    #%s  pr=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
						num, pr, s.cfg.MaxFixAttempts)
				}
				s.transitionState(num, forge.InProgress, forge.Failed)
				return LandingFailed
			}
			fmt.Printf("    #%s  pr=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
			// Best-effort: a failure to fetch the CI failure detail must
			// never block the fix pass — fall back to an empty summary.
			detail, detailErr := s.pr.FailureDetail(pr)
			if detailErr != nil {
				fmt.Printf("    #%s  pr=%s  status=failure-detail-unavailable  !! %v\n", num, pr, detailErr)
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
func (s *Settle) landPushOnly(num, branch string) LandingResult {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, branch, nil); err != nil {
		fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("merge blocked after push: %v", err))
		return LandingManual
	}
	if s.cfg.MergeMode == "immediate" {
		return LandingMerged
	}
	return LandingManual
}

// gateToGreen polls CheckState on the PR's head commit until the state
// reaches confirmed SUCCESS, a terminal failure, or MergePollTimeout seconds
// elapse. On confirmed green, agent-complete is swapped unconditionally.
//
// Returns:
//   - GateGreen     — CI confirmed green; issue swapped to CompleteLabel.
//   - GateRedRetry  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box. No label swap performed.
//   - GateTerminal  — non-retriable outcome (timeout, API error); no label
//     swap. Caller must swap to failedLabel.
func (s *Settle) gateToGreen(num, pr string) GateResult {
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
		state, stateErr := s.pr.CheckState(pr)
		if stateErr != nil {
			fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, stateErr)
			return GateTerminal
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
				fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, confirmErr)
				return GateTerminal
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return GateRedRetry
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			// Confirmed green: mark complete regardless of merge outcome.
			s.transitionState(num, forge.InProgress, forge.Complete)
			return GateGreen
		case forge.StateFailure, forge.StateError:
			// Genuine red — signal caller so it can dispatch a fix pass.
			return GateRedRetry
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
	return GateTerminal
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
			fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		fmt.Printf("    #%s  pr=%s  status=agent-complete  merge-mode=%s\n", num, pr, s.cfg.MergeMode)
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
		err := s.cf.Merge(pr)
		if err == nil {
			return nil
		}
		if errors.Is(err, forge.ErrMergeBlockedByChecks) {
			if checksBlockedAttempts >= s.cfg.MaxRebaseAttempts {
				return err
			}
			checksBlockedAttempts++
			fmt.Printf("    #%s  pr=%s  status=merge-blocked-by-checks  attempt=%d/%d\n",
				num, pr, checksBlockedAttempts, s.cfg.MaxRebaseAttempts)
			time.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			skipRebase = false
			fmt.Printf("    #%s  pr=%s  status=merge-retry-settle\n", num, pr)
			time.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if rebaseAttempts >= s.cfg.MaxRebaseAttempts {
			return err
		}
		rebaseAttempts++
		fmt.Printf("    #%s  pr=%s  status=rebase-retry  attempt=%d/%d\n",
			num, pr, rebaseAttempts, s.cfg.MaxRebaseAttempts)
		rbErr := s.cf.Rebase(pr)
		for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts {
			pushRetries++
			fmt.Printf("    #%s  pr=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
				num, pr, pushRetries, s.cfg.MaxRebaseAttempts, rbErr)
			rbErr = s.cf.Rebase(pr)
		}
		if rbErr != nil {
			if errors.Is(rbErr, forge.ErrTransientPushFailure) {
				fmt.Printf("    #%s  pr=%s  status=rebase-push-retries-exhausted  attempts=%d  !! %v\n",
					num, pr, pushRetries, rbErr)
				return rbErr
			}
			if errors.Is(rbErr, forge.ErrMergeConflict) && d != nil {
				fmt.Printf("    #%s  pr=%s  status=conflict-resolve\n", num, pr)
				if crErr := d.ResolveConflict(pr); crErr != nil {
					fmt.Printf("    #%s  pr=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
					return crErr
				}
				if rwErr := s.rewaitAfterForcePush(num, pr); rwErr != nil {
					return rwErr
				}
				skipRebase = true
			} else {
				fmt.Printf("    #%s  pr=%s  status=rebase-failed  !! %v\n", num, pr, rbErr)
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
	fmt.Printf("    #%s  pr=%s  status=post-force-push-wait\n", num, pr)
	if s.gateToGreen(num, pr) != GateGreen {
		return fmt.Errorf("CI did not reach green after force-push on %s", pr)
	}
	return nil
}
