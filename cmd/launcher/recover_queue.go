package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

// errRecoverIneligible is recoverIssue's queue-mode verdict that an issue is not
// landable and the scan should move on; it may have posted a give-up comment.
var errRecoverIneligible = errors.New("recover: issue not eligible for queue recovery")

// recoverQueueOne is `spindrift recover` with no issue number: it walks the
// agent-failed issues and lands the first one carrying the evidence a hand-run
// recover trusts, through recoverIssue's relayed-branch arm, doing in process
// the label work agent-recover.yml does around a manual recover. At most one
// issue is attempted. It returns errQueueEmpty when none qualified and
// waves.ErrSignalledStop on an operator stop; a drain stop during a failing
// settle, or an abort that reclaimed nothing, still parks the issue first; an
// abort that took the issue is left to the watcher's reclaim. A
// tracker or forge outage during the scan is an error, never "nothing
// eligible", as is a failed restore or park that would strand the issue on
// agent-in-progress.
func recoverQueueOne(c config, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, pwd string, f *dispatch.Factory, s settle.MergeSettler, terminated *terminate.Registry, stdout, stderr io.Writer) error {
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
		return fmt.Errorf("recover: list %s issues: %w", c.FailedLabel, err)
	}
	for _, fi := range failed {
		if waves.SignalledStopAlready(stopCh, abortCh) {
			return waves.ErrSignalledStop
		}
		err := recoverIssue(stopCh, abortCh, true, c, it, cf, caps, pwd, f, s, terminated, fi.Number, stdout, stderr)
		if errors.Is(err, errRecoverIneligible) {
			continue
		}
		return err
	}
	fmt.Fprintf(stdout, "recover: no %s issue has a landable bundle and a genuine ready self-report\n", c.FailedLabel)
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
// agent-failed and records the failed attempt. It returns nil (an issue was
// tried, so the run exits 0) unless the record save or the park fails; a
// failed park leaves the issue on agent-in-progress, which queue mode never
// revisits. Only the
// attempt reaching c.MaxRecoverAttempts, or a Workflows-permission refusal a
// retry cannot fix (issue #5411), comments once that auto-recover gave up
// (issue #4655); earlier failures stay silent and retry after a backoff. The
// comment names the last attempt's cause; a nil settleErr keeps the earlier
// one. A merge-gate failure is parked by the settler itself and never reaches
// here.
func parkQueueFailure(c config, it forge.IssueTracker, num string, rec recoverrecord.Record, settleErr error, now time.Time, stdout, stderr io.Writer) error {
	note := "queue recover could not land the outbox bundle"
	if settleErr != nil {
		rec.LastError = normalizeCause(settleErr.Error())
		note += ": " + rec.LastError
	}
	fmt.Fprintf(stdout, "    #%s  status=failed  note=%s\n", num, note)
	rec.Count++
	rec.Last = now
	workflowDenied := errors.Is(settleErr, forge.ErrWorkflowPermissionDenied)
	if settleErr != nil {
		rec.WorkflowPermissionDenied = workflowDenied
	}
	if rec.Count >= c.MaxRecoverAttempts || workflowDenied {
		giveUpRecover(&rec, it, num, stderr)
	}
	// Without the record the next pass would retry with no backoff and no bound.
	saveErr := rec.Save()
	var parkErr error
	v, note, err := settle.Transition(it, num, forge.InProgress, settle.ForReason(dispatchrecord.ReasonRelayFailed), note)
	if err != nil {
		parkErr = fmt.Errorf("recover: park #%s on %s: %w", num, c.FailedLabel, err)
	}
	// Emitted here, not through the settler's latch: a landing that returns
	// false never reaches a terminal transition there, so nothing else reports
	// this attempt's end.
	recoverSettled(c, num, v, note)
	return errors.Join(saveErr, parkErr)
}
