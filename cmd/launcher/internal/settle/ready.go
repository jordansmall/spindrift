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
func (s *Settle) selfHeal(d dispatch.Dispatcher, num string, gen uint64, pr string) landingResult {
	return s.selfHealGate(d, num, gen, pr, false)
}

// selfHealAdopted is selfHeal's counterpart for a PR discovered independently
// of this process's own push (SettleAdopted's resume/recovery path — a Box
// exited with no outcome line). Unlike selfHeal, it cannot assume the PR's
// current head SHA is one this process just pushed, so its first CI gate
// poll requires evidence this run's checks registered before trusting a
// SUCCESS rollup (issue #1652).
func (s *Settle) selfHealAdopted(d dispatch.Dispatcher, num string, gen uint64, pr string) landingResult {
	return s.selfHealGate(d, num, gen, pr, true)
}

// selfHealGate is selfHeal and selfHealAdopted's shared implementation;
// requireRegistration guards only the loop's first attempt — a fix-pass
// retry always follows a push d.Fix just made in this process, so it is
// never ambiguous the way the initial adopted poll can be.
func (s *Settle) selfHealGate(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool) landingResult {
	if s.pr == nil {
		return s.landPushOnly(num, gen, pr)
	}
	for attempt := 0; ; attempt++ {
		switch s.gateToGreen(num, gen, pr, requireRegistration && attempt == 0) {
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
			// The launcher owns the draft->ready flip at green, ahead of the
			// merge itself (issue #1651) — the Driver itself never flips a PR
			// ready anymore (#1653), and a no-outcome run is never adopted as
			// ready off draft-ness either (#1654), completing the inversion
			// of the old draft-until-ready invariant (#1614/#1625). MarkReady
			// is idempotent, so this runs unconditionally; a failure only
			// reaches the console log below (never a public issue comment),
			// so it never blocks the merge, matching EnqueueAutoMerge's
			// best-effort precedent below.
			if err := s.pr.MarkReady(pr); err != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-ready-failed  !! %v\n", num, pr, err)
			}
			if err := s.applyMergeMode(num, gen, pr, d); err != nil {
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
func (s *Settle) landPushOnly(num string, gen uint64, branch string) landingResult {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, gen, branch, nil); err != nil {
		fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("landing blocked: %v", err))
		return landingManual
	}
	if s.cfg.MergeMode == "immediate" {
		// CODE_FORGE=local's landing: needs the resolved Integration ref +
		// commit sha (ADR 0029/0033), richer than the raw branch name
		// recordLanding already wrote when the outcome line was parsed — so
		// overwrite it now that Merge has actually landed. Best-effort: a
		// resolution failure here is surprising (the merge just succeeded)
		// but must never turn an actual successful land into a failure.
		if lr, ok := s.cf.(forge.LandingRef); ok {
			if landing, err := lr.LandingRef(); err == nil {
				s.recordLanding(num, landing)
			} else {
				fmt.Printf("    #%s  landing=%s  status=landing-ref-unresolved  !! %v\n", num, branch, err)
			}
		}
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
// requireRegistration guards against trusting a rollup this run never
// watched register (issue #1652): an unchanged head SHA can carry a
// terminal SUCCESS inherited from an earlier attempt, so when set, a
// first-poll SUCCESS is not accepted until a non-terminal state
// (PENDING/EXPECTED/NONE) has been observed first — proof this run's own
// checks are alive on the head commit. A caller that just performed the
// push itself (the normal ready path, and any post-force-push rewait) has
// no such ambiguity and passes false, preserving the original trust-on-
// first-poll behavior.
//
// Returns:
//   - gateGreen     — CI confirmed green.
//   - gateRedRetry  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box.
//   - gateTerminal  — non-retriable outcome (timeout, API error). Caller
//     must swap to failedLabel.
func (s *Settle) gateToGreen(num string, gen uint64, pr string, requireRegistration bool) gateResult {
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
	registered := !requireRegistration

	for {
		if s.terminated(num, gen) {
			return gateAbandoned
		}
		state, stateErr := s.pr.CheckState(pr)
		if stateErr != nil {
			fmt.Printf("    #%s  landing=%s  status=check-state-error  !! %v\n", num, pr, stateErr)
			return gateTerminal
		}
		if state != forge.StateSuccess && state != forge.StateFailure && state != forge.StateError {
			registered = true
		}

		switch state {
		case forge.StateSuccess:
			if !registered {
				// No evidence yet that this run's own checks registered —
				// wait rather than trust a possibly-inherited rollup.
				break
			}
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
func (s *Settle) applyMergeMode(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	switch s.cfg.MergeMode {
	case "immediate":
		return s.mergeImmediate(num, gen, pr, d)
	case "auto":
		if s.pr == nil {
			return fmt.Errorf("MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)")
		}
		if err := s.pr.EnqueueAutoMerge(pr); err != nil {
			// Audited (issue #1233, extending #831): err traces through
			// execClient.EnqueueAutoMerge (github/exec_pr.go), which runs
			// `gh pr merge --auto --rebase --delete-branch` via
			// exec.Command(...).Run() with no stdout/stderr capture. So err
			// is only ever *exec.ExitError, a start failure, or the wrapped
			// message embedding prURL (already public) — never gh's stderr
			// text. Safe to surface verbatim in the issue comment below.
			fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		note := ""
		if _, ok := s.cf.(forge.BundleRelay); ok {
			// Manual mode never calls RelayBundle (that only happens inside
			// mergeImmediate's immediate-mode path), so a local seam's outbox
			// bundle sits unrelayed until a later immediate-mode run -- worth
			// a loud note, since there's nothing else to point the operator
			// at it the way a pushed branch does for git.
			note = "  note=bundle left in outbox, not relayed (requires MERGE_MODE=immediate to land under CODE_FORGE=local)"
		}
		fmt.Printf("    #%s  landing=%s  status=agent-complete  merge-mode=%s%s\n", num, pr, s.cfg.MergeMode, note)
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
//
// The termination check ahead of preflightStaleBase (issue #943) is
// deliberately duplicated by the loop's own first-iteration check below
// rather than relied on alone: preflightStaleBase itself force-pushes, so a
// terminated issue must never reach it, not just never reach Merge.
func (s *Settle) mergeImmediate(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	rebaseAttempts := 0
	pushRetries := 0
	checksBlockedAttempts := 0
	skipRebase := false
	if s.terminated(num, gen) {
		return errAbandoned
	}
	// preflightStaleBase gets its own attempt budget rather than sharing
	// rebaseAttempts/pushRetries with the reactive conflict-retry loop
	// below: a stale-base rebase and a conflict-triggered rebase are
	// independent concerns, and charging one against the other's budget
	// would let a stale-base retry exhaust the conflict path's allowance
	// before a real conflict ever arises (or vice versa).
	if err := s.preflightStaleBase(num, gen, pr, d); err != nil {
		return err
	}
	// CODE_FORGE=local's Merge assumes ref already exists as a branch on the
	// backing repo, exactly like git/github — but the Box's read-only repo
	// mount means it never pushed there directly. Relay the Box's code-out
	// bundle in first, once, so the loop below's Merge(pr) attempts find the
	// ref (ADR 0033). A relay failure (missing/malformed bundle) is returned
	// directly: there is nothing to retry, unlike a merge conflict below.
	if br, ok := s.cf.(forge.BundleRelay); ok {
		if s.cfg.OutboxDir == nil {
			return fmt.Errorf("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay — every CODE_FORGE=local construction site must supply an OutboxDir resolver")
		}
		if err := br.RelayBundle(s.cfg.OutboxDir(num), pr); err != nil {
			return err
		}
	}
	for {
		if s.terminated(num, gen) {
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
				if crErr := s.resolveConflict(num, pr, d); crErr != nil {
					return crErr
				}
				if rwErr := s.rewaitAfterForcePush(num, gen, pr); rwErr != nil {
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
		if rwErr := s.rewaitAfterForcePush(num, gen, pr); rwErr != nil {
			return rwErr
		}
	}
}

// preflightStaleBase proactively rebases pr when the forge reports its
// branch is behind its base (NeedsUpdate — issue #936) — even though the PR
// shows no textual conflict and CI is already green on its current head. A
// green PR can still be stale: main may have advanced past a just-merged
// sibling whose changes the PR's tested tree never saw.
//
// It is opt-in via PreflightStaleBase (ADR 0028): off by default, a
// green-but-behind PR merges as-is, and this returns immediately without even
// querying NeedsUpdate — no wasted compare-API round-trip and no extra
// rebase+CI cycle on the near-constant "behind main because a sibling landed
// first" case. Turn it on to restore ADR 0026's behavior where a stale base
// is treated as a conflict requiring rebase-and-re-green before merge.
//
// When enabled it reuses the same
// Rebase/rewaitAfterForcePush path the reactive conflict-retry loop below
// uses, but with its own single-attempt-plus-push-retry budget — not the
// loop's rebaseAttempts/pushRetries counters — since a stale-base rebase and
// a conflict-triggered rebase are independent concerns; sharing a budget
// would let one exhaust the other's allowance before it ever gets to run.
//
// A NeedsUpdate query error is logged and swallowed rather than returned:
// staleness is merely unknown, and the caller's normal Merge attempt will
// surface the same underlying problem (a genuine conflict, blocked checks,
// or a clean merge if the staleness turns out to be harmless) through its
// own, already-tested error handling.
//
// A Rebase failure — including one that persists past its push-retry
// budget — is different: staleness is confirmed and the corrective action
// itself failed. A genuine ErrMergeConflict falls through to the same
// ResolveConflict dispatch the reactive conflict-retry loop below uses
// (issue #1319) when a Dispatcher is in scope; any other Rebase error, or a
// conflict with no Dispatcher available, is returned as a hard,
// merge-blocking error (issue #940) rather than falling through to Merge on
// a base known to be stale and never re-validated. rewaitAfterForcePush's
// own contract (a rebase that force-pushes but never re-confirms green) is
// likewise a hard failure, for the same reason: staleness confirmed, fix
// attempted, fix unconfirmed.
func (s *Settle) preflightStaleBase(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	if s.pr == nil || !s.cfg.PreflightStaleBase {
		return nil
	}
	stale, err := s.pr.NeedsUpdate(pr)
	if err != nil {
		fmt.Printf("    #%s  landing=%s  status=needs-update-check-error  !! %v\n", num, pr, err)
		return nil
	}
	if !stale || s.cfg.MaxRebaseAttempts <= 0 {
		return nil
	}
	fmt.Printf("    #%s  landing=%s  status=stale-base-rebase  attempt=1/%d\n", num, pr, s.cfg.MaxRebaseAttempts)
	rbErr := s.cf.Rebase(pr)
	for pushRetries := 0; rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts; pushRetries++ {
		fmt.Printf("    #%s  landing=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
			num, pr, pushRetries+1, s.cfg.MaxRebaseAttempts, rbErr)
		rbErr = s.cf.Rebase(pr)
	}
	if rbErr != nil {
		if errors.Is(rbErr, forge.ErrMergeConflict) && d != nil {
			if crErr := s.resolveConflict(num, pr, d); crErr != nil {
				return crErr
			}
			// No skipRebase equivalent needed here (contrast the reactive
			// loop's post-resolve skipRebase=true): the caller's loop hasn't
			// started yet, so mergeImmediate's first Merge attempt runs
			// fresh once rewaitAfterForcePush confirms the resolved head is
			// green, rather than re-entering a rebase it already did.
			return s.rewaitAfterForcePush(num, gen, pr)
		}
		fmt.Printf("    #%s  landing=%s  status=stale-base-rebase-failed  !! %v\n", num, pr, rbErr)
		return rbErr
	}
	return s.rewaitAfterForcePush(num, gen, pr)
}

// resolveConflict dispatches a Box to resolve a genuine ErrMergeConflict
// hit by a force-pushing rebase, shared by preflightStaleBase and
// mergeImmediate's reactive conflict-retry loop above.
func (s *Settle) resolveConflict(num, pr string, d dispatch.Dispatcher) error {
	fmt.Printf("    #%s  landing=%s  status=conflict-resolve\n", num, pr)
	if crErr := d.ResolveConflict(pr); crErr != nil {
		// Audited (issue #831): crErr traces through
		// dispatch.Dispatch.ResolveConflict -> runOnce (dispatch/box.go) ->
		// runner.Runner.Run. Both the OCI and bwrap adapters wire the
		// Box's stdout/stderr to the log file, not to the returned error,
		// so crErr is only ever *exec.ExitError or a start failure (missing
		// binary, mkdtemp/file error) — never Box-internal output. Safe to
		// surface verbatim in the issue comment posted from this error at
		// ready.go's selfHeal.
		fmt.Printf("    #%s  landing=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
		return fmt.Errorf("%w: conflict-resolve dispatch failed: %v", errLandingNeverGreen, crErr)
	}
	return nil
}

// rewaitAfterForcePush blocks for CI to reach green on the PR's current head
// after a gate-driven force-push (rebase or conflict-resolve) reset its
// required checks. It reuses gateToGreen's timeout/poll bounds, so a wait
// that ends in genuine CI failure or a timeout returns an error distinct from
// forge.ErrMergeConflict — the caller's conflict-retry path is never
// re-entered for it.
//
// A no-op for a push-only forge (s.pr == nil, git and local alike): there is
// no CI to wait for, so the force-push having succeeded is itself enough —
// mirroring preflightStaleBase's own s.pr == nil guard. Without this, the
// reactive conflict-retry loop's rebase-succeeded branch would call
// gateToGreen unconditionally and crash on s.pr.CheckState (issue found in
// review of #1698): a merge conflict followed by a clean rebase, landing
// on the retry, is a routine occurrence for CODE_FORGE=local specifically,
// where concurrent seams commonly land onto the same Integration branch.
func (s *Settle) rewaitAfterForcePush(num string, gen uint64, pr string) error {
	if s.pr == nil {
		return nil
	}
	fmt.Printf("    #%s  landing=%s  status=post-force-push-wait\n", num, pr)
	return rewaitGateResultErr(s.gateToGreen(num, gen, pr, false), pr)
}

// rewaitGateResultErr maps a gateToGreen outcome to rewaitAfterForcePush's
// return value, naming gateTerminal and gateRedRetry explicitly rather than
// folding them into a catch-all default — so a future gateResult variant
// must be handled here too, loudly (panic), instead of silently landing on
// "never green" (issue #1175).
func rewaitGateResultErr(g gateResult, pr string) error {
	switch g {
	case gateGreen:
		return nil
	case gateAbandoned:
		return errAbandoned
	case gateTerminal, gateRedRetry:
		return fmt.Errorf("%w: CI did not reach green after force-push on %s", errLandingNeverGreen, pr)
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", g))
	}
}
