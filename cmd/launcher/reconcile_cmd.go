package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/runner"
)

// runReconcile drives the reconcile.Run seam and reports the outcome to w.
// reconcile is `local`-tracker-specific (ADR 0029): for any other
// issueTracker it is a clear no-op, not an error that looks like a crash.
func runReconcile(it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, issueTracker string, w io.Writer) error {
	if issueTracker != "local" {
		fmt.Fprintf(w, "reconcile is a local-tracker concern (ISSUE_TRACKER=%q) — nothing to do.\n", issueTracker)
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
	return nil
}

// reconcileAfterDispatch auto-invokes the reconcile sweep at the end of a
// dispatch run when the tracker is local (ADR 0029), so the common loop
// (dispatch -> immediate-merge -> issue auto-closes) needs no extra command.
// Unlike runReconcile's explicit refusal message on the standalone
// `spindrift reconcile` verb, this is a silent no-op for any other tracker —
// a routine github/jira dispatch run has nothing to report here.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, w io.Writer) error {
	if c.issueTracker != "local" {
		return nil
	}
	return runReconcile(it, cf, lp, c.issueTracker, w)
}

// cmdReconcile is the `reconcile` subcommand: the local-tracker bookkeeping
// sweep (ADR 0029). Like cmdDoctor, it needs only the IssueTracker/CodeForge
// seams plus a bare runner for the LivenessProbe's container check — no
// EnsureReady/IsReady gate, dispatch factory, or settle wiring — so it does
// not go through bootstrap.
func cmdReconcile() int {
	c := loadConfig()
	it := newIssueTracker(c)
	cf := newCodeForge(c)
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	rc := runnerConfig(c)
	var r runner.Runner
	if c.runtime == "bwrap" {
		r = runner.NewBwrap(rc)
	} else {
		r = runner.NewOCI(rc, pwd)
	}
	lp := reconcile.NewFSProbe(pwd, r)
	if err := runReconcile(it, cf, lp, c.issueTracker, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}
