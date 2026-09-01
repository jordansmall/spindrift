package dispatch

import (
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
)

// dispatchWithRetry runs once, retrying transient failures according to
// cfg, and returns the parsed Result once once() exits zero or the failure
// is terminal / the retry cap is exhausted.
//
//   - 429 with a known resetsAt: hold until the reset time (+ Policy.Jitter),
//     then re-dispatch. A hold that ends in success or terminal does NOT
//     consume the retry cap. Consecutive holds that each end in another 429
//     count toward the cap (the "no-progress" case — the token never
//     recovered).
//   - Other transients (529/overloaded, network, 429 without resetsAt):
//     linear backoff retry up to Policy.Max, then give up.
//   - Terminal: give up immediately, no retry.
//
// Applies uniformly to Run and Fix: a 429 during a fix pass holds until reset
// instead of burning a fix attempt.
//
// A zero-exit box that reports no SPINDRIFT_OUTCOME line is classified the same
// as a non-zero exit: a transient classification feeds the same hold/backoff
// decision below instead of dead-ending as status=missing. Only a genuinely
// terminal classification returns directly, so status=missing still means "box
// finished cleanly but told us nothing, and there's nothing to retry."
func (d *Dispatch) dispatchWithRetry(logPath string, once func(resumeAfterHold bool) error) Result {
	holdCount := 0
	transientCount := 0
	prevWasHold := false
	// prevRedispatched threads the resume signal on ANY re-dispatch — hold
	// OR backoff — distinct from prevWasHold, which is purely hold-cap
	// (no-progress) accounting below and must not be repurposed for this.
	prevRedispatched := false

	for {
		resumeAfterHold := prevRedispatched
		err := once(resumeAfterHold)

		var cls driver.Classification
		if err == nil {
			result := d.successResult(logPath)
			if result.Resolved.Found || result.ParseErr != nil || result.ClassifyErr != nil {
				return result
			}
			if result.Classification.Class != driver.Transient {
				return result
			}
			if exists, prErr := d.cfg.OpenPRForIssue(d.number); prErr == nil && exists {
				// The box's work already landed a PR; re-dispatching would
				// duplicate it. Pass the Result through unchanged so settle's
				// own PR lookup routes it.
				return result
			}
			cls = result.Classification
		} else {
			if errors.Is(err, runner.ErrAlreadyRunning) {
				return Result{AlreadyInFlight: true}
			}

			var qErr quarantineErr
			if errors.As(err, &qErr) {
				// quarantinePriorRunLogs failed before this attempt dispatched
				// anything, so logPath may still hold the exact prior run's
				// content it was trying to move aside: neither settledOutcome
				// nor ClassifyTransient may be trusted against it, hence the
				// `continue` rather than falling through to either.
				//
				// It must not give up on the FIRST failure either: a local
				// filesystem hiccup is what a short retry clears, and never
				// running the agent at all is worse than the mis-charge risk a
				// stray uncounted pass log carries.
				//
				// prevRedispatched/prevWasHold are deliberately left untouched:
				// no box attempt happened, so the next retry must still see
				// resumeAfterHold=false — rerunning quarantine fresh rather
				// than skipping it, and never setting RESUME_AFTER_HOLD on a
				// session that never started.
				fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", d.number, qErr)
				transientCount++
				if transientCount > d.cfg.Policy.Max {
					fmt.Fprintf(d.humanOut(), "    !! #%s: quarantine retry cap exhausted (%d)\n",
						d.number, d.cfg.Policy.Max)
					return Result{Success: false}
				}
				// Jitter deliberately omitted: it extends a hold wait (see the
				// rate-limit branch below), not a backoff retry.
				lb := retry.LinearBackoff{
					Unit:  d.cfg.Policy.Unit,
					Clock: d.clock,
				}
				backoff := lb.Duration(transientCount)
				fmt.Fprintf(d.humanOut(), "    .. #%s: quarantine failed; retry %d/%d in %s\n",
					d.number, transientCount, d.cfg.Policy.Max, backoff)
				d.clock.Sleep(backoff)
				continue
			}

			if result, ok := d.settledOutcome(logPath); ok {
				// A non-zero exit still settles on a genuine outcome the box
				// printed before dying: a run resumed after a 429 hold can
				// print status=ready/blocked yet exit non-zero, and
				// reclassifying that into another hold or an agent-failed
				// would re-spend the tokens the resume preserved.
				return result
			}

			var clsErr error
			cls, clsErr = d.driver.ClassifyTransient(logPath)
			if clsErr != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: classify error: %v\n", d.number, clsErr)
				return Result{Success: false, KilledBySignal: runner.KilledBySignal(err)}
			}

			if cls.Class == driver.Terminal {
				return Result{Success: false, KilledBySignal: runner.KilledBySignal(err)}
			}
		}

		if cls.Reason == driver.RateLimit && cls.ResetAt != nil {
			// A hold following another hold means the token has not
			// recovered — consume the cap. A hold after any non-hold
			// iteration is "free".
			if prevWasHold {
				holdCount++
			}
			if holdCount >= d.cfg.Policy.Max {
				fmt.Fprintf(d.humanOut(), "    !! #%s: hold cap exhausted (%d consecutive no-progress hold(s))\n",
					d.number, d.cfg.Policy.Max)
				return Result{Success: false}
			}
			wait := cls.ResetAt.Sub(d.clock.Now()) + d.cfg.Policy.Jitter
			if wait < 0 {
				wait = d.cfg.Policy.Jitter
			}
			fmt.Fprintf(d.humanOut(), "    .. #%s: rate limit; holding until %s\n",
				d.number, cls.ResetAt.UTC().Format("15:04 UTC"))
			d.clock.Sleep(wait)
			prevWasHold = true
			prevRedispatched = true
			continue
		}

		// 529/overloaded, network, or 429 without a known reset time →
		// backoff retry.
		prevWasHold = false
		prevRedispatched = true
		transientCount++
		if transientCount > d.cfg.Policy.Max {
			fmt.Fprintf(d.humanOut(), "    !! #%s: transient retry cap exhausted (%d)\n",
				d.number, d.cfg.Policy.Max)
			return Result{Success: false}
		}
		// Jitter deliberately omitted: hold-wait extension, not backoff.
		lb := retry.LinearBackoff{
			Unit:  d.cfg.Policy.Unit,
			Clock: d.clock,
		}
		backoff := lb.Duration(transientCount)
		fmt.Fprintf(d.humanOut(), "    .. #%s: transient (%s); retry %d/%d in %s\n",
			d.number, cls.Reason, transientCount, d.cfg.Policy.Max, backoff)
		d.clock.Sleep(backoff)
	}
}

// successResult parses logPath's outcome line after a zero-exit dispatch. An
// unparseable line is reported via ParseErr without attempting classification;
// a missing outcome line falls back to a best-effort classification so the
// caller can explain what happened. The scan is not nonce-gated (ADR 0047):
// the freshness boundary is purely structural — the line must lead the box's
// log, a guarantee the upstream in-box extractor enforces before this runs.
func (d *Dispatch) successResult(logPath string) Result {
	resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, d.cfg.Kind)
	if err != nil {
		return Result{Success: true, ParseErr: err}
	}
	if resolved.Found && resolved.IsGenuineOrSynthetic() {
		return d.outcomeResult(logPath, resolved)
	}
	cls, clsErr := d.driver.ClassifyTransient(logPath)
	return Result{Success: true, Classification: cls, ClassifyErr: clsErr}
}

// outcomeResult builds the fully populated Result for a parsed outcome,
// gathering the companion comment / PR-intent / issue-intent host-mediated
// signals from logPath. Shared by successResult and settledOutcome so the
// zero-exit and non-zero-exit paths surface identical signals.
func (d *Dispatch) outcomeResult(logPath string, resolved outcome.Resolved) Result {
	comment, commentFound, commentRejected, commentErr := outcome.LastCommentLineInLog(logPath, d.nonce)
	if commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: comment scan: %v\n", d.number, commentErr)
	}
	prIntent, prIntentFound, prIntentRejected, prIntentErr := outcome.LastPRIntentInLog(logPath, d.nonce)
	if prIntentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: pr-intent scan: %v\n", d.number, prIntentErr)
	}
	issueIntents, issueIntentsRejected, issueIntentsErr := outcome.AllIssueIntentLinesInLog(logPath, d.nonce)
	if issueIntentsErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent scan: %v\n", d.number, issueIntentsErr)
	}
	if resolved.SelfReportError != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: self-report scan: %v\n", d.number, resolved.SelfReportError)
	}
	return Result{
		Success: true, Resolved: resolved,
		Comment: comment, CommentFound: commentFound, CommentRejected: commentRejected,
		PRIntent: prIntent, PRIntentFound: prIntentFound, PRIntentRejected: prIntentRejected,
		IssueIntents: issueIntents, IssueIntentsFound: len(issueIntents) > 0, IssueIntentsRejected: issueIntentsRejected,
	}
}

// settledOutcome scans logPath for a SPINDRIFT_OUTCOME line after a NON-ZERO
// exit. A cleanly parsed one returns the fully populated Result plus ok=true,
// so a run that printed status=ready/blocked yet exited non-zero settles on
// that verdict instead of being reclassified into another hold or an
// agent-failed. ok=false means no genuine outcome was printed — a limit-hit box
// prints none, and a near-miss line is left to the caller's transient
// classification. Not nonce-gated (ADR 0047), same structural leading-line
// freshness boundary as successResult.
func (d *Dispatch) settledOutcome(logPath string) (Result, bool) {
	resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, d.cfg.Kind)
	if err != nil || !resolved.Found || !resolved.IsGenuineOrSynthetic() {
		return Result{}, false
	}
	return d.outcomeResult(logPath, resolved), true
}
