package dispatch

import (
	"errors"
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
)

// dispatchWithRetry runs once, retrying transient failures according to
// cfg, and returns the parsed Result once once() exits zero or the failure
// is terminal / the retry cap is exhausted.
//
//   - 429 with a known resetsAt: hold until the reset time (+ HoldJitterSecs),
//     then re-dispatch. A hold that ends in success or terminal does NOT
//     consume the retry cap. Consecutive holds that each end in another 429
//     count toward the cap (the "no-progress" case — the token never
//     recovered).
//   - Other transients (529/overloaded, network, 429 without resetsAt):
//     linear backoff retry up to TransientRetryMax, then give up.
//   - Terminal: give up immediately, no retry.
//
// Applies uniformly to Run and Fix (issue #441): a 429 during a fix pass now
// holds until reset instead of burning a fix attempt.
//
// A zero-exit box that reports no SPINDRIFT_OUTCOME line is classified the
// same as a non-zero exit (issue #565): a transient classification — rate
// limit or otherwise — feeds into the same hold/backoff decision below
// instead of dead-ending as status=missing. Only a genuinely terminal
// classification (no transient marker at all) returns as before, so
// status=missing still means "box finished cleanly but told us nothing, and
// there's nothing to retry."
func (d *Dispatch) dispatchWithRetry(logPath string, once func(resumeAfterHold bool) error) Result {
	holdCount := 0
	transientCount := 0
	prevWasHold := false

	for {
		resumeAfterHold := prevWasHold
		err := once(resumeAfterHold)

		var cls driver.Classification
		if err == nil {
			result := d.successResult(logPath)
			if result.OutcomeFound || result.ParseErr != nil || result.ClassifyErr != nil {
				return result
			}
			if result.Classification.Class != driver.Transient {
				return result
			}
			if exists, prErr := d.cfg.OpenPRForIssue(d.number); prErr == nil && exists {
				// The box's work already landed a PR; re-dispatching
				// would duplicate it. Pass the Result through unchanged
				// so settle's own PR lookup routes it (issue #565).
				return result
			}
			cls = result.Classification
		} else {
			if errors.Is(err, runner.ErrAlreadyRunning) {
				return Result{AlreadyInFlight: true}
			}

			if result, ok := d.settledOutcome(logPath); ok {
				// A non-zero exit still settles on a genuine, nonce-gated
				// outcome the box printed before dying (issue #2075): a run
				// resumed after a 429 hold can finish and print
				// status=ready/blocked yet exit non-zero, and reclassifying
				// that into another hold or an agent-failed would re-spend the
				// tokens the resume preserved. A limit-hit box prints no
				// outcome and still falls through to classification below.
				return result
			}

			var clsErr error
			cls, clsErr = d.driver.ClassifyTransient(logPath)
			if clsErr != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: classify error: %v\n", d.number, clsErr)
				return Result{Success: false}
			}

			if cls.Class == driver.Terminal {
				return Result{Success: false}
			}
		}

		if cls.Reason == driver.RateLimit && cls.ResetAt != nil {
			// 429 with known reset: hold until reset + jitter. A hold
			// following another hold (prevWasHold=true) means the token has
			// not recovered — consume the cap. A hold after a non-hold
			// iteration (success, terminal, or different transient) is
			// "free".
			if prevWasHold {
				holdCount++
			}
			if holdCount >= d.cfg.TransientRetryMax {
				fmt.Fprintf(d.humanOut(), "    !! #%s: hold cap exhausted (%d consecutive no-progress hold(s))\n",
					d.number, d.cfg.TransientRetryMax)
				return Result{Success: false}
			}
			wait := cls.ResetAt.Sub(d.clock.Now()) + time.Duration(d.cfg.HoldJitterSecs)*time.Second
			if wait < 0 {
				wait = time.Duration(d.cfg.HoldJitterSecs) * time.Second
			}
			fmt.Fprintf(d.humanOut(), "    .. #%s: rate limit; holding until %s\n",
				d.number, cls.ResetAt.UTC().Format("15:04 UTC"))
			d.clock.Sleep(wait)
			prevWasHold = true
			continue
		}

		// 529/overloaded, network, or 429 without a known reset time →
		// backoff retry.
		prevWasHold = false
		transientCount++
		if transientCount > d.cfg.TransientRetryMax {
			fmt.Fprintf(d.humanOut(), "    !! #%s: transient retry cap exhausted (%d)\n",
				d.number, d.cfg.TransientRetryMax)
			return Result{Success: false}
		}
		lb := retry.LinearBackoff{
			Unit:  time.Duration(d.cfg.TransientBackoffSecs) * time.Second,
			Clock: d.clock,
		}
		backoff := lb.Duration(transientCount)
		fmt.Fprintf(d.humanOut(), "    .. #%s: transient (%s); retry %d/%d in %s\n",
			d.number, cls.Reason, transientCount, d.cfg.TransientRetryMax, backoff)
		d.clock.Sleep(backoff)
	}
}

// successResult parses logPath's outcome line after a zero-exit dispatch. An
// unparseable line is reported via ParseErr without attempting
// classification; a missing outcome line (a box that exited zero without
// reporting one) falls back to a best-effort classification so the caller
// can explain what happened. The scan is gated on this run's own nonce
// (issue #1939): a SPINDRIFT_OUTCOME-shaped line that doesn't carry it is
// not a candidate, so an untrusted issue/comment author's echoed line can't
// win last-wins over the Box's genuine outcome. A skipped line still logs a
// warning so a spoof attempt or misconfigured run is visible.
func (d *Dispatch) successResult(logPath string) Result {
	o, found, skipped, err := outcome.LastInLog(logPath, d.nonce)
	if skipped {
		fmt.Fprintf(os.Stderr, "    ?? #%s: outcome scan: skipped a SPINDRIFT_OUTCOME-shaped line without this run's nonce\n", d.number)
	}
	if err != nil {
		return Result{Success: true, ParseErr: err}
	}
	if found {
		return d.outcomeResult(logPath, o)
	}
	cls, clsErr := d.driver.ClassifyTransient(logPath)
	return Result{Success: true, Classification: cls, ClassifyErr: clsErr}
}

// outcomeResult builds the fully populated Result for a parsed outcome o,
// gathering the companion comment / PR-intent / issue-intent host-mediated
// signals from logPath. Shared by the zero-exit success path (successResult)
// and the non-zero-exit settled-outcome path (settledOutcome, issue #2075) so
// both surface the identical signals.
func (d *Dispatch) outcomeResult(logPath string, o outcome.Outcome) Result {
	comment, commentFound, commentErr := outcome.LastCommentLineInLog(logPath, d.nonce)
	if commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: comment scan: %v\n", d.number, commentErr)
	}
	prIntent, prIntentFound, prIntentErr := outcome.LastPRIntentInLog(logPath, d.nonce)
	if prIntentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: pr-intent scan: %v\n", d.number, prIntentErr)
	}
	issueIntents, issueIntentsErr := outcome.AllIssueIntentLinesInLog(logPath, d.nonce)
	if issueIntentsErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent scan: %v\n", d.number, issueIntentsErr)
	}
	return Result{
		Success: true, Outcome: o, OutcomeFound: true,
		Comment: comment, CommentFound: commentFound,
		PRIntent: prIntent, PRIntentFound: prIntentFound,
		IssueIntents: issueIntents, IssueIntentsFound: len(issueIntents) > 0,
	}
}

// settledOutcome scans logPath for this run's nonce-bearing SPINDRIFT_OUTCOME
// line after a NON-ZERO exit. When one parses cleanly it returns the fully
// populated Result (Success and OutcomeFound true) plus ok=true, so a run
// that finished its work and printed status=ready/blocked yet exited non-zero
// -- a run resumed after a 429 hold whose driver process dies after emitting
// its verdict (issue #2075) -- settles on that verdict instead of being
// reclassified into another hold or an agent-failed, re-spending the tokens
// the resume preserved. ok=false means no genuine outcome was printed (a
// limit-hit box prints none, and a near-miss/unparseable line is left to the
// caller's transient classification), so the caller proceeds to classify. The
// scan is nonce-gated exactly like successResult's: a SPINDRIFT_OUTCOME-shaped
// line without this run's nonce is not a candidate and only logs a warning.
func (d *Dispatch) settledOutcome(logPath string) (Result, bool) {
	o, found, skipped, err := outcome.LastInLog(logPath, d.nonce)
	if skipped {
		fmt.Fprintf(os.Stderr, "    ?? #%s: outcome scan: skipped a SPINDRIFT_OUTCOME-shaped line without this run's nonce\n", d.number)
	}
	if err != nil || !found {
		return Result{}, false
	}
	return d.outcomeResult(logPath, o), true
}
