package terminate

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/forge"
)

// Reaper is the Box-side half of Reclaim: dispatch.Factory satisfies it. It
// stays local so terminate need not import dispatch, which would drag the
// runner, driver, and registry-proxy trees into every importer.
type Reaper interface {
	Kill(number string) error
	AppendTerminalLine(number, note string) error
}

// Trigger is what called Reclaim. It picks the wording of the Box log line
// and the issue comment, so neither names a trigger that never
// happened (issue #3559).
type Trigger int

const (
	// SignalAbort is the second SIGTERM/SIGINT reaping Boxes still in flight.
	// It starts at iota+1 so an unset Trigger gets neutral wording, never a
	// real trigger's.
	SignalAbort Trigger = iota + 1
	// DrainDecline is reached on a shutdown (either signal) declining a
	// launch and releasing the held claim before any Box launched, so
	// nothing was terminated.
	DrainDecline
)

// CommentSuffix closes every Reclaim comment's trigger wording, so
// callers' tests can match a reclaim under any trigger.
const CommentSuffix = "to Dispatchable."

// wording is a trigger's Box log line and issue comment, resolved in one
// switch so the two cannot drift apart.
type wording struct {
	terminalLine string
	comment      string
}

func (t Trigger) wording() wording {
	switch t {
	case SignalAbort:
		return wording{
			terminalLine: "aborted on signal; issue returned to Dispatchable",
			comment:      "Aborted on signal: reclaimed back " + CommentSuffix,
		}
	case DrainDecline:
		return wording{
			terminalLine: "released on shutdown; issue returned to Dispatchable",
			comment:      "Released on shutdown, never launched: returned " + CommentSuffix,
		}
	default:
		return wording{
			terminalLine: "reclaimed; issue returned to Dispatchable",
			comment:      "Reclaimed back " + CommentSuffix,
		}
	}
}

// Reclaim ends num's live Dispatch (ADR 0024, issues #649 and #3519), worded
// for trigger: it marks reg so an in-flight settle abandons at its next
// checkpoint, reaps any running Box via reaper, resolves a dangling branch/PR
// note, transitions the issue back to Dispatchable (never Failed, since
// nothing failed), and comments. Every step is best-effort and logs to
// stderr; only the reap's error is returned. Pass a nil interface for an
// absent reaper or cf, never a typed nil boxed into one, or the nil guards
// will not see it. branchPrefix is BRANCH_PREFIX.
func Reclaim(tracker forge.IssueTracker, cf forge.CodeForge, branchPrefix string, reaper Reaper, reg *Registry, num string, trigger Trigger) error {
	reg.Mark(num)

	var killErr error
	if reaper != nil {
		killErr = reaper.Kill(num)
		if killErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: kill: %v\n", num, killErr)
		}
		if err := reaper.AppendTerminalLine(num, trigger.wording().terminalLine); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: append log line: %v\n", num, err)
		}
	}

	danglingNote := "no open branch/PR found"
	if cf != nil {
		branch := forge.AgentBranchName(branchPrefix, num)
		if res, err := forge.ResolveOpenPR(cf, branch); err == nil && res.Found {
			danglingNote = res.URL
		} else if branch != "" {
			danglingNote = fmt.Sprintf("no open PR found; branch=%s", branch)
		}
	}

	// The issue's current label depends on which phase Reclaim caught:
	// InProgress for a running Box, a CI watch, or anywhere on the landing path,
	// since selfHeal holds the swap to Complete until landing settles (issue
	// #757, settle/ready.go); Complete if Reclaim lands just after settling.
	// TransitionState has no compare-and-swap, so both calls run regardless.
	if err := tracker.TransitionState(num, forge.InProgress, forge.Dispatchable); err != nil {
		forge.CommentRefusal(tracker, num, err)
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: transition to Dispatchable: %v\n", num, err)
	}
	if err := tracker.TransitionState(num, forge.Complete, forge.Dispatchable); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: clear Complete: %v\n", num, err)
	}
	comment := fmt.Sprintf("%s %s", trigger.wording().comment, danglingNote)
	if err := tracker.Comment(num, comment); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: terminate: post comment: %v\n", num, err)
	}

	return killErr
}
