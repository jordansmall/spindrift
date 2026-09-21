package settle

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
)

// Settle interprets result and drives num to its terminal label, routing a
// parsed "ready" outcome to the self-heal merge gate. Called immediately after
// a Box exits so each issue settles independently of its wave siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	logRejectedSignals(num, result)
	if result.ParseErr != nil {
		// A malformed outcome line gets the same PR-adoption safety net as no
		// outcome line at all (issue #1898): the Box may still have landed a
		// real, open, green PR before mangling its last print, and ADR 0012
		// reserves agent-failed for "never produced a green PR".
		s.settleUnresolved(num, "", fmt.Sprintf("unparseable outcome line: %v", result.ParseErr))
		return
	}
	if !result.Resolved.Found {
		clsNote := ""
		if result.ClassifyErr != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: classify: %v\n", num, result.ClassifyErr)
		} else {
			clsNote = fmt.Sprintf("  class=%s  reason=%s", result.Classification.Class, result.Classification.Reason)
			if result.Classification.ResetAt != nil {
				clsNote += "  resetsAt=" + result.Classification.ResetAt.UTC().Format(time.RFC3339)
			}
		}
		// A read-only run's !Resolved.Found may just mean the Box was cut short
		// before it printed any parseable outcome line, leaving no ADR 0036
		// synthetic backstop line to key off (issue #2253), unlike the
		// "blocked" arm below.
		if s.tryAdoptRelayedBranchNoOutcome(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart to the adopt call above (ADR
		// 0039): local has no PR-shaped adopt path (s.pr is always nil), so the
		// call above always returns false here. tryMarkRecoverable checks the
		// same fingerprint but promotes to Recoverable, leaving the land itself
		// to `spindrift recover`.
		if s.tryMarkRecoverable(num, result) {
			return
		}
		s.settleUnresolved(num, clsNote, "no outcome in log")
		return
	}

	o := result.Resolved.Outcome
	s.recordLanding(num, o.Landing)
	s.recordLandingPass(num, o.Landing, result.Passes)
	// Ahead of the status switch so it runs on every outcome status alike
	// (issue #2019): a run's own findings are worth tracking whether it landed
	// ready or blocked. Best-effort, so a filing failure never changes the
	// switch's landing decision.
	fileIssueIntents(s.it, num, result, "agent-review-finding")
	switch o.Status {
	case outcome.StatusBlocked:
		// A read-only run's status=blocked may be the ADR 0036 synthetic
		// backstop over a Box cut short before its final print, not a genuine
		// "never finished" (issue #2224). tryAdoptRelayedBranch checks the
		// driver's last genuine self-report (issue #2223) and, if that holds
		// and a branch was relayable, opens a PR and merges it instead.
		if s.tryAdoptRelayedBranch(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart to the adopt call above (ADR
		// 0039). The ProvenanceSynthetic guard is repeated here rather than
		// left to tryMarkRecoverable because a genuine status=blocked is the
		// driver's own authoritative outcome line, not the backstop this
		// override exists to second-guess, so it must still park Failed below.
		if result.Resolved.Provenance == outcome.ProvenanceSynthetic && s.tryMarkRecoverable(num, result) {
			return
		}
		fmt.Printf("    #%s  landing=%s  status=%s  !! %s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Failed)
		// A read-only Box never pushes or opens a PR in-box (issue #1933), so a
		// bundle it wrote to the outbox and a PR-intent line it printed would
		// be stranded once the container exits. Applies to PR-shaped and
		// push-only forges alike (issue #1946). Best-effort and additive:
		// failure never changes the blocked outcome recorded above.
		if s.readOnly {
			s.relayBlockedWork(num, result)
		}
		s.postBlockedNoteComment(num, o.Note)
		s.postUsageComment(num, d)
	case outcome.StatusReady:
		pr := o.Landing
		// A read-only PR-shaped Code Forge never opens its own PR in-box (issue
		// #1919), so o.Landing carries the branch name, not a PR URL, and
		// selfHeal cannot watch CI on it. Push-only forges (s.pr == nil) need
		// no such step: landPushOnly's own RelayBundle call covers them.
		if s.readOnly && s.pr != nil {
			var ok bool
			pr, ok = s.hostMediateDraftPR(num, result)
			if !ok {
				s.postUsageComment(num, d)
				return
			}
			// Upgrade the placeholder branch-name landing recorded above to the
			// real PR URL. A no-op for every tracker but local's, and local
			// never reaches this branch (s.pr is nil for its push-only forge).
			s.recordLanding(num, pr)
		}
		landing, reason := s.selfHeal(d, num, gen, pr)
		switch landing {
		case landingMerged:
			// verifyMerged reads PR state, which a push-only Code Forge does
			// not have. pr, not o.Landing, so a host-mediated landing verifies
			// against the PR settle just created rather than the Box's
			// placeholder branch-name landing= value.
			if s.pr != nil {
				s.verifyMerged(num, pr)
			}
		case landingFailed:
			fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, pr, reason)
		case landingAbandoned:
			// Terminate already recorded its own comment and log line, so a
			// usage comment here would be noise.
			return
		}
		s.postUsageComment(num, d)
	case "merged":
		// status=merged is off-script: no prompt fragment instructs a Box to
		// print it, so o.Landing is Agent-controlled with no legitimate
		// provenance. It is absent from lib/prompt-contract.nix's
		// outcomeStatusSets (issue #2504), hence a bare string literal. Resolve
		// the ref host-side (issue #1955); PRForBranch gives a full PR URL.
		branch := s.cf.AgentBranch(num)
		if s.pr != nil {
			pr, ok, err := s.pr.PRForBranch(branch)
			if err != nil || !ok {
				fmt.Printf("    #%s  landing=%s  status=failed  !! no PR found on branch to verify merge\n", num, branch)
				s.transitionState(num, forge.InProgress, forge.Failed)
			} else {
				s.verifyMerged(num, pr)
			}
		} else {
			// A push-only Code Forge has no PR state to verify. Print the
			// host-derived branch, never the Agent-controlled o.Landing, so
			// landing= carries the same provenance across both arms.
			fmt.Printf("    #%s  landing=%s  status=%s\n", num, branch, o.Status)
		}
		s.postUsageComment(num, d)
	case outcome.StatusAmbiguous:
		// The Box detected an internally-contradictory issue and halted before
		// scouting (issue #2275). This is a successful, non-crash stop, so it
		// must never fall through to agent-failed. The Box has no fragment to
		// post this comment in-box, so settle always posts o.Note host-side,
		// unlike postBlockedNoteComment's landing/readOnly-gated relay.
		if o.Note != "" {
			if err := s.it.Comment(num, o.Note); err != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: could not post ambiguous-spec comment: %v\n", num, err)
			}
		}
		fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Ambiguous)
		s.postUsageComment(num, d)
	default:
		fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		s.postUsageComment(num, d)
	}
}

// logRejectedSignals warns about rejected lines a result channel dropped
// (issue #2976), naming the actual cause — nonce-mismatched, malformed, or
// both (issue #3670) — rather than always saying nonce-mismatched. An
// issue-intent rejection leaves no other trace, so it always warns. retry.go's
// own scan already warns when every comment or pr-intent line was rejected, so
// for those two this covers only the silent case: a verifying match alongside
// one or more rejections.
func logRejectedSignals(num string, result dispatch.Result) {
	if result.CommentFound && result.CommentRejected.Total() > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d %s comment line(s) rejected\n", num, result.CommentRejected.Total(), result.CommentRejected.Cause())
	}
	if result.PRIntentFound && result.PRIntentRejected.Total() > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d %s pr-intent line(s) rejected\n", num, result.PRIntentRejected.Total(), result.PRIntentRejected.Cause())
	}
	if result.IssueIntentsRejected.Total() > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d %s issue-intent line(s) rejected\n", num, result.IssueIntentsRejected.Total(), result.IssueIntentsRejected.Cause())
	}
}

// settleUnresolved is the shared safety net for a box result carrying no usable
// outcome line. clsNote is classification detail to log alongside a
// confirmed-missing PR, empty for the ParseErr case, which never classifies.
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
	// No transitionState here, on purpose, regardless of draft-ness (issue
	// #1654): an open PR is a real, if unmergeable-right-now, result, and ADR
	// 0012 reserves agent-failed for "never produced a green PR". A non-draft
	// PR only got that way via this launcher's own MarkReady at green (issue
	// #1651).
	fmt.Printf("    #%s  landing=%s  status=blocked  note=no outcome line; PR on %s left for manual adopt\n", num, res.URL, branch)
}

// transitionState is a best-effort dispatch-state transition that logs but does
// not propagate errors.
func (s *Settle) transitionState(num string, from, to forge.DispatchState) {
	if err := s.it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// postBlockedNoteComment posts note when the Box had no way to post it in-box:
// the local content plane (ADR 0032, issue #1692) or a read-only github/jira
// Box stripped of its write token (issue #1917). Best-effort, matching
// postUsageComment's log-but-don't-propagate contract.
func (s *Settle) postBlockedNoteComment(num, note string) {
	if (s.landing == nil && !s.readOnly) || note == "" {
		return
	}
	if err := s.it.Comment(num, note); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post blocked-note comment: %v\n", num, err)
	}
}

// recordLanding persists landing onto the tracker issue via the optional
// LandingRecorder interface (ADR 0029). An empty landing is a no-op: a blank
// write must never clear an already-recorded ref. Best-effort, matching
// transitionState's log-but-don't-propagate contract.
func (s *Settle) recordLanding(num, landing string) {
	if s.landing == nil || landing == "" {
		return
	}
	if err := s.landing.RecordLanding(num, landing); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not record landing: %v\n", num, err)
	}
}

// recordLandingPass persists which pass produced the outcome (issue #2983). An
// empty landing is a no-op, mirroring recordLanding: a blocked run with no
// landing must never overwrite the pass provenance an earlier genuine landing
// recorded. Picks the last entry with OutcomeFound true. Best-effort, matching
// transitionState's log-but-don't-propagate contract.
func (s *Settle) recordLandingPass(num, landing string, passes []passmanifest.Entry) {
	if s.landingPass == nil || landing == "" || len(passes) == 0 {
		return
	}
	entry := passes[len(passes)-1]
	for i := len(passes) - 1; i >= 0; i-- {
		if passes[i].OutcomeFound {
			entry = passes[i]
			break
		}
	}
	if err := s.landingPass.RecordLandingPass(num, entry.Pass, entry.Kind); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not record landing pass: %v\n", num, err)
	}
}

// closeIssue closes num through the tracker's optional MergeCloser (issue
// #1892), a backstop for github's merged-PR auto-close, which only fires when
// the PR body carries a literal Closes #<N>. MergeCloser rather than
// IssueCloser keeps this a no-op for local, whose closed: axis is reconcile's
// sole write path (ADR 0029), even paired with a github Code Forge.
func (s *Settle) closeIssue(num string) {
	closer, ok := s.it.(forge.MergeCloser)
	if !ok {
		return
	}
	if err := closer.CloseMergedIssue(num); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not close issue: %v\n", num, err)
	}
}
