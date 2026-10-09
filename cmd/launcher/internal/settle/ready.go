package settle

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
)

// errAbandoned marks a Terminate (ADR 0024, issue #649) that landed mid-retry,
// or a conflict-resolve Box skipped because the issue's own Box is already in
// flight (issue #3655), so the caller skips the merge-blocked print and comment
// on an issue whose state another actor owns.
var errAbandoned = errors.New("settle: abandoned (terminated, or a live run owns the issue)")

// errLandingNeverGreen marks a force-pushed head that never reached green. A
// merge failure on an already-green PR leaves the issue agent-complete (ADR
// 0012); here no green PR exists at the current head, so selfHeal demotes to
// agent-failed instead (issue #758).
var errLandingNeverGreen = errors.New("settle: force-pushed head never went green")

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// MaxFixAttempts times, then applies the merge mode. It swaps agent-complete
// only once the landing path settles (issue #757). A merge failure on a
// still-green PR stays agent-complete (ADR 0012), but a force-pushed head that
// never re-confirms green demotes to agent-failed instead (issue #758).
func (s *Settle) selfHeal(d dispatch.Dispatcher, num string, gen uint64, pr string) (landingResult, string) {
	return s.selfHealGate(d, num, gen, pr, false)
}

// selfHealAdopted is selfHeal's counterpart for a PR discovered independently
// of this process's own push (SettleAdopted's resume path). It cannot assume
// the head SHA is one this process just pushed, so its first gate poll requires
// evidence this run's checks registered before trusting a SUCCESS rollup,
// within a bounded window (issues #1652, #2475).
func (s *Settle) selfHealAdopted(d dispatch.Dispatcher, num string, gen uint64, pr string) (landingResult, string) {
	return s.selfHealGate(d, num, gen, pr, true)
}

// selfHealGate is selfHeal and selfHealAdopted's shared implementation.
// requireRegistration guards only the loop's first attempt, because a fix-pass
// retry always follows a push d.Fix just made in this process.
func (s *Settle) selfHealGate(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool) (landingResult, string) {
	if s.pr == nil {
		return s.landPushOnly(num, gen, pr), ""
	}
	for attempt := 0; ; attempt++ {
		obs, gateReason := s.gateToGreen(num, gen, pr, requireRegistration && attempt == 0)
		// A mark (Terminate or Reclaim) already released num back to
		// Dispatchable by the time any check below sees it, so no arm past
		// that point may commit tracker state or dispatch further work for
		// num (issue #3523).
		switch obs.outcome {
		case gateAbandoned:
			return landingAbandoned, ""
		case gateGreen:
			// Catches a mark landing during the gate's own confirm sleep,
			// before MarkReady's idempotent ready flip below.
			if s.terminated(num, gen) {
				return landingAbandoned, ""
			}
			// The launcher, never the Driver, owns the draft to ready flip at
			// green, inverting the old draft-until-ready invariant (issues
			// #1651, #1653, #1654, #1614, #1625). MarkReady is idempotent, so
			// it runs before the guard and merge-mode checks below: a PR
			// downgraded to manual must not stay stranded as a draft.
			if err := s.pr.MarkReady(pr); err != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-ready-failed  !! %v\n", num, pr, err)
			}
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				return s.completeLanding(num, gen, landingManual, ReasonMergeGuardCheckError), ""
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  landing=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				return s.completeLanding(num, gen, landingManual, ReasonMergeGuardHit), ""
			}
			mergeReason, err := s.applyMergeMode(num, gen, pr, d)
			if err != nil {
				if errors.Is(err, errAbandoned) {
					return landingAbandoned, ""
				}
				if errors.Is(err, errLandingNeverGreen) {
					fmt.Printf("    #%s  landing=%s  status=landing-failed  !! %v\n", num, pr, err)
					s.it.Comment(num, fmt.Sprintf("landing failed: %v — no green PR exists at the current head", err))
					s.transitionState(num, forge.InProgress, forge.Failed, err.Error(), ReasonLandingFailed)
					return landingFailed, err.Error()
				}
				fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				return s.completeLanding(num, gen, landingManual, ReasonMergeBlocked), ""
			}
			return s.completeLanding(num, gen, mergedLanding(mergeReason), mergeReason), ""
		case gateTerminal:
			// Catches a mark landing mid-poll, before the Failed commit and
			// comment below.
			if s.terminated(num, gen) {
				return landingAbandoned, ""
			}
			fmt.Printf("    #%s  landing=%s  status=gate-terminal  !! %s\n", num, pr, gateReason)
			s.it.Comment(num, fmt.Sprintf("landing failed: %s", gateReason))
			s.transitionState(num, forge.InProgress, forge.Failed, gateReason, ReasonGateTerminal)
			return landingFailed, gateReason
		case gateRedRetry:
			// Catches a mark landing mid-poll, before the fix-exhausted and
			// budget-exhausted Failed commits below.
			if s.terminated(num, gen) {
				return landingAbandoned, ""
			}
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					fmt.Printf("    #%s  landing=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
						num, pr, s.cfg.MaxFixAttempts)
				}
				reason := fmt.Sprintf("ci-red: still red after exhausting %d fix pass(es)", s.cfg.MaxFixAttempts)
				// With no fix pass allowed none ran, so the note's own ci-red is the
				// accurate class, not fix-exhausted.
				exhaustedReason := ReasonCIRed
				if s.cfg.MaxFixAttempts > 0 {
					exhaustedReason = ReasonFixExhausted
				}
				if s.cfg.Unclaimed {
					// No label records this failure (transitionState is a
					// no-op for an unclaimed issue, issue #4076), unlike the
					// gateTerminal arm above which already comments.
					if commentErr := s.it.Comment(num, fmt.Sprintf("landing failed: %s", reason)); commentErr != nil {
						fmt.Fprintf(os.Stderr, "    ?? #%s: post unclaimed-failure comment: %v\n", num, commentErr)
					}
				}
				s.transitionState(num, forge.InProgress, forge.Failed, reason, exhaustedReason)
				return landingFailed, reason
			}
			// The budget caps (issue #2001) stop a runaway token or cost run
			// even while MaxFixAttempts would still allow more passes. Both
			// knobs unset skips the check, so the no-cap path never pays
			// CumulativeUsage's disk stat and parse over every pass log.
			if s.cfg.MaxBudgetTokens > 0 || s.cfg.MaxBudgetUSD > 0 {
				if exceeded, reason := budgetExceeded(s.cfg, d.CumulativeUsage()); exceeded {
					fmt.Printf("    #%s  landing=%s  status=budget-exhausted  !! %s\n", num, pr, reason)
					s.it.Comment(num, fmt.Sprintf("budget exhausted (%s) — stopping self-heal before another fix pass", reason))
					note := fmt.Sprintf("budget-exhausted: %s", reason)
					s.transitionState(num, forge.InProgress, forge.Failed, note, ReasonBudgetExhausted)
					return landingFailed, note
				}
			}
			fmt.Printf("    #%s  landing=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
			// Best-effort: a failure to fetch the CI failure detail must never
			// block the fix pass, so the summary falls back to empty.
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
			disp := d.Fix(attempt+1, detail)
			// Catches d.Fix's own exit — a non-zero result can be Reclaim's
			// SIGKILL — before the Failed commit or bundle relay below.
			if s.terminated(num, gen) {
				return landingAbandoned, ""
			}
			type fixStop struct {
				landing landingResult
				note    string
				stop    bool
			}
			stopped := dispatch.Route(disp,
				func() fixStop {
					// No Box started: a live run owns this issue's in-progress claim,
					// so leave its state alone and do not retry (issue #3655).
					fmt.Printf("    #%s  landing=%s  status=already-in-flight  ~~ fix pass %d skipped; live run continues\n", num, pr, attempt+1)
					return fixStop{landing: landingAbandoned, stop: true}
				},
				func(result dispatch.Result) fixStop {
					RecordSettleWarnings(d, num, fmt.Sprintf("fix pass %d: ", attempt+1), result)
					fmt.Printf("    #%s  landing=%s  status=fix-failed  !! fix pass %d exited non-zero — aborting self-heal\n", num, pr, attempt+1)
					result.ReportFailureReason(num)
					s.it.Comment(num, fmt.Sprintf("fix pass %d exited non-zero — aborting self-heal", attempt+1))
					note := fmt.Sprintf("fix-failed: fix pass %d exited non-zero", attempt+1)
					s.transitionState(num, forge.InProgress, forge.Failed, note, ReasonFixFailed)
					return fixStop{landing: landingFailed, note: note, stop: true}
				},
				func(result dispatch.Result) fixStop {
					RecordSettleWarnings(d, num, fmt.Sprintf("fix pass %d: ", attempt+1), result)
					return fixStop{}
				})
			if stopped.stop {
				return stopped.landing, stopped.note
			}
			// A read-only Box holds no push-capable token (issue #1979): its fix
			// agent bundled its work to the outbox, so HeadCommitSHA reflects no
			// work until this relay lands it. It must run before the no-op check
			// below, which would otherwise misread every read-only fix pass as a
			// no-op. Best-effort: a failure here only logs.
			if _, err := s.relayBoxBundle(num, gen); err != nil {
				// A stop mid-backoff already released num (issue #3523); falling
				// through would log a failure and sleep the no-op confirm poll.
				if errors.Is(err, errAbandoned) {
					return landingAbandoned, ""
				}
				fmt.Printf("    #%s  landing=%s  status=fix-relay-failed  !! %v\n", num, pr, err)
			}
			// A fix pass that exits zero but pushes no new commit leaves CI's
			// rollup unchanged, so the next gateToGreen poll would read the
			// identical terminal FAILURE as a fresh genuine red (issue #1980).
			if headErr == nil {
				if headAfter, err := s.pr.HeadCommitSHA(pr); err == nil && headAfter == headBefore {
					// Confirm before concluding no-op: the forge's API can still
					// serve the pre-push snapshot right after a genuine push.
					s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
					confirmed, confirmErr := s.pr.HeadCommitSHA(pr)
					if confirmErr == nil && confirmed == headBefore {
						// Catches a mark landing during the confirm sleep,
						// before the fix-no-op Failed commit below.
						if s.terminated(num, gen) {
							return landingAbandoned, ""
						}
						fmt.Printf("    #%s  landing=%s  status=fix-no-op  !! fix pass %d produced no new commit — aborting self-heal\n", num, pr, attempt+1)
						s.it.Comment(num, fmt.Sprintf("fix pass %d produced no new commit — aborting self-heal", attempt+1))
						note := fmt.Sprintf("fix-no-op: fix pass %d produced no new commit", attempt+1)
						s.transitionState(num, forge.InProgress, forge.Failed, note, ReasonFixNoOp)
						return landingFailed, note
					}
				}
			}
		}
	}
}

// completeLanding is the single Complete-commit gate for the gateGreen arm and
// landPushOnly. The arm's top-of-switch check catches a mark that lands before
// MarkReady, but the Registry mark is sticky (terminate/registry.go), so
// re-checking here also catches one that lands during MarkReady's or
// applyMergeMode's own round-trip: Reclaim already moved num to Dispatchable
// by then, and a Complete commit here would leave the issue wearing
// agent-complete on top, which Reconcile's InProgress-only sweep never clears
// (issue #3523).
func (s *Settle) completeLanding(num string, gen uint64, landed landingResult, reason string) landingResult {
	if s.terminated(num, gen) {
		return landingAbandoned
	}
	// An unclaimed issue (issue #4076) only ever reaches Complete on an actual
	// merge; manual/auto/guard-hit/merge-blocked leave it untouched since
	// there was never an InProgress state to move out of.
	if s.cfg.Unclaimed && landed != landingMerged {
		return landed
	}
	s.transitionState(num, forge.InProgress, forge.Complete, "", reason)
	return landed
}

// mergedLanding classifies an applyMergeMode reason: only ReasonMerged is a
// merge that actually landed.
func mergedLanding(reason string) landingResult {
	if reason == ReasonMerged {
		return landingMerged
	}
	return landingManual
}

// landPushOnly lands a push-only forge, where there is no PR or CI to watch, so
// the issue goes Complete once MERGE_MODE has applied straight against the
// forge's Merge and Rebase. Complete commits after the merge, through
// completeLanding, so a mark landing during the merge's retry backoff abandons
// instead of committing tracker state (issue #4879). A merge failure leaves
// the issue Complete with a merge-blocked note (unless Unclaimed, which has no
// state to move), never demoted to Failed (ADR 0012). A relay that fails with
// its bundle still in the outbox parks the issue Failed instead (issue #4651)
// and returns landingFailed, the transition and comment already done. An
// immediate merge drops the landed bundle here, since verifyMerged never runs
// without a PR.
func (s *Settle) landPushOnly(num string, gen uint64, branch string) landingResult {
	// No CI watch here, so this is the only checkpoint before landing —
	// an aborted run must not merge or commit Complete (issue #3523).
	if s.terminated(num, gen) {
		return landingAbandoned
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
	}
	if errors.Is(relayErr, errAbandoned) {
		return landingAbandoned
	}
	if relayFailureParkable(relayErr) {
		s.parkRelayFailure(num, branch, relayErr)
		return landingFailed
	}
	err := relayErr
	mergeReason := ReasonMergeBlocked
	if err == nil {
		mergeReason, err = s.applyMergeMode(num, gen, branch, nil)
	}
	if errors.Is(err, errAbandoned) {
		return landingAbandoned
	}
	if err != nil {
		if s.completeLanding(num, gen, landingManual, ReasonMergeBlocked) == landingAbandoned {
			return landingAbandoned
		}
		fmt.Printf("    #%s  landing=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("landing blocked: %v", err))
		return landingManual
	}
	landed := mergedLanding(mergeReason)
	if landed != landingMerged {
		return s.completeLanding(num, gen, landed, mergeReason)
	}
	// Ahead of the re-check: the merge happened and the outbox bundle is
	// not tracker state, so an abandoned landing must still drop it.
	s.removeLandedBundle(num)
	if s.completeLanding(num, gen, landed, mergeReason) == landingAbandoned {
		return landingAbandoned
	}
	// CODE_FORGE=local needs the resolved Integration ref and commit sha
	// (ADR 0029/0033), not the raw branch name recordLanding wrote from the
	// outcome line, so overwrite it now that Merge has landed. Best-effort:
	// a resolution failure must never turn a successful land into a failure.
	if lr, ok := s.cfForNum(num).(forge.LandingRef); ok {
		if landing, err := lr.LandingRef(); err == nil {
			s.recordLanding(num, landing)
		} else {
			fmt.Printf("    #%s  landing=%s  status=landing-ref-unresolved  !! %v\n", num, branch, err)
		}
	}
	return landed
}

// gateToGreen polls CheckState until confirmed SUCCESS, terminal failure, or
// MergePollTimeout, swapping no labels: it re-runs mid-landing, so the caller
// owns agent-complete (issue #757). requireRegistration refuses a first-poll
// SUCCESS inherited from an earlier attempt until a non-terminal state proves
// this run's checks are alive (#1652), bounded by registrationWindow (#2475).
func (s *Settle) gateToGreen(num string, gen uint64, pr string, requireRegistration bool) (watchObservation, string) {
	key := s.keyFor(num)
	report.CIWait(key, pr, "")
	deadline := s.cfg.MergePollTimeout
	w := watch{
		pollInterval:        s.cfg.MergePollInterval,
		deadline:            deadline,
		requireRegistration: requireRegistration,
		clock:               s.clock,
	}
	checkState := func() (forge.RollupState, error) { return s.pr.CheckState(pr) }
	if rr := s.ciRun; rr != nil {
		// Checks may not exist when the wait starts, so announce the run URL
		// when it first appears, changes or disappears, not on every tick.
		var announced string
		checkState = func() (forge.RollupState, error) {
			state, run, err := rr.CheckRun(pr)
			if err == nil && run != announced {
				announced = run
				report.CIWait(key, pr, run)
			}
			return state, err
		}
	}
	obs := w.poll(
		func() bool { return s.terminated(num, gen) },
		checkState,
	)

	switch obs.outcome {
	case gateGreen, gateRedRetry, gateAbandoned:
		return obs, ""
	case gateTerminal:
		// fall through to reason formatting below.
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", obs.outcome))
	}

	// poll() does no I/O of its own, so this block prints the check-state-error
	// status line as well as formatting the operator-facing reason.
	if obs.err != nil {
		fmt.Printf("    #%s  landing=%s  status=check-state-error  !! %v\n", num, pr, obs.err)
		return obs, gateTerminalReason(obs.err, deadline)
	}
	if requireRegistration && !obs.sawNonTerminal {
		// The deadline passed with no genuine evidence for the
		// requireRegistration guard, only registrationWindow's elapsed
		// fallback, so name that reason separately from the generic
		// ci-timeout (issue #2476).
		return obs, gateTerminalReasonRegistration(deadline)
	}
	return obs, gateTerminalReason(nil, deadline)
}

// mergeGuardHit returns the PR's changed files that hit a MergeGuardPaths glob.
// A nil, nil result means the guard is disabled or matched nothing; an error
// means the changed-file list could not be read at all.
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

// applyMergeMode performs the mode-specific action once a landing is ready to
// merge: CI green on a PR, or straight away on a push-only forge. It runs
// before the Complete commit, which both callers make through completeLanding
// after it returns; see completeLanding for why that order matters (issues
// #3523, #4879) and for the Unclaimed case. A returned merge failure settles
// Complete with ReasonMergeBlocked (ADR 0012), except errAbandoned, which
// commits nothing, and selfHealGate's errLandingNeverGreen, which settles
// Failed. A nil d makes a rebase conflict immediately non-retriable, since
// nothing can dispatch a conflict resolution. On a nil error the returned
// string is the reason class of the landing, decided here so the mode switch
// is the only one.
func (s *Settle) applyMergeMode(num string, gen uint64, pr string, d dispatch.Dispatcher) (string, error) {
	switch s.cfg.MergeMode {
	case "immediate":
		if err := s.mergeImmediate(num, gen, pr, d); err != nil {
			return "", err
		}
		return ReasonMerged, nil
	case "auto":
		if s.pr == nil {
			return "", fmt.Errorf("MERGE_MODE=auto requires a Code Forge with PR support (got a push-only forge)")
		}
		if err := s.pr.EnqueueAutoMerge(pr); err != nil {
			// Audited (issues #1233, #831): execClient.EnqueueAutoMerge captures
			// no stdout or stderr, so err is only ever an *exec.ExitError, a
			// start failure, or a message embedding the already-public prURL,
			// never gh's stderr. Safe to surface verbatim in the comment below.
			fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return ReasonMergeBlocked, nil
		}
		fmt.Printf("    #%s  landing=%s  status=auto-merge-enqueued\n", num, pr)
		return ReasonAutoMergeEnqueued, nil
	case "manual":
		// CODE_FORGE=local requires MERGE_MODE=immediate (validated at launcher
		// startup, issue #1725), so a forge.BundleRelay hook never reaches
		// manual mode here.
		fmt.Printf("    #%s  landing=%s  status=agent-complete  merge-mode=%s\n", num, pr, s.cfg.MergeMode)
		return ReasonManual, nil
	default:
		return "", fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// mergeImmediate merges the green PR, rebasing on conflict. A successful
// conflict-resolve already rebased and force-pushed, so the next conflict is
// retried directly after a settle wait rather than rebased a second time. The
// termination check ahead of preflightStaleBase duplicates the loop's own first
// iteration deliberately, because preflightStaleBase force-pushes (issue #943).
func (s *Settle) mergeImmediate(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	rebaseAttempts := 0
	pushRetries := 0
	checksBlockedAttempts := 0
	mergeTransientAttempts := 0
	skipRebase := false
	if s.terminated(num, gen) {
		return errAbandoned
	}
	// preflightStaleBase keeps its own retry budget: sharing rebaseAttempts with
	// the conflict loop below would let one path exhaust the other's allowance.
	if err := s.preflightStaleBase(num, gen, pr, d); err != nil {
		return err
	}
	// cf is num's own parent-keyed instance when Config.CodeForgeForIssue is set
	// (CODE_FORGE=local, issue #1734), otherwise New's cf unchanged.
	cf := s.cfForNum(num)
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
			s.transientBackoff().Do(mergeTransientAttempts)
			continue
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			// The conflict-resolve dispatch already ran and restored ready, so
			// this ErrMergeConflict is the same resolved conflict read from a
			// stale mergeability snapshot. Re-demoting would leave the Merge
			// retry below attempting a draft PR (issue #1863).
			skipRebase = false
			fmt.Printf("    #%s  landing=%s  status=merge-retry-settle\n", num, pr)
			s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		// A genuine conflict: demote to draft (issue #1863) as a visible signal
		// the PR is not currently mergeable. Best-effort; nil-guarded because
		// landPushOnly reaches mergeImmediate with s.pr unset.
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
			s.transientBackoff().Do(pushRetries)
			rbErr = cf.Rebase(pr)
		}
		if rbErr != nil {
			if errors.Is(rbErr, forge.ErrTransientPushFailure) {
				fmt.Printf("    #%s  landing=%s  status=rebase-push-retries-exhausted  attempts=%d  !! %v\n",
					num, pr, pushRetries, rbErr)
				return rbErr
			}
			if errors.Is(rbErr, forge.ErrMergeConflict) && d != nil {
				if crErr := s.resolveConflict(num, gen, pr, d); crErr != nil {
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
		// The rebase force-push reset the PR's required checks, so the next merge
		// attempt must wait for the new head to go green.
		if rwErr := s.rewaitAfterForcePush(num, gen, pr); rwErr != nil {
			return rwErr
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

// preflightStaleBase rebases pr when the forge reports its branch is behind the
// base (issue #936): a green PR can still be stale, never having tested a sibling
// that landed first. Opt-in via PreflightStaleBase (ADR 0028). A NeedsUpdate
// error is swallowed, since the caller's Merge surfaces the same problem, but a
// Rebase failure is hard (issue #940): staleness is confirmed, the fix failed.
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
		s.transientBackoff().Do(pushRetries + 1)
		rbErr = cf.Rebase(pr)
	}
	if rbErr != nil {
		isConflict := errors.Is(rbErr, forge.ErrMergeConflict)
		if isConflict && s.pr != nil {
			// A genuine conflict: demote to draft (issue #1863) whether or not a
			// Dispatcher can resolve it. The early return above already
			// guarantees s.pr is non-nil; the check repeats so this stays
			// correct if that guard ever moves.
			if mdErr := s.pr.MarkDraft(pr); mdErr != nil {
				fmt.Printf("    #%s  landing=%s  status=mark-draft-failed  !! %v\n", num, pr, mdErr)
			}
		}
		if isConflict && d != nil {
			if crErr := s.resolveConflict(num, gen, pr, d); crErr != nil {
				return crErr
			}
			// The ResolveConflict dispatch above is shared with the reactive loop
			// (issue #1319), but no skipRebase is needed here: the caller's loop
			// has not started, so its first Merge runs fresh once
			// rewaitAfterForcePush confirms the resolved head is green.
			return s.rewaitAfterForcePush(num, gen, pr)
		}
		fmt.Printf("    #%s  landing=%s  status=stale-base-rebase-failed  !! %v\n", num, pr, rbErr)
		return rbErr
	}
	return s.rewaitAfterForcePush(num, gen, pr)
}

// resolveConflict dispatches a Box to resolve a genuine ErrMergeConflict hit by
// a force-pushing rebase. It returns errAbandoned when Reclaim reaps the Box
// mid-dispatch, since its SIGKILL can surface as crErr indistinguishably from
// a genuine dispatch failure (issue #3523), or when the issue's own Box is
// already running (issue #3655).
func (s *Settle) resolveConflict(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	fmt.Printf("    #%s  landing=%s  status=conflict-resolve\n", num, pr)
	crErr := d.ResolveConflict(pr)
	// Reclaim reaps this Box under num's own box name, so crErr can be its own
	// SIGKILL, and the relay below would push a resolved-conflict bundle for
	// an issue Terminate already released back to Dispatchable (issue #3523).
	// This check runs ahead of the crErr handling below, so a genuine
	// conflict-resolve failure racing a mark loses its
	// status=conflict-resolve-failed log line — deliberate, since num is
	// already released and nothing reads that log line for it.
	if s.terminated(num, gen) {
		return errAbandoned
	}
	// The issue's own Box is live and owns its state, so this is no failed
	// resolve (issue #3655).
	if errors.Is(crErr, runner.ErrAlreadyRunning) {
		fmt.Printf("    #%s  landing=%s  status=already-in-flight  ~~ conflict-resolve skipped; live run continues\n", num, pr)
		return errAbandoned
	}
	if crErr != nil {
		// Audited (issue #831): the OCI and bwrap adapters both wire the Box's
		// stdout and stderr to the log file, not to the returned error, so crErr
		// is only ever an *exec.ExitError or a start failure, never Box-internal
		// output. Safe to surface verbatim in selfHeal's issue comment.
		fmt.Printf("    #%s  landing=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
		return fmt.Errorf("%w: conflict-resolve dispatch failed: %v", errLandingNeverGreen, crErr)
	}
	// A read-only Box holds no push-capable token (issue #1979): it bundled the
	// resolved branch to the outbox, and nothing else relays that bundle in, so
	// without this the caller's rewaitAfterForcePush would poll CI on the
	// still-conflicted pre-resolve head forever.
	if _, err := s.relayBoxBundle(num, gen); err != nil {
		// A stop mid-backoff must stay errAbandoned, not become a never-green
		// Failed demotion of an issue Terminate already released (issue #3523).
		if errors.Is(err, errAbandoned) {
			return err
		}
		return fmt.Errorf("%w: relay after conflict-resolve failed: %v", errLandingNeverGreen, err)
	}
	return nil
}

// relayBoxBundle relays num's outbox bundle in via the Code Forge's optional
// forge.BundleRelay hook (issue #1919), for callers whose Box may have bundled
// instead of pushing. A read-write Code Forge never implements BundleRelay, so
// this is a no-op there: its Box already pushed during its own run. ref is the
// branch it relays, empty when the Code Forge is not a BundleRelay.
func (s *Settle) relayBoxBundle(num string, gen uint64) (ref string, err error) {
	cf := s.cfForNum(num)
	br, ok := cf.(forge.BundleRelay)
	if !ok {
		return "", nil
	}
	ref = cf.AgentBranch(num)
	if s.cfg.OutboxDir == nil {
		return ref, fmt.Errorf("settle: Config.OutboxDir is unset but the Code Forge implements forge.BundleRelay — every CODE_FORGE=local construction site must supply an OutboxDir resolver")
	}
	if err := s.retryingRelay(num, gen, br).RelayBundle(s.cfg.OutboxDir(num), ref); err != nil {
		return ref, fmt.Errorf("%w: %w", errRelayBundle, err)
	}
	return ref, nil
}

// rewaitAfterForcePush waits for CI to reach green on the PR's current head
// after a force-push reset its required checks. Its error is distinct from
// forge.ErrMergeConflict, so the caller's conflict-retry path is never
// re-entered for it. A push-only forge has no CI to wait for, so this is a
// no-op there rather than a crash on s.pr.CheckState (found reviewing #1698).
func (s *Settle) rewaitAfterForcePush(num string, gen uint64, pr string) error {
	if s.pr == nil {
		return nil
	}
	fmt.Printf("    #%s  landing=%s  status=post-force-push-wait\n", num, pr)
	obs, gReason := s.gateToGreen(num, gen, pr, false)
	if obs.outcome == gateGreen {
		// Restore ready (issue #1863). MarkReady is idempotent, so calling it
		// unconditionally beats threading a was-it-ever-demoted flag through
		// for the stale-base path that never demoted. Best-effort.
		if mrErr := s.pr.MarkReady(pr); mrErr != nil {
			fmt.Printf("    #%s  landing=%s  status=mark-ready-failed  !! %v\n", num, pr, mrErr)
		}
	}
	return rewaitGateResultErr(obs.outcome, gReason, pr)
}

// rewaitGateResultErr maps a gateToGreen outcome to rewaitAfterForcePush's
// return value. gateTerminal and gateRedRetry are named explicitly rather than
// folded into a catch-all default, so a future gateResult variant panics here
// instead of silently landing on "never green" (issue #1175).
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
