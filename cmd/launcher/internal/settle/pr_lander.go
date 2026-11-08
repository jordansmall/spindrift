package settle

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

// prLander watches CI on a PR, applies the merge mode to it, and verifies a
// merge against the PR's state. pr is the PR settle opened or adopted, never
// the Box's placeholder branch-name landing= value, so a host-mediated landing
// verifies against the real PR.
type prLander struct{ *Settle }

func (s prLander) land(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool, _ []passmanifest.Entry) (landingResult, string, error) {
	landing, reason, err := s.selfHealGate(d, num, gen, pr, requireRegistration)
	if landing == landingMerged && !errors.Is(err, errAbandoned) {
		s.verifyMerged(num, pr)
	}
	return landing, reason, err
}

// errLandingNeverGreen marks a force-pushed head that never reached green. A
// merge failure on an already-green PR leaves the issue agent-complete (ADR
// 0012); here no green PR exists at the current head, so selfHeal demotes to
// agent-failed instead (issue #758). A genuinely red head (errForcePushRed)
// first gets the shared fix-pass budget; the demotion applies only once that is
// spent, or when the re-wait timed out or errored (issue #5095).
var errLandingNeverGreen = errors.New("settle: force-pushed head never went green")

// errForcePushRed is the red subset of errLandingNeverGreen: the post-force-push
// re-wait ended on a genuine CI failure, which a fix pass can address, unlike a
// timeout or API error.
var errForcePushRed = fmt.Errorf("%w: CI went red", errLandingNeverGreen)

// selfHealGate is the PR Lander's landing: it polls the merge gate,
// dispatching fix boxes on genuine red up to MaxFixAttempts times, then applies
// the merge mode. It swaps agent-complete only once the landing path settles
// (issue #757). A merge failure on a still-green PR stays agent-complete (ADR
// 0012). A force-pushed head that goes red gets a fix pass from the same
// MaxFixAttempts budget (issue #5095); one that never re-confirms green demotes
// to agent-failed instead (issue #758).
//
// requireRegistration is set for a PR found independently of this process's own
// push (SettleAdopted): the head SHA may not be one this process just pushed,
// so the first gate poll requires evidence this run's checks registered before
// trusting a SUCCESS rollup, within a bounded window (issues #1652, #2475). It
// guards only the loop's first attempt, because a fix-pass retry always follows
// a push d.Fix just made in this process.
func (s prLander) selfHealGate(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool) (landingResult, string, error) {
	// forcePushFixes counts the fix passes run on the current force-pushed
	// head, so the failure comment can name what that head consumed. It is
	// zero until an immediate-mode force-push's head goes red, and resets
	// for each new force-pushed head. Non-zero is also how the gateRedRetry
	// arm recognizes a red that follows such a fix pass.
	forcePushFixes := 0
	for attempt := 0; ; attempt++ {
		obs, gateReason := s.gateToGreen(num, gen, pr, requireRegistration && attempt == 0)
		// A mark (Terminate or Reclaim) already released num back to
		// Dispatchable by the time any check below sees it, so no arm past
		// that point may commit tracker state or dispatch further work for
		// num (issue #3523).
		if errors.Is(obs.err, errAbandoned) {
			return landingFailed, "", errAbandoned
		}
		switch obs.outcome {
		case gateGreen:
			// Catches a mark landing during the gate's own confirm sleep,
			// before MarkReady's idempotent ready flip below.
			if s.terminated(num, gen) {
				return landingFailed, "", errAbandoned
			}
			// The launcher, never the Driver, owns the draft to ready flip at
			// green, inverting the old draft-until-ready invariant (issues
			// #1651, #1653, #1654, #1614, #1625). MarkReady is idempotent, so
			// it runs before the hold and merge-mode checks below: a PR
			// downgraded to manual must not stay stranded as a draft. It also
			// precedes any mergeability read, since Forgejo reports a draft pull
			// unmergeable; a PR that does conflict is demoted again by the
			// conflict response, as an immediate merge's conflict is.
			if mrErr := s.pr.MarkReady(pr); mrErr != nil {
				s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMarkReadyFailed, Detail: fmt.Sprintf("!! %v", mrErr)})
			}
			// A hold takes the PR away from the merge below, so a human gets it
			// instead; under immediate that PR must still merge into its base
			// first, which the merge path's own conflict handling would have
			// checked (issue #6368).
			holdReason, postHoldComment := s.handOffHold(num, pr)
			held := holdReason != ""
			var mergeReason string
			var err error
			if held {
				err = s.resolveBeforeHandOff(num, gen, pr, d)
			} else {
				mergeReason, err = s.applyMergeMode(num, gen, pr, d)
			}
			if err != nil {
				if errors.Is(err, errAbandoned) {
					return landingFailed, "", errAbandoned
				}
				// The force-push reset the head's checks and they came back red:
				// the same fix pass as a pre-merge red, drawing the same budget.
				// A green head loops back through gateToGreen into a fresh merge.
				if errors.Is(err, errForcePushRed) {
					forcePushFixes = 0
					if attempt >= s.cfg.MaxFixAttempts {
						return s.failForcePushedHead(num, pr, err, s.fixBudgetNote(attempt, forcePushFixes))
					}
					if s.terminated(num, gen) {
						return landingFailed, "", errAbandoned
					}
					s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusPostForcePushRed})
					if fs := s.fixPass(d, num, gen, pr, attempt); fs.stop {
						return fs.landing, fs.note, fs.err
					}
					forcePushFixes++
					continue
				}
				if errors.Is(err, errLandingNeverGreen) {
					return s.failForcePushedHead(num, pr, err, "")
				}
				s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeBlocked, Detail: fmt.Sprintf("!! %v", err)})
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				postHoldComment()
				landing, err := s.completeLanding(num, gen, landingManual, dispatchrecord.ReasonMergeBlocked)
				return landing, "", err
			}
			if held {
				postHoldComment()
				landing, err := s.completeLanding(num, gen, landingManual, holdReason)
				return landing, "", err
			}
			landing, err := s.completeLanding(num, gen, mergedLanding(mergeReason), mergeReason)
			return landing, "", err
		case gateTerminal:
			// Catches a mark landing mid-poll, before the Failed commit and
			// comment below.
			if s.terminated(num, gen) {
				return landingFailed, "", errAbandoned
			}
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusGateTerminal, Detail: "!! " + gateReason})
			s.it.Comment(num, fmt.Sprintf("landing failed: %s", gateReason))
			s.transitionState(num, ForReason(dispatchrecord.ReasonGateTerminal), gateReason)
			return landingFailed, gateReason, nil
		case gateRedRetry:
			// Catches a mark landing mid-poll, before the fix-exhausted and
			// budget-exhausted Failed commits below.
			if s.terminated(num, gen) {
				return landingFailed, "", errAbandoned
			}
			// The fix pass that followed a red force-pushed head left it red:
			// a landing failure, not the generic fix-exhausted class.
			if attempt >= s.cfg.MaxFixAttempts && forcePushFixes > 0 {
				return s.failForcePushedHead(num, pr, forcePushRedErr(pr), s.fixBudgetNote(attempt, forcePushFixes))
			}
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFixExhausted, Detail: fmt.Sprintf("!! exhausted %d fix pass(es)", s.cfg.MaxFixAttempts)})
				}
				reason := fmt.Sprintf("ci-red: still red after exhausting %d fix pass(es)", s.cfg.MaxFixAttempts)
				// With no fix pass allowed none ran, so the note's own ci-red is the
				// accurate class, not fix-exhausted.
				exhaustedReason := dispatchrecord.ReasonCIRed
				if s.cfg.MaxFixAttempts > 0 {
					exhaustedReason = dispatchrecord.ReasonFixExhausted
				}
				if s.cfg.Unclaimed {
					// No label records this failure (transitionState is a
					// no-op for an unclaimed issue, issue #4076), unlike the
					// gateTerminal arm above which already comments.
					if commentErr := s.it.Comment(num, fmt.Sprintf("landing failed: %s", reason)); commentErr != nil {
						fmt.Fprintf(os.Stderr, "    ?? #%s: post unclaimed-failure comment: %v\n", num, commentErr)
					}
				}
				s.transitionState(num, ForReason(exhaustedReason), reason)
				return landingFailed, reason, nil
			}
			if fs := s.fixPass(d, num, gen, pr, attempt); fs.stop {
				return fs.landing, fs.note, fs.err
			}
			if forcePushFixes > 0 {
				forcePushFixes++
			}
		}
	}
}

// failForcePushedHead settles a force-pushed head that never reached green:
// Failed with dispatchrecord.ReasonLandingFailed, since no green PR exists at
// the current head. budgetNote is fixBudgetNote's text for a red head, empty
// for a timeout or a conflict-resolve or relay failure, which no fix pass
// answers.
func (s prLander) failForcePushedHead(num, pr string, err error, budgetNote string) (landingResult, string, error) {
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusLandingFailed, Detail: fmt.Sprintf("!! %v", err)})
	s.it.Comment(num, fmt.Sprintf("landing failed: %v%s — no green PR exists at the current head", err, budgetNote))
	s.transitionState(num, ForReason(dispatchrecord.ReasonLandingFailed), err.Error())
	return landingFailed, err.Error(), nil
}

// fixBudgetNote names the fix passes a red force-pushed head left spent, in
// total and since the force-push, for its landing-failed comment.
func (s prLander) fixBudgetNote(attempt, forcePushFixes int) string {
	if s.cfg.MaxFixAttempts == 0 {
		return "; no fix passes allowed (MAX_FIX_ATTEMPTS=0)"
	}
	return fmt.Sprintf("; fix budget exhausted (%d of %d fix pass(es) used, %d on the force-pushed head)",
		attempt, s.cfg.MaxFixAttempts, forcePushFixes)
}

// fixStop is fixPass's verdict: when stop is set, selfHealGate must return the
// given landing, note and err (errAbandoned when a stop landed) instead of
// re-polling.
type fixStop struct {
	landing landingResult
	note    string
	err     error
	stop    bool
}

// fixPass runs one red-CI fix pass for attempt (zero-based).
// Both genuine red before a merge and genuine red after an immediate-mode
// force-push (issue #5095) come through here, so they share one attempt counter.
func (s prLander) fixPass(d dispatch.Dispatcher, num string, gen uint64, pr string, attempt int) fixStop {
	// The budget caps (issue #2001) stop a runaway token or cost run
	// even while MaxFixAttempts would still allow more passes. Both
	// knobs unset skips the check, so the no-cap path never pays
	// CumulativeUsage's disk stat and parse over every pass log.
	if s.cfg.MaxBudgetTokens > 0 || s.cfg.MaxBudgetUSD > 0 {
		cum := d.CumulativeUsage()
		if exceeded, reason := usage.BudgetExceeded(cum.TotalTokens(), cum.TotalCostUSD, s.cfg.MaxBudgetTokens, s.cfg.MaxBudgetUSD); exceeded {
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusBudgetExhausted, Detail: "!! " + reason})
			s.it.Comment(num, fmt.Sprintf("budget exhausted (%s) — stopping self-heal before another fix pass", reason))
			note := fmt.Sprintf("budget-exhausted: %s", reason)
			s.transitionState(num, ForReason(dispatchrecord.ReasonBudgetExhausted), note)
			return fixStop{landing: landingFailed, note: note, stop: true}
		}
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Detail: fmt.Sprintf("fix-pass=%d/%d", attempt+1, s.cfg.MaxFixAttempts)})
	// Best-effort: a failure to fetch the CI failure detail must never
	// block the fix pass, so the summary falls back to empty.
	detail, detailErr := s.pr.FailureDetail(pr)
	if detailErr != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFailureDetailUnavailable, Detail: fmt.Sprintf("!! %v", detailErr)})
		detail = ""
	}
	// Best-effort: headErr suppresses the no-op check below rather than
	// aborting the fix pass outright.
	headBefore, headErr := s.pr.HeadCommitSHA(pr)
	if headErr != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusHeadShaUnavailable, Detail: fmt.Sprintf("!! %v", headErr)})
	}
	disp := d.Fix(attempt+1, detail)
	// Catches d.Fix's own exit — a non-zero result can be Reclaim's
	// SIGKILL — before the Failed commit or bundle relay below.
	if s.terminated(num, gen) {
		return fixStop{landing: landingFailed, err: errAbandoned, stop: true}
	}
	stopped := dispatch.Route(disp,
		func() fixStop {
			// No Box started: a live run owns this issue's in-progress claim,
			// so leave its state alone and do not retry (issue #3655).
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusAlreadyInFlight, Detail: fmt.Sprintf("~~ fix pass %d skipped; live run continues", attempt+1)})
			return fixStop{landing: landingFailed, err: errAbandoned, stop: true}
		},
		func(result dispatch.Result) fixStop {
			RecordSettleWarnings(d, num, fmt.Sprintf("fix pass %d: ", attempt+1), result)
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFixFailed, Detail: fmt.Sprintf("!! fix pass %d exited non-zero — aborting self-heal", attempt+1)})
			result.ReportFailureReason(num)
			s.it.Comment(num, fmt.Sprintf("fix pass %d exited non-zero — aborting self-heal", attempt+1))
			note := fmt.Sprintf("fix-failed: fix pass %d exited non-zero", attempt+1)
			s.transitionState(num, ForReason(dispatchrecord.ReasonFixFailed), note)
			return fixStop{landing: landingFailed, note: note, stop: true}
		},
		func(result dispatch.Result) fixStop {
			RecordSettleWarnings(d, num, fmt.Sprintf("fix pass %d: ", attempt+1), result)
			return fixStop{}
		})
	if stopped.stop {
		return stopped
	}
	// A read-only Box holds no push-capable token (issue #1979): its fix
	// agent bundled its work to the outbox, so HeadCommitSHA reflects no
	// work until this relay lands it. It must run before the no-op check
	// below, which would otherwise misread every read-only fix pass as a
	// no-op. Only an unparkable failure (a missing bundle or a wiring error)
	// falls through to it.
	if _, err := s.relayBoxBundle(num, gen); err != nil {
		// A stop mid-backoff already released num (issue #3523); falling
		// through would log a failure and sleep the no-op confirm poll.
		if errors.Is(err, errAbandoned) {
			return fixStop{landing: landingFailed, err: errAbandoned, stop: true}
		}
		// The bundle is intact in the outbox but unrelayed, so head never
		// moved: falling through would misreport it as fix-no-op.
		if relayFailureParkable(err) {
			// The final relay attempt is not a backoff, so a mark landing
			// there yields an exhausted error, not errAbandoned.
			if s.terminated(num, gen) {
				return fixStop{landing: landingFailed, err: errAbandoned, stop: true}
			}
			// Not parkRelayFailure: the PR is already open, so `recover`
			// re-gates it rather than relaying this bundle.
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusRelayFailed, Detail: fmt.Sprintf("!! fix pass %d: %v", attempt+1, err)})
			next := relayFailureNext(num, err, fmt.Sprintf("run `spindrift recover %s` to re-gate the PR (recover does not relay this bundle).", num))
			s.it.Comment(num, fmt.Sprintf("fix pass %d's bundle relay failed: %v. Its commits never reached the PR; %s", attempt+1, err, next))
			note := fmt.Sprintf("relay-failed: fix pass %d's bundle relay failed; its commits never reached the PR", attempt+1)
			s.transitionState(num, ForReason(dispatchrecord.ReasonRelayFailed), note)
			return fixStop{landing: landingFailed, note: note, stop: true}
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFixRelayFailed, Detail: fmt.Sprintf("!! %v", err)})
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
					return fixStop{landing: landingFailed, err: errAbandoned, stop: true}
				}
				s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFixNoOp, Detail: fmt.Sprintf("!! fix pass %d produced no new commit — aborting self-heal", attempt+1)})
				s.it.Comment(num, fmt.Sprintf("fix pass %d produced no new commit — aborting self-heal", attempt+1))
				note := fmt.Sprintf("fix-no-op: fix pass %d produced no new commit", attempt+1)
				s.transitionState(num, ForReason(dispatchrecord.ReasonFixNoOp), note)
				return fixStop{landing: landingFailed, note: note, stop: true}
			}
		}
	}
	return fixStop{}
}

// gateToGreen polls CheckState until confirmed SUCCESS, terminal failure, or
// MergePollTimeout, swapping no labels: it re-runs mid-landing, so the caller
// owns agent-complete (issue #757). requireRegistration refuses a first-poll
// SUCCESS inherited from an earlier attempt until a non-terminal state proves
// this run's checks are alive (#1652), bounded by registrationWindow (#2475).
func (s prLander) gateToGreen(num string, gen uint64, pr string, requireRegistration bool) (watchObservation, string) {
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
	case gateGreen, gateRedRetry:
		return obs, ""
	case gateTerminal:
		// fall through to reason formatting below.
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", obs.outcome))
	}

	// poll() does no I/O of its own, so this block prints the check-state-error
	// status line as well as formatting the operator-facing reason.
	if errors.Is(obs.err, errAbandoned) {
		return obs, ""
	}
	if obs.err != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusCheckStateError, Detail: fmt.Sprintf("!! %v", obs.err)})
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
func (s prLander) mergeGuardHit(pr string) ([]string, error) {
	if strings.TrimSpace(s.cfg.MergeGuardPaths) == "" {
		return nil, nil
	}
	files, err := s.pr.ListPRFiles(pr)
	if err != nil {
		return nil, err
	}
	return matchedGuardPaths(s.cfg.MergeGuardPaths, files), nil
}

// commentHoldPR posts a merge-hold comment on the PR, where a reviewer of
// the held PR will look. A failed post is logged only: the hold stands anyway.
func (s prLander) commentHoldPR(num, pr, body string) {
	if err := s.pr.CommentPR(pr, body); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: post merge-hold PR comment: %v\n", num, err)
	}
}

// mergeByHandRows is the catalog rows whose findings the host never merges.
func mergeByHandRows() []chore.Row {
	var rows []chore.Row
	for _, r := range catalogRows() {
		if r.MergeByHand {
			rows = append(rows, r)
		}
	}
	return rows
}

// mergeByHandHold reports whether num's issue is a finding of a MergeByHand
// Chore, whose PR the host never merges, and which row held it. With no such
// row the issue is not read at all. A tracker whose issues wear labels is
// judged by the row's finding label alone, so removing it is the override; one
// whose issues do not (local) may never have applied the label, so the body's
// chore=<name> marker counts there too. An unreadable issue is an error: the
// caller holds as a precaution.
func (s prLander) mergeByHandHold(num string) (chore.Row, bool, error) {
	rows := mergeByHandRows()
	if len(rows) == 0 {
		return chore.Row{}, false, nil
	}
	iss, err := s.it.Issue(num)
	if err != nil {
		return chore.Row{}, false, err
	}
	wearsLabels := s.cfg.Capabilities.TrackerDescriptor.IssuesWearLabels
	for _, r := range rows {
		if wearsFindingLabel(r, iss.Labels) {
			return r, true, nil
		}
		if !wearsLabels && carriesChoreMarker(iss.Body, r.Name) {
			return r, true, nil
		}
	}
	return chore.Row{}, false, nil
}

// handOffHold decides whether the green PR is held from the merge mode and
// handed to a human instead: a merge-guard hit or check error, or a
// merge-by-hand hold. It logs the hold as soon as it is decided, so a
// conflict response that then fails leaves that line ahead of its own. A
// non-empty reason is the landing's settled reason, and comment posts the
// explanation once the hand-off is certain; it is a no-op when nothing holds.
func (s prLander) handOffHold(num, pr string) (reason string, comment func()) {
	matched, guardErr := s.mergeGuardHit(pr)
	if guardErr != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeGuardCheckError, Detail: fmt.Sprintf("!! %v", guardErr)})
		return dispatchrecord.ReasonMergeGuardCheckError, func() {
			s.commentHoldPR(num, pr, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
		}
	}
	if len(matched) > 0 {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeGuardHit, Detail: fmt.Sprintf("paths=%v", matched)})
		return dispatchrecord.ReasonMergeGuardHit, func() { s.commentHoldPR(num, pr, mergeGuardComment(matched)) }
	}
	heldRow, isHeld, holdErr := s.mergeByHandHold(num)
	if holdErr != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeGuardCheckError, Detail: fmt.Sprintf("check=tuning  !! %v", holdErr)})
		return dispatchrecord.ReasonMergeGuardCheckError, func() {
			s.commentHoldPR(num, pr, holdCheckErrorComment(mergeByHandRows(), holdErr))
		}
	}
	if isHeld {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeByHand})
		return dispatchrecord.ReasonMergeByHand, func() { s.commentHoldPR(num, pr, holdComment(heldRow)) }
	}
	return "", func() {}
}

// mergeableUnknownPolls bounds how long resolveBeforeHandOff waits for the
// forge to compute a PR's mergeability before giving up on a verdict.
const mergeableUnknownPolls = 3

// awaitMergeable reads pr's mergeability, polling up to mergeableUnknownPolls
// times while the forge answers UNKNOWN. ok is false when no verdict came, an
// unreadable forge or one still UNKNOWN; the reason is already logged, with
// noVerdict naming what the caller does without one.
func (s prLander) awaitMergeable(num, pr string, interval time.Duration, noVerdict string) (forge.MergeableState, bool) {
	for poll := 0; poll < mergeableUnknownPolls; poll++ {
		if poll > 0 {
			s.clock.Sleep(interval)
		}
		state, err := s.pr.Mergeable(pr)
		if err != nil {
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeableCheckError, Detail: fmt.Sprintf("!! %v", err)})
			return forge.MergeableUnknown, false
		}
		if state != forge.MergeableUnknown {
			return state, true
		}
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeableUnknown, Detail: "~~ " + noVerdict})
	return forge.MergeableUnknown, false
}

// resolveBeforeHandOff runs the immediate path's conflict response ahead of a
// hold's hand-off, so a human is never handed a PR that no longer merges into
// its base (issue #6368). Other merge modes keep their exits. Only a
// CONFLICTING verdict that a re-read one poll interval later repeats acts:
// Forgejo's boolean is also false while its async conflict check runs, so one
// read is not proof. Both reads wait out UNKNOWN through awaitMergeable; a
// forge that gives no verdict, or a re-read that does not repeat CONFLICTING,
// hands off without a conflict response. The response is respondToConflict, the
// same one mergeImmediate runs, so a failure returns the errors that path does
// and selfHealGate settles them identically.
func (s prLander) resolveBeforeHandOff(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	if s.cfg.MergeMode != "immediate" {
		return nil
	}
	interval := time.Duration(s.cfg.MergePollInterval) * time.Second
	if state, ok := s.awaitMergeable(num, pr, interval, "handing off without a conflict check"); !ok || state != forge.MergeableConflicting {
		return nil
	}
	s.clock.Sleep(interval)
	again, ok := s.awaitMergeable(num, pr, interval, "handing off without a conflict response")
	if !ok {
		return nil
	}
	if again != forge.MergeableConflicting {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusConflictUnconfirmed, Detail: "~~ handing off without a conflict response"})
		return nil
	}
	if s.terminated(num, gen) {
		return errAbandoned
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusHandoffConflict})
	conflictErr := fmt.Errorf("%w: PR conflicts with its base", forge.ErrMergeConflict)
	_, err := s.respondToConflict(num, gen, pr, d, s.cfForNum(num), conflictErr, &rebaseBudget{})
	return err
}

// respondToConflict answers a genuine merge conflict on a green PR: demote it
// to draft (issue #1863) as a visible signal it is not currently mergeable,
// rebase it (retrying a transient push failure), fall back to a conflict-resolve
// dispatch when the rebase itself conflicts, and wait for the force-pushed head
// to go green. budget is the caller's, shared across that caller's own calls
// (mergeImmediate's loop), so the caller decides how far it reaches; an
// exhausted budget returns conflictErr unchanged.
// resolved reports that a conflict-resolve dispatch ran, so the caller's next
// conflict is the same one read from a stale snapshot and must not be rebased
// again.
func (s prLander) respondToConflict(num string, gen uint64, pr string, d dispatch.Dispatcher, cf forge.CodeForge, conflictErr error, budget *rebaseBudget) (resolved bool, err error) {
	// Best-effort.
	if mdErr := s.pr.MarkDraft(pr); mdErr != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMarkDraftFailed, Detail: fmt.Sprintf("!! %v", mdErr)})
	}
	var resolve func() error
	if d != nil {
		resolve = func() error {
			if crErr := s.resolveConflict(num, gen, pr, d); crErr != nil {
				return crErr
			}
			return s.rewaitAfterForcePush(num, gen, pr)
		}
	}
	resolved, err = s.rebaseOnConflict(num, pr, cf, conflictErr, budget, resolve)
	if err != nil || resolved {
		return resolved, err
	}
	// The rebase force-push reset the PR's required checks, so the next merge
	// attempt must wait for the new head to go green.
	return false, s.rewaitAfterForcePush(num, gen, pr)
}

// applyMergeMode performs the mode-specific action once a landing is ready to
// merge: CI green on a PR. It runs before the Complete commit, which
// selfHealGate makes through completeLanding after it returns; see
// completeLanding for why that order matters (issues #3523, #4879) and for the
// Unclaimed case. A returned merge failure settles Complete with
// dispatchrecord.ReasonMergeBlocked (ADR 0012), except errAbandoned, which
// commits nothing, and selfHealGate's errLandingNeverGreen, which settles
// Failed unless it is a red head with fix budget left (errForcePushRed, which
// takes a fix pass instead, issue #5095). A nil d makes a rebase conflict
// immediately non-retriable, since nothing can dispatch a conflict resolution.
// On a nil error the returned string is the reason class of the landing,
// decided here. This is the PR forge's mode switch; a push-only forge has its
// own in pushOnlyLander.applyMode.
func (s prLander) applyMergeMode(num string, gen uint64, pr string, d dispatch.Dispatcher) (string, error) {
	switch s.cfg.MergeMode {
	case "immediate":
		if err := s.mergeImmediate(num, gen, pr, d); err != nil {
			return "", err
		}
		return dispatchrecord.ReasonMerged, nil
	case "auto":
		if err := s.pr.EnqueueAutoMerge(pr); err != nil {
			// Audited (issues #1233, #831): execClient.EnqueueAutoMerge captures
			// no stdout or stderr, so err is only ever an *exec.ExitError, a
			// start failure, or a message embedding the already-public prURL,
			// never gh's stderr. Safe to surface verbatim in the comment below.
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusAutoMergeEnqueueFailed, Detail: fmt.Sprintf("!! %v", err)})
			s.it.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return dispatchrecord.ReasonMergeBlocked, nil
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusAutoMergeEnqueued})
		return dispatchrecord.ReasonAutoMergeEnqueued, nil
	case "manual":
		// CODE_FORGE=local requires MERGE_MODE=immediate (validated at launcher
		// startup, issue #1725), so a forge.BundleRelay hook never reaches
		// manual mode here.
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusAgentComplete, Detail: "merge-mode=" + s.cfg.MergeMode})
		return dispatchrecord.ReasonManual, nil
	default:
		return "", fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// mergeImmediate merges the green PR, rebasing on conflict. A successful
// conflict-resolve already rebased and force-pushed, so the next conflict is
// retried directly after a settle wait rather than rebased a second time. The
// termination check ahead of preflightStaleBase duplicates the loop's own first
// iteration deliberately, because preflightStaleBase force-pushes (issue #943).
func (s prLander) mergeImmediate(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	var budget rebaseBudget
	if s.terminated(num, gen) {
		return errAbandoned
	}
	// preflightStaleBase keeps its own retry budget: sharing the rebase budget with
	// the conflict loop below would let one path exhaust the other's allowance.
	if err := s.preflightStaleBase(num, gen, pr, d); err != nil {
		return err
	}
	cf := s.cfForNum(num)
	skipRebase := false
	return s.mergeLoop(num, gen, pr, cf, func(conflictErr error) error {
		if skipRebase {
			// The conflict-resolve dispatch already ran and restored ready, so
			// this ErrMergeConflict is the same resolved conflict read from a
			// stale mergeability snapshot. Re-demoting would leave the Merge
			// retry attempting a draft PR (issue #1863).
			skipRebase = false
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeRetrySettle})
			s.clock.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			return nil
		}
		resolved, rErr := s.respondToConflict(num, gen, pr, d, cf, conflictErr, &budget)
		skipRebase = resolved
		return rErr
	})
}

// preflightStaleBase rebases pr when the forge reports its branch is behind the
// base (issue #936): a green PR can still be stale, never having tested a sibling
// that landed first. Opt-in via PreflightStaleBase (ADR 0028). A NeedsUpdate
// error is swallowed, since the caller's Merge surfaces the same problem, but a
// Rebase failure is hard (issue #940): staleness is confirmed, the fix failed.
func (s prLander) preflightStaleBase(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	if !s.cfg.PreflightStaleBase {
		return nil
	}
	stale, err := s.pr.NeedsUpdate(pr)
	if err != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusNeedsUpdateCheckError, Detail: fmt.Sprintf("!! %v", err)})
		return nil
	}
	if !stale || s.cfg.MaxRebaseAttempts <= 0 {
		return nil
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusStaleBaseRebase, Detail: fmt.Sprintf("attempt=1/%d", s.cfg.MaxRebaseAttempts)})
	cf := s.cfForNum(num)
	rbErr := cf.Rebase(pr)
	for pushRetries := 0; rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts; pushRetries++ {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusRebasePushRetry, Detail: fmt.Sprintf("attempt=%d/%d  !! %v", pushRetries+1, s.cfg.MaxRebaseAttempts, rbErr)})
		s.transientBackoff().Do(pushRetries + 1)
		rbErr = cf.Rebase(pr)
	}
	if rbErr != nil {
		isConflict := errors.Is(rbErr, forge.ErrMergeConflict)
		if isConflict {
			// A genuine conflict: demote to draft (issue #1863) whether or not a
			// Dispatcher can resolve it.
			if mdErr := s.pr.MarkDraft(pr); mdErr != nil {
				s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMarkDraftFailed, Detail: fmt.Sprintf("!! %v", mdErr)})
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
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusStaleBaseRebaseFailed, Detail: fmt.Sprintf("!! %v", rbErr)})
		return rbErr
	}
	return s.rewaitAfterForcePush(num, gen, pr)
}

// resolveConflict dispatches a Box to resolve a genuine ErrMergeConflict hit by
// a force-pushing rebase. It returns errAbandoned when Reclaim reaps the Box
// mid-dispatch, since its SIGKILL can surface as crErr indistinguishably from
// a genuine dispatch failure (issue #3523), or when the issue's own Box is
// already running (issue #3655).
func (s prLander) resolveConflict(num string, gen uint64, pr string, d dispatch.Dispatcher) error {
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusConflictResolve})
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
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusAlreadyInFlight, Detail: "~~ conflict-resolve skipped; live run continues"})
		return errAbandoned
	}
	if crErr != nil {
		// Audited (issue #831): the OCI and bwrap adapters both wire the Box's
		// stdout and stderr to the log file, not to the returned error, so crErr
		// is only ever an *exec.ExitError or a start failure, never Box-internal
		// output. Safe to surface verbatim in selfHeal's issue comment.
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusConflictResolveFailed, Detail: fmt.Sprintf("!! %v", crErr)})
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

// rewaitAfterForcePush waits for CI to reach green on the PR's current head
// after a force-push reset its required checks. Its error is distinct from
// forge.ErrMergeConflict, so the caller's conflict-retry path is never
// re-entered for it.
func (s prLander) rewaitAfterForcePush(num string, gen uint64, pr string) error {
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusPostForcePushWait})
	obs, gReason := s.gateToGreen(num, gen, pr, false)
	if errors.Is(obs.err, errAbandoned) {
		return errAbandoned
	}
	if obs.outcome == gateGreen {
		// Restore ready (issue #1863). MarkReady is idempotent, so calling it
		// unconditionally beats threading a was-it-ever-demoted flag through
		// for the stale-base path that never demoted. Best-effort.
		if mrErr := s.pr.MarkReady(pr); mrErr != nil {
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMarkReadyFailed, Detail: fmt.Sprintf("!! %v", mrErr)})
		}
	}
	return rewaitGateResultErr(obs.outcome, gReason, pr)
}

// forcePushRedErr is the error for a force-pushed head on pr whose CI went red.
func forcePushRedErr(pr string) error {
	return fmt.Errorf("%w after force-push on %s", errForcePushRed, pr)
}

// rewaitGateResultErr maps a gateToGreen outcome to rewaitAfterForcePush's
// return value. gateTerminal and gateRedRetry are named explicitly rather than
// folded into a catch-all default, so a future gateResult variant panics here
// instead of silently landing on "never green" (issue #1175).
func rewaitGateResultErr(g gateResult, reason, pr string) error {
	switch g {
	case gateGreen:
		return nil
	case gateRedRetry:
		return forcePushRedErr(pr)
	case gateTerminal:
		if reason != "" {
			return fmt.Errorf("%w: CI did not reach green after force-push on %s (%s)", errLandingNeverGreen, pr, reason)
		}
		return fmt.Errorf("%w: CI did not reach green after force-push on %s", errLandingNeverGreen, pr)
	default:
		panic(fmt.Sprintf("settle: unhandled gateResult %v", g))
	}
}

// verifyMerged confirms a PR reported merged carries both a MERGED state and
// the CompleteLabel, and demotes the issue to Failed otherwise, since a gap
// there means something merged outside the gate. A read that still fails after
// its retries proves nothing, so it leaves the issue as it stands rather than
// demote it. A verified merge also drops the landed bundle; a demoted settle
// keeps it.
func (s prLander) verifyMerged(num, pr string) {
	var prState forge.PRState
	var iss forge.Issue
	err := s.retryVerifyRead(num, pr, func() error {
		var err error
		prState, err = s.pr.PRState(pr)
		return err
	})
	if err == nil {
		err = s.retryVerifyRead(num, pr, func() error {
			var err error
			iss, err = s.it.Issue(num)
			return err
		})
	}
	if err != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeUnverifiedReadError, Detail: fmt.Sprintf("!! %v", err)})
		return
	}
	if prState == forge.PRMerged && containsLabel(iss.Labels, s.cfg.CompleteLabel) {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusVerifiedMerged})
		s.removeLandedBundle(num)
		s.closeIssue(num)
		return
	}
	var reason string
	if prState != forge.PRMerged {
		reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
	} else {
		reason = fmt.Sprintf("issue does not carry '%s'", s.cfg.CompleteLabel)
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFailed, Detail: "!! " + reason})
	s.transitionState(num, ForReason(dispatchrecord.ReasonMergeUnverified), reason)
}

// retryVerifyRead runs read, retrying a failure up to Policy.Max times with
// backoff.
func (s prLander) retryVerifyRead(num, pr string, read func() error) error {
	attempts := 0
	for {
		err := read()
		if err == nil {
			return nil
		}
		if attempts >= s.cfg.Policy.Max {
			return err
		}
		attempts++
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeUnverifiedReadRetry,
			Detail: fmt.Sprintf("attempt=%d/%d  !! %v", attempts, s.cfg.Policy.Max, err)})
		s.transientBackoff().Do(attempts)
	}
}
