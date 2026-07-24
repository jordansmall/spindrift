package settle

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// Settle interprets result (a Dispatcher's Run outcome) and drives num to its
// terminal label: routing to the self-heal merge gate on a parsed "ready"
// outcome, or reporting blocked/missing/malformed otherwise, then posting
// the usage comment. Called immediately after a Box exits so each issue
// reaches CompleteLabel or its failed label independently of its wave
// siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	if result.ParseErr != nil {
		// A malformed outcome line gets the same PR-adoption safety net as
		// no outcome line at all (issue #1898): the box may still have
		// landed a real, open, green PR before mangling its last print —
		// that PR is no less real for the line above it being unparseable,
		// and ADR 0012 reserves agent-failed for "never produced a green
		// PR," not "produced one but said so badly."
		s.settleUnresolved(num, "", fmt.Sprintf("unparseable outcome line: %v", result.ParseErr))
		return
	}
	if !result.OutcomeFound {
		clsNote := ""
		if result.ClassifyErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: classify: %v\n", num, result.ClassifyErr)
		} else {
			clsNote = fmt.Sprintf("  class=%s  reason=%s", result.Classification.Class, result.Classification.Reason)
			if result.Classification.ResetAt != nil {
				clsNote += "  resetsAt=" + result.Classification.ResetAt.UTC().Format(time.RFC3339)
			}
		}
		s.settleUnresolved(num, clsNote, "no outcome in log")
		return
	}

	o := result.Outcome
	s.recordLanding(num, o.Landing)
	switch o.Status {
	case "blocked":
		fmt.Printf("    #%s  landing=%s  status=%s  !! %s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Failed)
		s.postBlockedNoteComment(num, o.Note)
		s.postUsageComment(num, d)
	case "ready":
		switch s.selfHeal(d, num, gen, o.Landing) {
		case landingMerged:
			// verifyMerged reads PR state, which a push-only Code Forge
			// does not have — landPushOnly's own cf.Merge success already
			// confirms the push landed, so there is nothing left to verify.
			if s.pr != nil {
				s.verifyMerged(num, o.Landing)
			}
		case landingFailed:
			fmt.Printf("    #%s  landing=%s  status=failed  !! CI or merge failed\n", num, o.Landing)
		case landingAbandoned:
			// Terminate already recorded its own comment and log line; a
			// usage comment here would be noise on an issue it reclaimed.
			return
		}
		s.postUsageComment(num, d)
	case "merged":
		// verifyMerged reads PR state, which a push-only Code Forge does
		// not have — unlike the "ready" case above, this branch logs a
		// status line via the else when s.pr is nil.
		if s.pr != nil {
			s.verifyMerged(num, o.Landing)
		} else {
			fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		}
		s.postUsageComment(num, d)
	default:
		fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		s.postUsageComment(num, d)
	}
}

// settleUnresolved is the shared safety net for a box result that carries no
// usable outcome line — either an unparseable SPINDRIFT_OUTCOME (ParseErr)
// or none at all (!OutcomeFound). clsNote is classification detail to log
// alongside a confirmed-missing PR (empty for the ParseErr case, which never
// attempts classification); missingNote explains why no outcome was usable.
func (s *Settle) settleUnresolved(num, clsNote, missingNote string) {
	branch := s.cf.AgentBranch(num)

	res, prErr := forge.ResolveOpenPR(s.cf, num)
	if prErr != nil {
		fmt.Printf("    #%s  status=missing%s  note=PR lookup failed: %v\n", num, clsNote, prErr)
		return
	}
	if !res.Found {
		fmt.Printf("    #%s  status=missing%s  note=%s\n", num, clsNote, missingNote)
		s.transitionState(num, forge.InProgress, forge.Failed)
		return
	}
	// No transitionState here, on purpose, regardless of draft-ness
	// (issue #1654 folded the non-draft case into this same branch): an
	// open PR — draft or not — is a real, if unmergeable-right-now,
	// result, and ADR 0012 reserves agent-failed for "never produced a
	// green PR." A non-draft PR only ever got that way via this
	// launcher's own MarkReady at green (issue #1651), so if anything
	// it is *more* likely to have gone green than a draft one — never
	// less deserving of the same restraint.
	fmt.Printf("    #%s  landing=%s  status=blocked  note=no outcome line; PR on %s left for manual adopt\n", num, res.URL, branch)
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the launcher's original behaviour.
func (s *Settle) transitionState(num string, from, to forge.DispatchState) {
	if err := s.it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// postBlockedNoteComment posts note as a comment when s.landing != nil
// (local's LandingRecorder shape) or s.readOnly (Config.ReadOnly) is true —
// a no-op otherwise, or when note is empty. Best-effort, matching
// postUsageComment's log-but-don't-propagate contract.
//
// Both conditions mean the same thing: the Box's issue-prompt has no way to
// post the blocked-note comment in-box, so settle posts it host-side
// instead — via the optional local content plane (ADR 0032, issue #1692) or,
// under BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917), the equivalent
// relay for a github/jira Box stripped of its write token. This Go-level
// gate is tracker-shape-agnostic (readOnly fires for github and jira alike);
// the entrypoint's prompt-fragment selection (lib/fragments.nix,
// agent/entrypoint.sh) folds jira into the same ISSUE_TRACKER_GITHUB(_
// READONLY) gate github uses, since jira shares github's in-box
// reachability.
func (s *Settle) postBlockedNoteComment(num, note string) {
	if (s.landing == nil && !s.readOnly) || note == "" {
		return
	}
	if err := s.it.Comment(num, note); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post blocked-note comment: %v\n", num, err)
	}
}

// recordLanding persists landing onto the tracker issue via the optional
// LandingRecorder surface (ADR 0029) once a work outcome line is parsed, so
// a later reconcile has a pointer to check without re-deriving it. A no-op
// for a tracker that doesn't implement it (github, jira), or when landing is
// empty — outcome.Parse never yields that today, but a blank write must
// never clear an already-recorded ref (only cf's own SPINDRIFT_OUTCOME line
// is meant to update it). Best-effort on a tracker that does implement it,
// matching transitionState's log-but-don't-propagate contract.
func (s *Settle) recordLanding(num, landing string) {
	if s.landing == nil || landing == "" {
		return
	}
	if err := s.landing.RecordLanding(num, landing); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not record landing: %v\n", num, err)
	}
}

// closeIssue closes num through the tracker's optional MergeCloser surface
// (issue #1892) once verifyMerged has confirmed a genuine merge — a
// deterministic backstop for github's own merged-PR auto-close, which only
// fires when the agent's PR body happens to carry a literal Closes #<N>
// keyword. A no-op for a tracker that doesn't implement it: jira, and local
// too (local's closed: axis is reconcile's sole write path, ADR 0029) — the
// distinct MergeCloser surface, rather than reusing IssueCloser, is what
// keeps this a no-op for local even when it's paired with a PRForge-backed
// Code Forge (ISSUE_TRACKER=local + CODE_FORGE=github is a valid independent
// combination, main.go's newIssueTracker/newCodeForge). Best-effort,
// matching transitionState's log-but-don't-propagate contract.
func (s *Settle) closeIssue(num string) {
	closer, ok := s.it.(forge.MergeCloser)
	if !ok {
		return
	}
	if err := closer.CloseMergedIssue(num); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not close issue: %v\n", num, err)
	}
}
