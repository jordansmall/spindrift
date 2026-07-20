// Package reconcile implements ADR 0029's reconcile sweep: the sole
// authority that closes a local issue, reflecting Code Forge reality (a
// merged landing PR) into the local issue's closed: axis. It is
// observational — it never lands code.
package reconcile

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// Result reports what a Run swept.
type Result struct {
	// Closed lists the issue numbers Run closed this sweep, in the order
	// ListOpenIssues returned them.
	Closed []string
	// Abandoned lists the issue numbers Run flagged abandoned this sweep —
	// their recorded landing PR was closed without merging.
	Abandoned []string
	// Reset lists the issue numbers Run reset from InProgress to
	// Dispatchable this sweep, in the order ListIssues returned them.
	Reset []string
}

// LivenessProbe is reconcile's injected death-signal seam (#600, ADR 0029):
// whether an InProgress issue's Box is still alive. Run never touches
// os.Stat or the container runtime itself — every liveness fact comes
// through this seam, so it is fakeable in tests.
type LivenessProbe interface {
	// LogStale reports whether issue num's Box log has gone stale beyond
	// reconcile's threshold — the log-side half of the death signal.
	LogStale(num string) bool
	// ContainerLive reports whether issue num's Box container/sandbox is
	// currently running. reachable is false when the container runtime
	// itself could not be queried (e.g. the runtime is unreachable
	// on-host); Run treats that as no evidence of a live container, not as
	// proof of one, so it never blocks a reset on an unreachable runtime.
	ContainerLive(num string) (live, reachable bool)
}

// Run sweeps every open issue it reports: an issue carrying a recorded
// landing whose PR (per cf's PRForge surface) has merged is closed; one with
// no landing, or whose landing PR is still open, is left untouched. Run
// never merges, opens, or pushes — cf is queried read-only and it is only
// ever transitioned to closed.
//
// Run is a no-op, not an error, when it has no IssueCloser surface (every
// tracker but local) or cf has no PRForge surface (the push-only git Code
// Forge) — there is nothing to check or nowhere to write in either case.
//
// After closing, Run sweeps every InProgress issue and resets it to
// Dispatchable when lp reports the composite death signal: no PR (in any
// state — open, closed, or merged) exists for its agent branch, its Box log
// is stale, and (when the container runtime is reachable) its Box container
// is absent. This qualifies #600: a bare InProgress label is never enough to
// reset on its own, only the composite evidence from lp is.
func Run(it forge.IssueTracker, cf forge.CodeForge, lp LivenessProbe) (Result, error) {
	closer, ok := it.(forge.IssueCloser)
	if !ok {
		return Result{}, nil
	}
	pr, ok := cf.(forge.PRForge)
	if !ok {
		return Result{}, nil
	}
	lr, _ := it.(forge.LandingRecorder)
	flagger, _ := it.(forge.AbandonedFlagger)

	issues, err := it.ListOpenIssues()
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: list open issues: %w", err)
	}

	var res Result
	for _, iss := range issues {
		landing := iss.Landing
		if landing == "" {
			if lr == nil {
				continue
			}
			url, found, err := pr.PRForBranch(cf.AgentBranch(iss.Number))
			if err != nil {
				return res, fmt.Errorf("reconcile issue %s: resolve branch PR: %w", iss.Number, err)
			}
			if !found {
				continue
			}
			if err := lr.RecordLanding(iss.Number, url); err != nil {
				return res, fmt.Errorf("reconcile issue %s: record landing: %w", iss.Number, err)
			}
			landing = url
		}
		state, err := pr.PRState(landing)
		if err != nil {
			return res, fmt.Errorf("reconcile issue %s: PR state for %s: %w", iss.Number, landing, err)
		}
		switch state {
		case forge.PRMerged:
			if err := closer.CloseIssue(iss.Number); err != nil {
				return res, fmt.Errorf("reconcile issue %s: close: %w", iss.Number, err)
			}
			res.Closed = append(res.Closed, iss.Number)
		case forge.PRClosed:
			if flagger == nil || iss.Abandoned {
				continue
			}
			if err := flagger.FlagAbandoned(iss.Number); err != nil {
				return res, fmt.Errorf("reconcile issue %s: flag abandoned: %w", iss.Number, err)
			}
			res.Abandoned = append(res.Abandoned, iss.Number)
		}
	}

	inProgress, err := it.ListIssues(forge.InProgress)
	if err != nil {
		return res, fmt.Errorf("reconcile: list in-progress issues: %w", err)
	}
	for _, iss := range inProgress {
		orphaned, err := isOrphaned(pr, cf, lp, iss.Number)
		if err != nil {
			return res, fmt.Errorf("reconcile issue %s: liveness check: %w", iss.Number, err)
		}
		if !orphaned {
			continue
		}
		if err := it.TransitionState(iss.Number, forge.InProgress, forge.Dispatchable); err != nil {
			return res, fmt.Errorf("reconcile issue %s: reset: %w", iss.Number, err)
		}
		res.Reset = append(res.Reset, iss.Number)
	}
	return res, nil
}

// isOrphaned reports whether num's InProgress issue shows the full
// composite death signal: no PR of any state for its agent branch, a stale
// Box log, and — only when the container runtime answered — no live
// container. A PR of any state (not just open/merged) counts as evidence a
// runner touched this branch, so a closed-unmerged PR withholds the reset
// rather than silently re-dispatching what a human or CI already rejected;
// flagging that case as abandoned is a separate reconcile concern.
func isOrphaned(pr forge.PRForge, cf forge.CodeForge, lp LivenessProbe, num string) (bool, error) {
	if _, found, err := pr.PRForBranch(cf.AgentBranch(num)); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if !lp.LogStale(num) {
		return false, nil
	}
	if live, reachable := lp.ContainerLive(num); reachable && live {
		return false, nil
	}
	return true, nil
}
