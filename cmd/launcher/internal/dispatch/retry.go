package dispatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/runner"
)

// dispatchWithRetry calls once until it exits zero, the failure is terminal, or
// a cap is exhausted. A 429 with a known reset holds until that time, consuming
// the cap only when the previous iteration also held; other transients back off
// linearly. It covers Run and Fix alike (issue #441). A zero-exit box printing
// no SPINDRIFT_OUTCOME line is classified like a non-zero exit (issue #565).
func (d *Dispatch) dispatchWithRetry(logPath string, once func(resumeAfterHold bool) error) Result {
	holdCount := 0
	transientCount := 0
	prevWasHold := false
	// prevRedispatched covers any re-dispatch, hold or backoff. prevWasHold is
	// purely hold-cap accounting below and must not be repurposed for it.
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
				// duplicate it. Let settle's own PR lookup route it (issue #565).
				return result
			}
			cls = result.Classification
		} else {
			if errors.Is(err, runner.ErrAlreadyRunning) {
				return Result{AlreadyInFlight: true}
			}
			if errors.Is(err, errKilled) {
				// The abort already released this issue; waves' Box goroutine
				// routes it through its abandon branch before it ever reads
				// Success (issue #3521).
				return Result{Success: false}
			}

			var qErr quarantineErr
			if errors.As(err, &qErr) {
				// Quarantine failed before this attempt dispatched anything,
				// so logPath may still hold the prior run's content: neither
				// settledOutcome nor ClassifyTransient may be trusted against
				// it (issue #2575). Leave prevRedispatched/prevWasHold alone
				// too, so the retry reruns quarantine and skips the resume.
				fmt.Fprintf(os.Stderr, "    ?? #%s: %v\n", d.number, qErr)
				transientCount++
				if transientCount > d.cfg.Policy.Max {
					fmt.Fprintf(d.humanOut(), "    !! #%s: quarantine retry cap exhausted (%d)\n",
						d.number, d.cfg.Policy.Max)
					return Result{Success: false}
				}
				backoff := d.cfg.Policy.Backoff(d.clock).Duration(transientCount)
				fmt.Fprintf(d.humanOut(), "    .. #%s: quarantine failed; retry %d/%d in %s\n",
					d.number, transientCount, d.cfg.Policy.Max, backoff)
				d.sleepOrKilled(backoff)
				continue
			}

			if result, ok := d.settledOutcome(logPath); ok {
				// A non-zero exit still settles on a genuine outcome the box
				// printed before dying (issue #2075): reclassifying it would
				// re-spend the tokens a post-hold resume preserved. A limit-hit
				// box prints no outcome and falls through to classification.
				return result
			}

			var clsErr error
			cls, clsErr = d.driver.ClassifyTransient(logPath)
			if clsErr != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: classify error: %v\n", d.number, clsErr)
				return Result{Success: false, KilledBySignal: runner.KilledBySignal(err)}
			}

			if cls.Class == driver.Terminal {
				result := Result{Success: false, KilledBySignal: runner.KilledBySignal(err)}
				if logIsEmpty(logPath) {
					// A box that ran and failed left something in its log, so an
					// empty log means it never launched (a pre-Box registry-proxy
					// or outbox-setup error, issue #3119). Report that error
					// rather than the reason-free "FAILED" a caller would print.
					result.Err = err
				}
				return result
			}
		}

		if cls.Reason == driver.RateLimit && cls.ResetAt != nil {
			// A hold following another hold means the token never recovered, so
			// it consumes the cap. A hold after any other iteration is free.
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
			d.sleepOrKilled(wait)
			prevWasHold = true
			prevRedispatched = true
			continue
		}

		// 529/overloaded, network, or a 429 with no known reset time.
		prevWasHold = false
		prevRedispatched = true
		transientCount++
		if transientCount > d.cfg.Policy.Max {
			fmt.Fprintf(d.humanOut(), "    !! #%s: transient retry cap exhausted (%d)\n",
				d.number, d.cfg.Policy.Max)
			return Result{Success: false}
		}
		backoff := d.cfg.Policy.Backoff(d.clock).Duration(transientCount)
		fmt.Fprintf(d.humanOut(), "    .. #%s: transient (%s); retry %d/%d in %s\n",
			d.number, cls.Reason, transientCount, d.cfg.Policy.Max, backoff)
		d.sleepOrKilled(backoff)
	}
}

// logIsEmpty reports whether logPath is missing or zero bytes, the "box never
// launched" signal behind Result.Err (issue #3119). A stat error other than
// not-exist counts as non-empty: no evidence the box never launched.
func logIsEmpty(logPath string) bool {
	info, err := os.Stat(logPath)
	if err != nil {
		return os.IsNotExist(err)
	}
	return info.Size() == 0
}

// successResult parses logPath's outcome line after a zero-exit dispatch,
// falling back to a best-effort classification when no line parses. The scan is
// not nonce-gated (ADR 0039): the freshness boundary is structural, since the
// in-box extractor guarantees the line leads the box's log.
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
// gathering the comment, PR-intent and issue-intent signals from whichever
// carrier this Dispatch's BOX_SIGNAL_CARRIER selects (issue #3725): the
// log carrier's marker-line scanners in "log" mode, or d.signalBuffer in
// "socket" mode. Shared by the zero-exit and non-zero-exit settled paths
// (issue #2075) so both report identical signals.
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
	// Under the socket carrier the log scan above still runs, but only to
	// warn: a marker line surviving in the log means a Box or an untrusted
	// echo wrote one anyway, which under this carrier is stale and must
	// contribute no data -- neither the payload nor the *Rejected counts,
	// which describe log-carrier lines only. d.signalBuffer, not the log
	// scan, is the sole source of truth for all three fields here.
	if d.cfg.signalCarrierSocket() {
		// A line that attempted the grammar and failed to verify is still a
		// marker line on the log carrier, so it warns alongside a verifying
		// one: under this carrier both are equally stale.
		warnStaleMarker := func(channel string, found bool, rejected outcome.Rejections) {
			if !found && rejected.Total() == 0 {
				return
			}
			fmt.Fprintf(os.Stderr, "    ?? #%s: %s marker line found in log under BOX_SIGNAL_CARRIER=socket; ignored\n", d.number, channel)
		}
		warnStaleMarker("comment", commentFound, commentRejected)
		warnStaleMarker("pr-intent", prIntentFound, prIntentRejected)
		warnStaleMarker("issue-intent", len(issueIntents) > 0, issueIntentsRejected)
		comment, commentFound, prIntent, prIntentFound, issueIntents = signalResultFromBuffer(d.signalBuffer)
		commentRejected, prIntentRejected, issueIntentsRejected = outcome.Rejections{}, outcome.Rejections{}, outcome.Rejections{}
	}
	if resolved.SelfReportError != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: self-report scan: %v\n", d.number, resolved.SelfReportError)
	}
	passes, passesErr := passmanifest.Read(filepath.Join(OutboxDirFor(d.pwd, d.number), passmanifest.FileName))
	if passesErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: pass-manifest scan: %v\n", d.number, passesErr)
	}
	return Result{
		Success: true, Resolved: resolved,
		Comment: comment, CommentFound: commentFound, CommentRejected: commentRejected,
		PRIntent: prIntent, PRIntentFound: prIntentFound, PRIntentRejected: prIntentRejected,
		IssueIntents: issueIntents, IssueIntentsFound: len(issueIntents) > 0, IssueIntentsRejected: issueIntentsRejected,
		Passes: passes,
	}
}

// settledOutcome returns the Result for an outcome line printed before a
// NON-ZERO exit, so a run that finished its work and died after emitting its
// verdict (issue #2075) settles on it instead of re-spending the tokens a resume
// preserved. ok=false when none parses, and the caller classifies instead. Not
// nonce-gated (ADR 0039), same leading-line boundary as successResult.
func (d *Dispatch) settledOutcome(logPath string) (Result, bool) {
	resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, d.cfg.Kind)
	if err != nil || !resolved.Found || !resolved.IsGenuineOrSynthetic() {
		return Result{}, false
	}
	return d.outcomeResult(logPath, resolved), true
}
