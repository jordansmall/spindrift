package main

import (
	"errors"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// errRecoverIneligible is recoverIssue's queue-mode verdict that an issue is not
// landable and the scan should move on; nothing was written for it.
var errRecoverIneligible = errors.New("recover: issue not eligible for queue recovery")

// recoverQueueOne is `spindrift recover` with no issue number: it walks the
// agent-failed issues and lands the first one carrying the same evidence a
// hand-run recover trusts, through recoverIssue's relayed-branch arm, doing in
// process the label work agent-recover.yml does around a manual recover. At
// most one issue is attempted per call. It returns errQueueEmpty when none
// qualified, and waves.ErrSignalledStop on an operator stop, which never parks
// the issue. A tracker or forge outage during the scan is returned as an
// error rather than read as "nothing eligible".
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

// parkQueueFailure parks an issue queue mode claimed but could not land back on
// agent-failed with a comment, and reports the attempt as made (nil): an issue
// was tried, so the run exits 0. The settler does not hand back why the landing
// failed, so the comment names the stages (bundle relay, draft PR creation)
// rather than a cause; a merge-gate failure is parked by the settler itself and
// never reaches here.
func parkQueueFailure(c config, it forge.IssueTracker, num string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "    #%s  status=failed  note=queue recover could not land the outbox bundle\n", num)
	body := fmt.Sprintf("Queue-mode `spindrift recover` claimed this issue but the outbox bundle did not land: the bundle relay or draft PR creation failed. Fix the cause, then re-run `spindrift recover %s`.", num)
	if err := it.Comment(num, body); err != nil {
		fmt.Fprintf(stderr, "    ?? #%s: comment: %v\n", num, err)
	}
	if err := it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		fmt.Fprintf(stderr, "    ?? #%s: park on %s: %v\n", num, c.failedLabel, err)
	}
	return nil
}
