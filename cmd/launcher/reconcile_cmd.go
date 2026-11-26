package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/settle"
)

// recordLateMerges upgrades Records whose PR was left open at settle and has
// merged since, printing a line only when it upgraded any. A nil pr (a
// PR-less code forge: git, local) has no PR state to read, so it does
// nothing. Per-Record warnings go to stderr, like the settle gate's.
func recordLateMerges(caps forge.Capabilities, drivers *driver.Set, pwd string, w io.Writer) error {
	if caps.PRForge == nil {
		return nil
	}
	merged, err := settle.LateMerges(pwd, drivers, caps.PRForge, caps.MergeCommitReader, time.Now(), os.Stderr)
	if len(merged) > 0 {
		fmt.Fprintf(w, "reconcile: recorded %d late merge(s): %s\n", len(merged), strings.Join(merged, ", "))
	}
	return err
}

// runReconcile is the standalone `spindrift reconcile` verb. It records late
// merges whenever the Code Forge has PRs, whatever the tracker (a PR merged
// after settle is otherwise never counted as a landing by `spindrift stats`),
// then always runs reconcileIssues: a stats-store failure is joined into the
// result but never skips the issue bookkeeping.
func runReconcile(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	lateErr := recordLateMerges(caps, c.connectors.Drivers(), pwd, w)
	return errors.Join(reconcileIssues(c, it, cf, lp, caps, pwd, w), lateErr)
}

// reconcileIssues drives reconcile.Run, reports the outcome to w, and on a
// clean sweep runs surfaceAfterDispatch: closing a ticket's last seam is the
// moment that can newly complete the ticket (ADR 0033, issue #1730). Only an
// in-box-unreachable tracker reconciles issues (ADR 0029); any other gets a
// one-line notice from the caps.TrackerDescriptor guard (issues #2946, #3064).
func reconcileIssues(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	if !caps.TrackerDescriptor.InBoxUnreachableTracker {
		fmt.Fprintf(w, "reconcile: issue bookkeeping is an in-box-unreachable-tracker concern (ISSUE_TRACKER=%q) — no issues to close.\n", c.IssueTracker)
		return nil
	}
	lw := localloop.Wire(localloopConfig(c), it)
	res, err := reconcile.Run(it, cf, lp, caps, lw.SeedScopeOf, c.BranchPrefix)
	if err != nil {
		if len(res.Closed) > 0 {
			fmt.Fprintf(w, "reconcile: closed %d issue(s) before error: %s\n", len(res.Closed), strings.Join(res.Closed, ", "))
		}
		if len(res.Abandoned) > 0 {
			fmt.Fprintf(w, "reconcile: flagged %d issue(s) abandoned before error: %s\n", len(res.Abandoned), strings.Join(res.Abandoned, ", "))
		}
		if len(res.Reset) > 0 {
			fmt.Fprintf(w, "reconcile: reset %d issue(s) despite errors: %s\n", len(res.Reset), strings.Join(res.Reset, ", "))
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

// sweepLateMergesThrottled is the one throttled late-merge sweep, shared by
// reconcileAfterDispatch (outside the daemon) and the hidden daemon-run
// __late-merge-sweep verb: it claims the sweep for this
// settle.LateMergeSweepInterval across processes sharing pwd and records
// late merges only when the claim is won.
func sweepLateMergesThrottled(caps forge.Capabilities, drivers *driver.Set, pwd string, w io.Writer) error {
	if caps.PRForge == nil {
		return nil
	}
	ok, err := settle.ClaimLateMergeSweep(pwd, time.Now())
	if ok {
		err = recordLateMerges(caps, drivers, pwd, w)
	}
	return err
}

// reconcileAfterDispatch runs the sweep at the end of a dispatch run (ADR
// 0029). Unlike the standalone verb, it stays silent for a tracker that is
// not in-box-unreachable unless a late merge was recorded, and late-merge
// recording is best-effort: a failure only warns. The late-merge sweep is
// throttled to one per settle.LateMergeSweepInterval across processes sharing
// pwd; the verb's is not. A daemon pool child skips
// the sweep (the daemon runs it outside the pool's slots, issue #7342) but
// still runs the local-tracker issue bookkeeping.
func reconcileAfterDispatch(c config, it forge.IssueTracker, cf forge.CodeForge, lp reconcile.LivenessProbe, caps forge.Capabilities, pwd string, w io.Writer) error {
	if !daemon.InPoolChild() {
		if err := sweepLateMergesThrottled(caps, c.connectors.Drivers(), pwd, w); err != nil {
			fmt.Fprintf(w, "    ?? late merges: %v\n", err)
		}
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
func cmdReconcile(connectors *connectorruntime.Runtime, stdout, stderr io.Writer) int {
	return withRepoReadContext(connectors, stderr, func(rc readContext, pwd string) error {
		lp, err := rc.reconcileLivenessProbe(pwd)
		if err != nil {
			return err
		}
		return runReconcile(rc.config, rc.issueTracker, rc.codeForge, lp, rc.capabilities, pwd, stdout)
	})
}

// withRepoReadContext runs a read-only command body against the read context
// and the working directory, owning the setup and the 0/1 exit mapping the
// reconcile commands share.
func withRepoReadContext(connectors *connectorruntime.Runtime, stderr io.Writer, run func(rc readContext, pwd string) error) int {
	// These commands never dispatch, so they carry no dispatch kind (issue #2944).
	rc, err := newReadContext(connectors, nil, false)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}

	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	if err := run(rc, pwd); err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	return 0
}

// cmdLateMergeSweep is the hidden daemon.LateMergeSweepVerb the daemon execs
// for the late-merge sweep, which pool children skip (issue #7342). Like
// cmdReconcile it needs only the read context; it shares the throttle with
// reconcileAfterDispatch, so a stray extra invocation is cheap.
func cmdLateMergeSweep(connectors *connectorruntime.Runtime, stdout, stderr io.Writer) int {
	return withRepoReadContext(connectors, stderr, func(rc readContext, pwd string) error {
		return sweepLateMergesThrottled(rc.capabilities, connectors.Drivers(), pwd, stdout)
	})
}
