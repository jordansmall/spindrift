package settle

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// The ready path end to end — gate, guard, merge, re-wait — kept in one file
// so the common green path (selfHeal → gateToGreen → mergeGuardHit →
// applyMergeMode → mergeImmediate) reads top-to-bottom.

// errAbandoned signals a Terminate (ADR 0024) landed mid-retry: distinct from
// a genuine merge failure so the caller skips the merge-blocked print/comment
// instead of reporting one on an issue Terminate already reclaimed.
var errAbandoned = errors.New("settle: abandoned by terminate")

// errLandingNeverGreen marks a force-pushed head (rebase or conflict-resolve)
// that never reached green. Distinct from a merge failure on an already-green
// PR: there a green PR genuinely exists and the issue stays agent-complete
// (ADR 0012); here there is none, so selfHeal demotes to agent-failed.
var errLandingNeverGreen = errors.New("settle: force-pushed head never went green")

// selfHeal polls the merge gate, dispatching fix boxes (via d) on genuine red
// up to MaxFixAttempts times. agent-complete is swapped only once the landing
// path settles; until then the issue stays agent-in-progress.
//
// Returns landingFailed when CI never reached green (genuine red exhausted, a
// gate timeout, or a force-pushed head that never went green — the issue is
// swapped to failedLabel). Otherwise landingMerged when immediate mode
// completed an actual merge, landingManual for every other green outcome —
// including a merge failure on a still-green PR, which leaves the issue
// agent-complete with a merge-blocked note rather than demoting it.
func (s *Settle) selfHeal(d dispatch.Dispatcher, num string, gen uint64, pr string) (landingResult, string) {
	return s.selfHealGate(d, num, gen, pr, false)
}

// selfHealAdopted is selfHeal's counterpart for a PR discovered independently
// of this process's own push (SettleAdopted's resume/recovery path). It cannot
// assume the PR's head SHA is one this process just pushed, so its first CI
// gate poll requires evidence this run's checks registered before trusting a
// SUCCESS rollup — see gateToGreen.
func (s *Settle) selfHealAdopted(d dispatch.Dispatcher, num string, gen uint64, pr string) (landingResult, string) {
	return s.selfHealGate(d, num, gen, pr, true)
}

// selfHealGate is selfHeal and selfHealAdopted's shared implementation.
// requireRegistration guards only the loop's first attempt — a fix-pass retry
// always follows a push d.Fix just made in this process, so it is never
// ambiguous the way the initial adopted poll can be.
func (s *Settle) selfHealGate(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool) (landingResult, string) {
	if s.pr == nil {
		return s.landPushOnly(num, gen, pr), ""
	}
	for attempt := 0; ; attempt++ {
		obs, gateReason := s.gateToGreen(num, gen, pr, requireRegistration && attempt == 0)
		switch obs.outcome {
		case gateAbandoned:
			return landingAbandoned, ""
		case gateGreen:
			// The launcher, not the Driver, owns the draft->ready flip.
			// MarkReady is idempotent, so it runs unconditionally — ahead of a
			// merge-guard hit or check error below, so a PR downgraded to manual
			// hand-off is mergeable by a human rather than stranded as a draft.
			// Best-effort: a failure logs and never blocks the merge.
			if err := s.pr.MarkReady(pr); err != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-ready-failed  !! %v\n", num, pr, err)
			}
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual, ""
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual, ""
			}
			if err := s.applyMergeMode(num, gen, pr, d); err != nil {
				if errors.Is(err, errAbandoned) {
					return landingAbandoned, ""
				}
				if errors.Is(err, errLandingNeverGreen) {
					fmt.Printf("    #%s  landing=%s  status=landing-failed  !! %v\n", num, pr, err)
					s.it.Comment(num, fmt.Sprintf("landing failed: %v — no green PR exists at the current head", err))
					s.transitionState(num, forge.InProgress, forge.Failed)
					return landingFailed, err.Error()
				}
				fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				s.transitionState(num, forge.InProgress, forge.Complete)
				return landingManual, ""
			}
			// Only now has the landing path settled, so only now may
			// agent-complete claim the agent has nothing left to do.
			s.transitionState(num, forge.InProgress, forge.Complete)
			if s.cfg.MergeMode == "immediate" {
				return landingMerged, ""
			}
			return landingManual, ""
		case gateTerminal:
			fmt.Printf("    #%s  landing=%s  status=gate-terminal  !! %s\n", num, pr, gateReason)
			s.it.Comment(num, fmt.Sprintf("landing failed: %s", gateReason))
			s.transitionState(num, forge.InProgress, forge.Failed)
			return landingFailed, gateReason
		case gateRedRetry:
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					fmt.Printf("    #%s  landing=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
						num, pr, s.cfg.MaxFixAttempts)
				}
				s.transitionState(num, forge.InProgress, forge.Failed)
				return landingFailed, fmt.Sprintf("ci-red: still red after exhausting %d fix pass(es)", s.cfg.MaxFixAttempts)
			}
			// A sibling governor to the attempt-count cap above, so a runaway
			// token/cost run stops even while MaxFixAttempts would allow more
			// passes. Skipped when both knobs are unset so the no-cap path never
			// pays CumulativeUsage's disk-stat-and-parse cost per pass log.
			if s.cfg.MaxBudgetTokens > 0 || s.cfg.MaxBudgetUSD > 0 {
				if exceeded, reason := budgetExceeded(s.cfg, d.CumulativeUsage()); exceeded {
					fmt.Printf("    #%s  landing=%s  status=budget-exhausted  !! %s\n", num, pr, reason)
					s.it.Comment(num, fmt.Sprintf("budget exhausted (%s) — stopping self-heal before another fix pass", reason))
					s.transitionState(num, forge.InProgress, forge.Failed)
					return landingFailed, fmt.Sprintf("budget-exhausted: %s", reason)
				}
			}
			fmt.Printf("    #%s  landing=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
			// Best-effort: a failure to fetch the CI failure detail must
			// never block the fix pass — fall back to an empty summary.
			detail, detailErr := s.pr.FailureDetail(pr)
			if detailErr != nil {
				fmt.Printf("    #%s  landing=%s  status=failure-detail-unavailable  !! %v\n", num, pr, detailErr)
				detail = ""
			}
			// Best-effort: headErr suppresses the no-op check below rather than
			// aborting the fix pass outright.
			headBefore, headErr := s.pr.HeadCommitSHA(pr)
			if headErr != nil {
				fmt.Printf("    #%s  landing=%s  status=head-sha-unavailable  !! %v\n", num, pr, headErr)
			}
			if result := d.Fix(attempt+1, detail); !result.Success {
				fmt.Printf("    #%s  landing=%s  status=fix-failed  !! fix pass %d exited non-zero — aborting self-heal\n", num, pr, attempt+1)
				s.it.Comment(num, fmt.Sprintf("fix pass %d exited non-zero — aborting self-heal", attempt+1))
				s.transitionState(num, forge.InProgress, forge.Failed)
				return landingFailed, fmt.Sprintf("fix-failed: fix pass %d exited non-zero", attempt+1)
			}
			// A read-only Box bundles its fix to the outbox rather than pushing,
			// so HeadCommitSHA reflects no work until this relay lands it. Must
			// run before the no-op check below, which would otherwise misread
			// every read-only fix pass as a no-op and abort on the first
			// attempt. Best-effort.
			if err := s.relayBoxBundle(num); err != nil {
				fmt.Printf("    #%s  landing=%s  status=fix-relay-failed  !! %v\n", num, pr, err)
			}
			// A fix pass that exits zero but never pushes leaves CI's rollup
			// unchanged, so the next gateToGreen poll would read the identical
			// terminal FAILURE and mistake it for a fresh genuine red (issue
			// #1980). Caught here, while the pre-fix SHA is still in hand.
			if headErr == nil {
				if headAfter, err := s.pr.HeadCommitSHA(pr); err == nil && headAfter == headBefore {
					// Confirm before concluding no-op: the forge's API can
					// briefly still serve the pre-push snapshot after a genuine
					// push (replication lag).
					s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
					confirmed, confirmErr := s.pr.HeadCommitSHA(pr)
					if confirmErr == nil && confirmed == headBefore {
						fmt.Printf("    #%s  landing=%s  status=fix-no-op  !! fix pass %d produced no new commit — aborting self-heal\n", num, pr, attempt+1)
						s.it.Comment(num, fmt.Sprintf("fix pass %d produced no new commit — aborting self-heal", attempt+1))
						s.transitionState(num, forge.InProgress, forge.Failed)
						return landingFailed, fmt.Sprintf("fix-no-op: fix pass %d produced no new commit", attempt+1)
					}
				}
			}
		}
	}
}

// landPushOnly is the push-only-forge counterpart to gateToGreen +
// applyMergeMode: with no PR or CI to watch, the issue is marked Complete
// immediately and MERGE_MODE applied straight against the forge's
// Merge/Rebase. A merge failure leaves the issue Complete with a merge-blocked
// note, matching the github adapter's post-green contract (ADR 0012).
func (s *Settle) landPushOnly(num string, gen uint64, branch string) landingResult {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, gen, branch, nil); err != nil {
		fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("landing blocked: %v", err))
		return landingManual
	}
	if s.cfg.MergeMode == "immediate" {
		// CODE_FORGE=local's landing needs the resolved Integration ref +
		// commit sha (ADR 0029/0033), richer than the raw branch name
		// recordLanding wrote from the outcome line — overwrite it now that
		// Merge has landed. Best-effort: a resolution failure must never turn a
		// successful land into a failure.
		if lr, ok := s.cfForNum(num).(forge.LandingRef); ok {
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
// elapse. It performs no label swap itself — gateToGreen also re-runs
// mid-landing (rewaitAfterForcePush), where a swap would be premature.
//
// requireRegistration guards against trusting a rollup this run never watched
// register (issue #1652): an unchanged head SHA can carry a terminal SUCCESS
// inherited from an earlier attempt, so a first-poll SUCCESS is not accepted
// until a non-terminal state (PENDING/EXPECTED/NONE) proves this run's own
// checks are alive on the head commit. It holds only for a bounded
// registrationWindow — a rollup SUCCESS across the whole window with no
// non-terminal state (a PR green before this run started watching) accepts the
// elapsed window as proof rather than waiting forever.
//
// Returns:
//   - gateGreen     — CI confirmed green. reason is "".
//   - gateRedRetry  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box. reason is "".
//   - gateTerminal  — non-retriable outcome (timeout, API error). Caller
//     must swap to failedLabel. reason is a classified, prefixed string —
//     "ci-check-error: ...", the ordinary "ci-timeout: CI-watch deadline
//     reached...", or "ci-timeout: registration guard never cleared..." when
//     the deadline was reached with requireRegistration set and no genuine
//     non-terminal poll ever observed.
//   - gateAbandoned — reason is "".
func (s *Settle) gateToGreen(num string, gen uint64, pr string, requireRegistration bool) (watchObservation, string) {
	deadline := s.cfg.MergePollTimeout
	w := watch{
		pollInterval:        s.cfg.MergePollInterval,
		deadline:            deadline,
		requireRegistration: requireRegistration,
		clock:               s.clock,
	}
	obs := w.poll(
		func() bool { return s.terminated(num, gen) },
		func() (forge.RollupState, error) { return s.pr.CheckState(pr) },
	)

	switch obs.outcome {
	case gateGreen, gateRedRetry, gateAbandoned:
		return obs, ""
	case gateTerminal:
		// fall through to reason formatting below.
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", obs.outcome))
	}

	// gateTerminal: format the operator-facing reason, logging the
	// check-state-error status line poll() has no I/O to print itself.
	if obs.err != nil {
		fmt.Printf("    #%s  landing=%s  status=check-state-error  !! %v\n", num, pr, obs.err)
		return obs, gateTerminalReason(obs.err, deadline)
	}
	if requireRegistration && !obs.sawNonTerminal {
		// Deadline reached with the guard unsatisfied by any genuine evidence —
		// only the registrationWindow's elapsed-fallback ever set registered.
		// Named explicitly rather than folded into the generic ci-timeout.
		return obs, gateTerminalReasonRegistration(deadline)
	}
	return obs, gateTerminalReason(nil, deadline)
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
			// Audited: execClient.EnqueueAutoMerge runs gh with no
			// stdout/stderr capture, so err is only ever *exec.ExitError, a
			// start failure, or the wrapped message embedding the (already
			// public) prURL — never gh's stderr text. Safe to surface verbatim
			// in the issue comment below.
			fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		// CODE_FORGE=local requires MERGE_MODE=immediate (validated at launcher
		// startup), so a local seam's forge.BundleRelay hook can never reach
		// manual mode — every Code Forge with no PR support relays via
		// mergeImmediate.
		fmt.Printf("    #%s  landing=%s  status=agent-complete  merge-mode=%s\n", num, pr, s.cfg.MergeMode)
		return nil
	default:
		return fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// mergeImmediate attempts to merge the green PR with rebase retry on conflict.
//
// A successful conflict-resolve already rebased and force-pushed the branch,
// so the next Merge conflict is retried directly (after a brief settle wait
// for the forge's mergeability snapshot to catch up) instead of invoking
// Rebase a second time.
//
// The termination check ahead of preflightStaleBase deliberately duplicates
// the loop's own first-iteration check: preflightStaleBase itself force-pushes,
// so a terminated issue must never reach it, not just never reach Merge.
func (s *Settle) mergeImmediate(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	rebaseAttempts := 0
	pushRetries := 0
	checksBlockedAttempts := 0
	mergeTransientAttempts := 0
	skipRebase := false
	if s.terminated(num, gen) {
		return errAbandoned
	}
	// preflightStaleBase keeps its own attempt budget rather than sharing
	// rebaseAttempts/pushRetries: a stale-base rebase and a conflict-triggered
	// rebase are independent concerns, and sharing would let one exhaust the
	// other's allowance before it ever gets to run.
	if err := s.preflightStaleBase(num, gen, pr, d); err != nil {
		return err
	}
	// cf is num's own parent-keyed instance (CODE_FORGE=local) when
	// Config.CodeForgeForIssue is set, otherwise New's cf unchanged.
	cf := s.cfForNum(num)
	// CODE_FORGE=local's Merge assumes ref already exists as a branch on the
	// backing repo, but the Box's read-only repo mount means it never pushed
	// there. Relay its code-out bundle in first, once, so the Merge(pr)
	// attempts below find the ref (ADR 0033). A relay failure is returned
	// directly: unlike a merge conflict, there is nothing to retry. Guarded on
	// the push-only path (s.pr == nil) because only there is pr a ref/branch
	// name, the value RelayBundle expects; a PR-shaped read-only forge is
	// already relayed by hostMediateDraftPR before its draft PR exists.
	//
	// pr is overwritten with cf.AgentBranch(num) rather than trusted as passed
	// in: it traces back to the outcome line's landing= field, Agent-controlled
	// input. Deriving it host-side once, before either RelayBundle or the Merge
	// loop sees it, pins both to the one ref this hand-off is meant to use.
	if br, ok := cf.(forge.BundleRelay); ok && s.pr == nil {
		if s.cfg.OutboxDir == nil {
			return fmt.Errorf("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay — every CODE_FORGE=local construction site must supply an OutboxDir resolver")
		}
		pr = cf.AgentBranch(num)
		if err := br.RelayBundle(s.cfg.OutboxDir(num), pr); err != nil {
			return err
		}
	}
	for {
		if s.terminated(num, gen) {
			return errAbandoned
		}
		err := cf.Merge(pr)
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
			s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if errors.Is(err, forge.ErrMergeTransient) {
			if mergeTransientAttempts >= s.cfg.Policy.Max {
				return err
			}
			mergeTransientAttempts++
			fmt.Printf("    #%s  landing=%s  status=merge-transient-retry  attempt=%d/%d  !! %v\n",
				num, pr, mergeTransientAttempts, s.cfg.Policy.Max, err)
			s.rebasePushBackoff().Do(mergeTransientAttempts)
			continue
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			// Stale-mergeability-snapshot retry: conflict-resolve already ran
			// and restored ready, so this ErrMergeConflict is the same resolved
			// conflict, not a new one -- must not re-demote, or the Merge retry
			// below would run against a draft PR.
			skipRebase = false
			fmt.Printf("    #%s  landing=%s  status=merge-retry-settle\n", num, pr)
			s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		// A genuine conflict: demote to draft as a visible signal the PR isn't
		// mergeable, ahead of the rebase/conflict-resolve cycle below.
		// Best-effort; nil-guarded since landPushOnly reaches mergeImmediate
		// too, with s.pr unset.
		if s.pr != nil {
			if mdErr := s.pr.MarkDraft(pr); mdErr != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-draft-failed  !! %v\n", num, pr, mdErr)
			}
		}
		if rebaseAttempts >= s.cfg.MaxRebaseAttempts {
			return err
		}
		rebaseAttempts++
		fmt.Printf("    #%s  landing=%s  status=rebase-retry  attempt=%d/%d\n",
			num, pr, rebaseAttempts, s.cfg.MaxRebaseAttempts)
		rbErr := cf.Rebase(pr)
		for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts {
			pushRetries++
			fmt.Printf("    #%s  landing=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
				num, pr, pushRetries, s.cfg.MaxRebaseAttempts, rbErr)
			s.rebasePushBackoff().Do(pushRetries)
			rbErr = cf.Rebase(pr)
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

// rebasePushBackoff builds the linear backoff both rebase-push retry loops
// share, so the two call sites cannot drift apart.
func (s *Settle) rebasePushBackoff() retry.LinearBackoff {
	return retry.LinearBackoff{
		Unit:   s.cfg.Policy.Unit,
		Jitter: s.cfg.Policy.Jitter,
		Clock:  s.clock,
	}
}

// preflightStaleBase proactively rebases pr when the forge reports its
// branch is behind its base (NeedsUpdate — issue #936) — even though the PR
// shows no textual conflict and CI is already green on its current head. A
// green PR can still be stale: main may have advanced past a just-merged
// sibling whose changes the PR's tested tree never saw.
//
// It is opt-in via PreflightStaleBase (ADR 0028): off by default it returns
// without even querying NeedsUpdate, so the near-constant "behind main because
// a sibling landed first" case costs no compare-API round-trip and no extra
// rebase+CI cycle. Turn it on to restore ADR 0026's behavior, where a stale
// base is a conflict requiring rebase-and-re-green before merge.
//
// A NeedsUpdate query error is logged and swallowed: staleness is merely
// unknown, and the caller's normal Merge attempt surfaces the same underlying
// problem through its own error handling.
//
// A Rebase failure is different: staleness is confirmed and the corrective
// action itself failed. A genuine ErrMergeConflict falls through to the
// reactive loop's ResolveConflict dispatch when a Dispatcher is in scope; any
// other Rebase error, a conflict with no Dispatcher, or a rewaitAfterForcePush
// failure is hard and merge-blocking, rather than a fall-through to Merge on a
// base known stale and never re-validated.
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
	cf := s.cfForNum(num)
	rbErr := cf.Rebase(pr)
	for pushRetries := 0; rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts; pushRetries++ {
		fmt.Printf("    #%s  landing=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
			num, pr, pushRetries+1, s.cfg.MaxRebaseAttempts, rbErr)
		s.rebasePushBackoff().Do(pushRetries + 1)
		rbErr = cf.Rebase(pr)
	}
	if rbErr != nil {
		isConflict := errors.Is(rbErr, forge.ErrMergeConflict)
		if isConflict && s.pr != nil {
			// Demote to draft as the reactive loop does, regardless of whether
			// a Dispatcher is available to attempt resolution. s.pr is already
			// non-nil per this function's early return; re-checked so the call
			// stays locally correct if that guard ever moves.
			if mdErr := s.pr.MarkDraft(pr); mdErr != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-draft-failed  !! %v\n", num, pr, mdErr)
			}
		}
		if isConflict && d != nil {
			if crErr := s.resolveConflict(num, pr, d); crErr != nil {
				return crErr
			}
			// No skipRebase equivalent needed (contrast the reactive loop's
			// post-resolve skipRebase=true): the caller's loop hasn't started,
			// so its first Merge attempt runs fresh once rewaitAfterForcePush
			// confirms the resolved head is green.
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
		// Audited: both the OCI and bwrap runner adapters wire the Box's
		// stdout/stderr to the log file, not to the returned error, so crErr is
		// only ever *exec.ExitError or a start failure — never Box-internal
		// output. Safe to surface verbatim in selfHeal's issue comment.
		fmt.Printf("    #%s  landing=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
		return fmt.Errorf("%w: conflict-resolve dispatch failed: %v", errLandingNeverGreen, crErr)
	}
	// A read-only Box bundles the resolved branch to the outbox rather than
	// force-pushing it, and nothing else ever relays this bundle in — without
	// this the caller's rewaitAfterForcePush would poll CI on the
	// still-conflicted pre-resolve head forever.
	if err := s.relayBoxBundle(num); err != nil {
		return fmt.Errorf("%w: relay after conflict-resolve failed: %v", errLandingNeverGreen, err)
	}
	return nil
}

// relayBoxBundle relays num's outbox bundle in via the resolved Code Forge's
// optional forge.BundleRelay hook, for any caller whose Box may have bundled
// instead of pushed directly. A read-write Code Forge never implements
// forge.BundleRelay, so this is a no-op there — its Box already pushed.
func (s *Settle) relayBoxBundle(num string) error {
	cf := s.cfForNum(num)
	br, ok := cf.(forge.BundleRelay)
	if !ok {
		return nil
	}
	if s.cfg.OutboxDir == nil {
		return fmt.Errorf("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay")
	}
	return br.RelayBundle(s.cfg.OutboxDir(num), cf.AgentBranch(num))
}

// rewaitAfterForcePush blocks for CI to reach green on the PR's current head
// after a gate-driven force-push reset its required checks. A wait that ends in
// genuine CI failure or a timeout returns an error distinct from
// forge.ErrMergeConflict, so the caller's conflict-retry path is never
// re-entered for it.
//
// A no-op for a push-only forge (s.pr == nil): there is no CI to wait for, so
// a successful force-push is enough. Without this guard the reactive loop's
// rebase-succeeded branch would crash on s.pr.CheckState — routine for
// CODE_FORGE=local, where concurrent seams land onto one Integration branch.
func (s *Settle) rewaitAfterForcePush(num string, gen uint64, pr string) error {
	if s.pr == nil {
		return nil
	}
	fmt.Printf("    #%s  landing=%s  status=post-force-push-wait\n", num, pr)
	obs, gReason := s.gateToGreen(num, gen, pr, false)
	if obs.outcome == gateGreen {
		// Restore ready. Most rewaits follow a conflict demote; the stale-base
		// clean-rebase path never demoted, but MarkReady is idempotent, so
		// calling it unconditionally beats threading a was-it-demoted flag
		// through. Best-effort.
		if mrErr := s.pr.MarkReady(pr); mrErr != nil {
			fmt.Printf("    #%s  landing=%s  status=mark-ready-failed  !! %v\n", num, pr, mrErr)
		}
	}
	return rewaitGateResultErr(obs.outcome, gReason, pr)
}

// rewaitGateResultErr maps a gateToGreen outcome to rewaitAfterForcePush's
// return value. gateTerminal and gateRedRetry are named explicitly rather than
// folded into a catch-all default, so a future gateResult variant must be
// handled here too — loudly (panic) instead of silently landing on "never
// green".
func rewaitGateResultErr(g gateResult, reason, pr string) error {
	switch g {
	case gateGreen:
		return nil
	case gateAbandoned:
		return errAbandoned
	case gateTerminal, gateRedRetry:
		if reason != "" {
			return fmt.Errorf("%w: CI did not reach green after force-push on %s (%s)", errLandingNeverGreen, pr, reason)
		}
		return fmt.Errorf("%w: CI did not reach green after force-push on %s", errLandingNeverGreen, pr)
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", g))
	}
}
