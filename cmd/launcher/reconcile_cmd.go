package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/runner"
)

// runReconcile drives the reconcile.Run seam and reports the outcome to w,
// then — on a clean sweep — surfaceAfterDispatch's auto-surface check (ADR
// 0033, issue #1730): closing a ticket's last seam this very sweep is
// exactly the moment that can newly complete it, so the check belongs here,
// not only at callers that already know a ticket just finished. reconcile
// itself is `local`-tracker-specific (ADR 0029): for any other
// c.issueTracker it is a clear no-op, not an error that looks like a crash.
func runReconcile(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, pwd string, w io.Writer) error {
	if c.issueTracker != "local" {
		fmt.Fprintf(w, "reconcile is a local-tracker concern (ISSUE_TRACKER=%q) — nothing to do.\n", c.issueTracker)
		return nil
	}
	res, err := reconcile.Run(it, cf, lp)
	if err != nil {
		if len(res.Closed) > 0 {
			fmt.Fprintf(w, "reconcile: closed %d issue(s) before error: %s\n", len(res.Closed), strings.Join(res.Closed, ", "))
		}
		if len(res.Abandoned) > 0 {
			fmt.Fprintf(w, "reconcile: flagged %d issue(s) abandoned before error: %s\n", len(res.Abandoned), strings.Join(res.Abandoned, ", "))
		}
		if len(res.Reset) > 0 {
			fmt.Fprintf(w, "reconcile: reset %d issue(s) before error: %s\n", len(res.Reset), strings.Join(res.Reset, ", "))
		}
		return err
	}
	if len(res.Closed) == 0 && len(res.Abandoned) == 0 {
		fmt.Fprintln(w, "reconcile: no issues closed.")
	} else {
		if len(res.Closed) > 0 {
			fmt.Fprintf(w, "reconcile: closed %d issue(s): %s\n", len(res.Closed), strings.Join(res.Closed, ", "))
		}
		if len(res.Abandoned) > 0 {
			fmt.Fprintf(w, "reconcile: flagged %d issue(s) abandoned: %s\n", len(res.Abandoned), strings.Join(res.Abandoned, ", "))
		}
	}
	if len(res.Reset) == 0 {
		fmt.Fprintln(w, "reconcile: no issues reset.")
	} else {
		fmt.Fprintf(w, "reconcile: reset %d issue(s): %s\n", len(res.Reset), strings.Join(res.Reset, ", "))
	}
	return surfaceAfterDispatch(c, it, pwd, w, res.Stuck)
}

// reconcileAfterDispatch auto-invokes the reconcile sweep at the end of a
// dispatch run when the tracker is local (ADR 0029), so the common loop
// (dispatch -> immediate-merge -> issue auto-closes) needs no extra command.
// Unlike runReconcile's explicit refusal message on the standalone
// `spindrift reconcile` verb, this is a silent no-op for any other tracker —
// a routine github/jira dispatch run has nothing to report here.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, pwd string, w io.Writer) error {
	if c.issueTracker != "local" {
		return nil
	}
	return runReconcile(c, it, cf, lp, pwd, w)
}

// surfaceAfterDispatch surfaces every completed broad ticket's Integration
// branch into pwd as a local branch, once every one of its seam issues is
// closed — CODE_FORGE=local's auto-surface exit (ADR 0033, issue #1730),
// delegated to localloop.Wire's Surface (issue #1806) so this and the
// composed loop test drive the identical sweep. stuck threads through
// reconcile.Run's own Result.Stuck (issue #1811) so Surface's held verdicts
// can name a ticket's stuck landing without redoing the ancestry check
// itself. A no-op for any codeForge other than "local";
// localloop.Wired.Surface itself covers the tracker-has-no-SeamLister no-op
// (every tracker but local). This mints its own Wired rather than sharing
// the run's own (issue #1810): runReconcile is reachable from the
// standalone `reconcile` subcommand (cmdReconcile), a separate process
// invocation with no dispatch-time Wired to share, not only from
// reconcileAfterDispatch's tail end of a run that already built one.
func surfaceAfterDispatch(c config, it forge.IssueTracker, pwd string, w io.Writer, stuck map[string]string) error {
	if c.codeForge != "local" {
		return nil
	}
	return localloop.Wire(localloopConfig(c), it).Surface(pwd, w, stuck)
}

// cmdReconcile is the `reconcile` subcommand: the local-tracker bookkeeping
// sweep (ADR 0029). Like cmdDoctor, it needs only the IssueTracker/CodeForge
// seams plus a bare runner for the LivenessProbe's container check — no
// EnsureReady/IsReady gate, dispatch factory, or settle wiring — so it does
// not go through bootstrap.
func cmdReconcile() int {
	c := loadConfig()
	it := newIssueTracker(c)
	cf := newCodeForge(c, local.SanitizedParent{})

	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}

	// The runner only matters for the LivenessProbe's container check, which
	// runReconcile below only reaches for a local tracker — skip building one
	// for the common github/jira "nothing to do" refusal.
	var lp reconcile.LivenessProbe
	if c.issueTracker == "local" {
		rc := runnerConfig(c)
		var r runner.Runner
		if c.runtime == "bwrap" {
			r = runner.NewBwrap(rc)
		} else {
			r = runner.NewOCI(rc, pwd)
		}
		lp = reconcile.NewFSProbe(pwd, r)
	}
	if err := runReconcile(c, it, cf, lp, pwd, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}
