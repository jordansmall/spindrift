package dispatch

import (
	"errors"
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
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
func (d *Dispatch) dispatchWithRetry(logPath string, once func() error) Result {
	holdCount := 0
	transientCount := 0
	prevWasHold := false

	for {
		err := once()

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
				fmt.Printf("    !! #%s: hold cap exhausted (%d consecutive no-progress hold(s))\n",
					d.number, d.cfg.TransientRetryMax)
				return Result{Success: false}
			}
			wait := cls.ResetAt.Sub(d.clock.Now()) + time.Duration(d.cfg.HoldJitterSecs)*time.Second
			if wait < 0 {
				wait = time.Duration(d.cfg.HoldJitterSecs) * time.Second
			}
			fmt.Printf("    .. #%s: rate limit; holding until %s\n",
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
			fmt.Printf("    !! #%s: transient retry cap exhausted (%d)\n",
				d.number, d.cfg.TransientRetryMax)
			return Result{Success: false}
		}
		backoff := time.Duration(d.cfg.TransientBackoffSecs) * time.Second * time.Duration(transientCount)
		fmt.Printf("    .. #%s: transient (%s); retry %d/%d in %s\n",
			d.number, cls.Reason, transientCount, d.cfg.TransientRetryMax, backoff)
		d.clock.Sleep(backoff)
	}
}

// successResult parses logPath's outcome line after a zero-exit dispatch. An
// unparseable line is reported via ParseErr without attempting
// classification; a missing outcome line (a box that exited zero without
// reporting one) falls back to a best-effort classification so the caller
// can explain what happened.
func (d *Dispatch) successResult(logPath string) Result {
	o, found, err := outcome.LastInLog(logPath)
	if err != nil {
		return Result{Success: true, ParseErr: err}
	}
	if found {
		comment, commentFound, commentErr := outcome.LastCommentInLog(logPath)
		if commentErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: comment scan: %v\n", d.number, commentErr)
		}
		return Result{Success: true, Outcome: o, OutcomeFound: true, Comment: comment, CommentFound: commentFound}
	}
	cls, clsErr := d.driver.ClassifyTransient(logPath)
	return Result{Success: true, Classification: cls, ClassifyErr: clsErr}
}
