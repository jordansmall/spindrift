package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// errRecoverIneligible is recoverIssue's queue-mode verdict that an issue is not
// landable and the scan should move on; it may have posted a give-up comment.
var errRecoverIneligible = errors.New("recover: issue not eligible for queue recovery")

// recoverQueueOne is `spindrift recover` with no issue number: it walks the
// agent-failed issues and lands the first one carrying the same evidence a
// hand-run recover trusts, through recoverIssue's relayed-branch arm, doing in
// process the label work agent-recover.yml does around a manual recover. At
// most one issue is attempted per call. It returns errQueueEmpty when none
// qualified, and waves.ErrSignalledStop on an operator stop; a drain stop
// during a settle that fails still parks the issue first, as does an abort
// that reclaimed nothing (the settle already finished or landed it); an abort
// that took the issue is left to the watcher's reclaim. A tracker or forge
// outage during the scan is returned as an error rather than read as "nothing
// eligible", as is a failed restore or park that would leave the issue on
// agent-in-progress.
func recoverQueueOne(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.WorkSettler, stdout, stderr io.Writer) error {
	if caps.PRForge == nil {
		return errors.New("recover: queue mode needs a PR-shaped Code Forge (github or forgejo); a CODE_FORGE=local Recoverable issue stays manual, run `spindrift recover <n>` (ADR 0039)")
	}
	// The one install for the whole scan, handed to every candidate: a second
	// install inside recoverIssue would miss a stop sent during these reads.
	stopCh, abortCh, stopCleanup := installStopSignal()
	defer stopCleanup()

	if waves.SignalledStopAlready(stopCh, abortCh) {
		return waves.ErrSignalledStop
	}
	failed, err := it.ListIssues(forge.Failed)
	if err != nil {
		return fmt.Errorf("recover: list %s issues: %w", c.failedLabel, err)
	}
	for _, fi := range failed {
		if waves.SignalledStopAlready(stopCh, abortCh) {
			return waves.ErrSignalledStop
		}
		err := recoverIssue(stopCh, abortCh, true, c, it, cf, caps, pwd, f, s, fi.Number, stdout, stderr)
		if errors.Is(err, errRecoverIneligible) {
			continue
		}
		return err
	}
	fmt.Fprintf(stdout, "recover: no %s issue has a landable bundle and a genuine ready self-report\n", c.failedLabel)
	return errQueueEmpty
}

// finishQueueSettle records queue mode's settle verdict: a landed issue's
// attempt record is cleared, a failed one is parked. It returns only the
// record/park error; the caller records the verdict before honouring a drain
// stop, which abandons nothing, since exiting early would strand a failed
// settle on in-progress (#4679).
func finishQueueSettle(c config, it forge.IssueTracker, pwd, num string, rec recoverrecord.Record, settled bool, settleErr error, stdout, stderr io.Writer) error {
	if settled {
		if err := os.Remove(recoverrecord.Path(pwd, num)); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "    ?? #%s: remove recover attempts: %v\n", num, err)
		}
	} else if err := parkQueueFailure(c, it, num, rec, settleErr, time.Now(), stdout, stderr); err != nil {
		return err
	}
	return nil
}

// parkQueueFailure parks an issue queue mode claimed but could not land back on
// agent-failed, records the failed attempt, and reports the attempt as made
// (nil): an issue was tried, so the run exits 0, unless the record cannot be
// saved or the park itself fails (the issue would sit on agent-in-progress,
// which queue mode never revisits); either or both are returned, after the park
// is attempted. Only the attempt that reaches c.maxRecoverAttempts comments,
// once, saying auto-recover gave up (issue #4655); earlier failures stay
// silent and are retried after a backoff. The comment names the last attempt's
// cause (settleErr, kept in the record for a later pass; a nil settleErr keeps
// the earlier cause); a merge-gate failure is parked by the settler itself and
// never reaches here.
func parkQueueFailure(c config, it forge.IssueTracker, num string, rec recoverrecord.Record, settleErr error, now time.Time, stdout, stderr io.Writer) error {
	note := "queue recover could not land the outbox bundle"
	if settleErr != nil {
		rec.LastError = normalizeCause(settleErr.Error())
		note += ": " + rec.LastError
	}
	fmt.Fprintf(stdout, "    #%s  status=failed  note=%s\n", num, note)
	rec.Count++
	rec.Last = now
	if rec.Count >= c.maxRecoverAttempts {
		giveUpRecover(&rec, it, num, stderr)
	}
	// Without the record the next pass would retry with no backoff and no bound.
	saveErr := rec.Save()
	var parkErr error
	if err := it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		parkErr = fmt.Errorf("recover: park #%s on %s: %w", num, c.failedLabel, err)
	}
	// Emitted here, not through the settler's latch: a landing that returns
	// false never reaches a terminal transition there, so nothing else reports
	// this attempt's end.
	recoverSettled(num, forge.Failed, settle.ReasonRelayFailed, note)
	return errors.Join(saveErr, parkErr)
}
