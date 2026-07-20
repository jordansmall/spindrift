package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/reconcile"
)

// runReconcile drives the reconcile.Run seam and reports the outcome to w.
// reconcile is `local`-tracker-specific (ADR 0029): for any other
// issueTracker it is a clear no-op, not an error that looks like a crash.
func runReconcile(it forge.IssueTracker, cf forge.CodeForge, issueTracker string, w io.Writer) error {
	if issueTracker != "local" {
		fmt.Fprintf(w, "reconcile is a local-tracker concern (ISSUE_TRACKER=%q) — nothing to do.\n", issueTracker)
		return nil
	}
	res, err := reconcile.Run(it, cf)
	if err != nil {
		if len(res.Closed) > 0 {
			fmt.Fprintf(w, "reconcile: closed %d issue(s) before error: %s\n", len(res.Closed), strings.Join(res.Closed, ", "))
		}
		if len(res.Abandoned) > 0 {
			fmt.Fprintf(w, "reconcile: flagged %d issue(s) abandoned before error: %s\n", len(res.Abandoned), strings.Join(res.Abandoned, ", "))
		}
		return err
	}
	if len(res.Closed) == 0 && len(res.Abandoned) == 0 {
		fmt.Fprintln(w, "reconcile: no issues closed.")
		return nil
	}
	if len(res.Closed) > 0 {
		fmt.Fprintf(w, "reconcile: closed %d issue(s): %s\n", len(res.Closed), strings.Join(res.Closed, ", "))
	}
	if len(res.Abandoned) > 0 {
		fmt.Fprintf(w, "reconcile: flagged %d issue(s) abandoned: %s\n", len(res.Abandoned), strings.Join(res.Abandoned, ", "))
	}
	return nil
}

// reconcileAfterDispatch auto-invokes the reconcile sweep at the end of a
// dispatch run when the tracker is local (ADR 0029), so the common loop
// (dispatch -> immediate-merge -> issue auto-closes) needs no extra command.
// Unlike runReconcile's explicit refusal message on the standalone
// `spindrift reconcile` verb, this is a silent no-op for any other tracker —
// a routine github/jira dispatch run has nothing to report here.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, w io.Writer) error {
	if c.issueTracker != "local" {
		return nil
	}
	return runReconcile(it, cf, c.issueTracker, w)
}

// cmdReconcile is the `reconcile` subcommand: the local-tracker bookkeeping
// sweep (ADR 0029). Like cmdDoctor, it needs only the IssueTracker/CodeForge
// seams — no runner/dispatch/settle wiring — so it does not go through
// bootstrap.
func cmdReconcile() int {
	c := loadConfig()
	it := newIssueTracker(c)
	cf := newCodeForge(c)
	if err := runReconcile(it, cf, c.issueTracker, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}
