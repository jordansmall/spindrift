package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/settle"
)

// recordLateMerges upgrades Records whose PR was left open at settle and has
// merged since, printing a line only when it upgraded any. A nil pr (a
// PR-less code forge: git, local) has no PR state to read, so it does
// nothing. Per-Record warnings go to stderr, like the settle gate's.
func recordLateMerges(pr forge.PRForge, pwd string, w io.Writer) error {
	if pr == nil {
		return nil
	}
	merged, err := settle.LateMerges(pwd, pr, time.Now(), os.Stderr)
	if len(merged) > 0 {
		fmt.Fprintf(w, "reconcile: recorded %d late merge(s): %s\n", len(merged), strings.Join(merged, ", "))
	}
	return err
}

// runReconcile is the standalone `spindrift reconcile` verb. It records late
// merges whenever the Code Forge has PRs, whatever the tracker, since a PR
// left open at settle and merged later is otherwise never counted as a landing
// by `spindrift stats`, then always runs reconcileIssues: a stats-store
// failure still exits non-zero (joined into the result) but never skips the
// issue bookkeeping, which does not depend on it.
func runReconcile(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	lateErr := recordLateMerges(caps.PRForge, pwd, w)
	return errors.Join(reconcileIssues(c, it, cf, lp, caps, pwd, w), lateErr)
}

// reconcileIssues drives reconcile.Run, reports the outcome to w, and on a
// clean sweep runs surfaceAfterDispatch: closing a ticket's last seam is the
// moment that can newly complete the ticket (ADR 0033, issue #1730). Only an
// in-box-unreachable tracker reconciles any issues (ADR 0029), so the guard
// below reads caps.TrackerDescriptor and is a no-op (issues #2946, #3064).
func reconcileIssues(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	if !caps.TrackerDescriptor.InBoxUnreachableTracker {
		fmt.Fprintf(w, "reconcile: issue bookkeeping is an in-box-unreachable-tracker concern (ISSUE_TRACKER=%q) — no issues to close.\n", c.issueTracker)
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

// reconcileAfterDispatch runs the sweep at the end of a dispatch run (ADR
// 0029). Unlike the standalone `spindrift reconcile` verb, it stays silent for
// any other tracker unless a late merge was recorded, since a routine
// github/jira run has nothing else to report. Late-merge recording is
// best-effort on every tracker: a stats bookkeeping failure only warns, never
// failing the dispatch run nor skipping the issue bookkeeping.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	if err := recordLateMerges(caps.PRForge, pwd, w); err != nil {
		fmt.Fprintf(w, "    ?? late merges: %v\n", err)
	}
	if caps.TrackerDescriptor.InBoxUnreachableTracker {
		return reconcileIssues(c, it, cf, lp, caps, pwd, w)
	}
	return nil
}

// surfaceAfterDispatch surfaces a completed broad ticket's Integration branch
// into pwd once all its seam issues are closed (ADR 0033, issues #1730, #1806).
// stuck carries reconcile.Run's Result.Stuck so Surface names a stuck landing
// without redoing the ancestry check (issue #1811). It takes the caller's lw
// because minting a second Wired resolves the same parents twice (issue #1833).
func surfaceAfterDispatch(c config, lw *localloop.Wired, caps forge.Capabilities, pwd string, w io.Writer, stuck map[string]string) error {
	if !caps.ForgeDescriptor.HostMediatedRemote {
		return nil
	}
	return lw.Surface(pwd, w, stuck, caps)
}

// cmdReconcile is the `reconcile` subcommand, the late-merge and local-tracker
// bookkeeping sweep (ADR 0029). It needs no EnsureReady gate, dispatch
// factory, or settle wiring, so it builds its seams through newReadContext
// rather than bootstrap (issue #2941).
func cmdReconcile(stdout, stderr io.Writer) int {
	// reconcile never dispatches, so it carries no dispatch kind (issue #2944).
	rc := newReadContext(nil, false)

	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}

	lp := rc.reconcileLivenessProbe(pwd)
	if err := runReconcile(rc.config, rc.issueTracker, rc.codeForge, lp, rc.capabilities, pwd, stdout); err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	return 0
}
