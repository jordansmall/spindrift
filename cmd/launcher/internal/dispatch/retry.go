package dispatch

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/outcome"
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
func (d *Dispatch) dispatchWithRetry(logPath string, once func() error) Result {
	holdCount := 0
	transientCount := 0
	prevWasHold := false

	for {
		if err := once(); err == nil {
			return d.successResult(logPath)
		}

		cls, err := d.driver.ClassifyTransient(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: classify error: %v\n", d.number, err)
			return Result{Success: false}
		}

		if cls.Class == outcome.Terminal {
			return Result{Success: false}
		}

		if cls.Reason == outcome.RateLimit && cls.ResetAt != nil {
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
		return Result{Success: true, Outcome: o, OutcomeFound: true}
	}
	cls, clsErr := d.driver.ClassifyTransient(logPath)
	return Result{Success: true, Classification: cls, ClassifyErr: clsErr}
}
