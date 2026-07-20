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
func Run(it forge.IssueTracker, cf forge.CodeForge) (Result, error) {
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
	return res, nil
}
