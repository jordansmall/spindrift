package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
)

// runReconcile drives the reconcile.Run seam and reports the outcome to w,
// then — on a clean sweep — surfaceAfterDispatch's auto-surface check (ADR
// 0033, issue #1730): closing a ticket's last seam this very sweep is
// exactly the moment that can newly complete it, so the check belongs here,
// not only at callers that already know a ticket just finished. reconcile
// itself is a concern only for an in-box-unreachable tracker (ADR 0029,
// InBoxUnreachableTracker): for any other c.issueTracker it is a clear
// no-op, not an error that looks like a crash.
//
// caps is threaded straight through to reconcile.Run (issue #2946) — every
// caller here already has one resolved (readContext/launchContext), so
// runReconcile never resolves its own.
func runReconcile(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	row, _ := backendByName(c.issueTracker)
	if !row.InBoxUnreachableTracker {
		fmt.Fprintf(w, "reconcile is an in-box-unreachable-tracker concern (ISSUE_TRACKER=%q) — nothing to do.\n", c.issueTracker)
		return nil
	}
	lw := localloop.Wire(localloopConfig(c), it)
	res, err := reconcile.Run(it, cf, lp, caps, lw.SeedScopeOf)
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
	return surfaceAfterDispatch(c, lw, caps, pwd, w, res.Stuck)
}

// reconcileAfterDispatch auto-invokes the reconcile sweep at the end of a
// dispatch run when the tracker is local (ADR 0029), so the common loop
// (dispatch -> immediate-merge -> issue auto-closes) needs no extra command.
// Unlike runReconcile's explicit refusal message on the standalone
// `spindrift reconcile` verb, this is a silent no-op for any other tracker —
// a routine github/jira dispatch run has nothing to report here. caps is as
// in runReconcile.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	row, _ := backendByName(c.issueTracker)
	if !row.InBoxUnreachableTracker {
		return nil
	}
	return runReconcile(c, it, cf, lp, caps, pwd, w)
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
// (every tracker but local). Takes lw rather than minting its own Wired
// (issue #1833): runReconcile's own lw is already in scope on every call
// path (cmdReconcile and reconcileAfterDispatch alike), so a second,
// independently-memoizing Wired only risked resolving a stuck issue's parent
// twice for no benefit. caps is as in runReconcile, threaded straight
// through to lw.Surface (issue #2946).
func surfaceAfterDispatch(c config, lw *localloop.Wired, caps forge.Capabilities, pwd string, w io.Writer, stuck map[string]string) error {
	row, _ := backendByName(c.codeForge)
	if !row.HostMediatedRemote {
		return nil
	}
	return lw.Surface(pwd, w, stuck, caps)
}

// cmdReconcile is the `reconcile` subcommand: the local-tracker bookkeeping
// sweep (ADR 0029). Like cmdDoctor, it needs only the IssueTracker/CodeForge
// seams plus a bare runner for the LivenessProbe's container check — no
// EnsureReady/IsReady gate, dispatch factory, or settle wiring — so it builds
// its wiring via newReadContext (issue #2941), including the LivenessProbe's
// conditional runner, rather than going through bootstrap.
func cmdReconcile() int {
	// "" (not dispatchKindWork): reconcile never dispatches, so it carries
	// no dispatch kind at all, matching its config before kind threading
	// (issue #2944) existed.
	rc := newReadContext("", false)

	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}

	lp := rc.reconcileLivenessProbe(pwd)
	if err := runReconcile(rc.config, rc.issueTracker, rc.codeForge, lp, rc.capabilities, pwd, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return 1
	}
	return 0
}
