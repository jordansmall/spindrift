package settle

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// Settle interprets result (a Dispatcher's Run outcome) and drives num to its
// terminal label: routing to the self-heal merge gate on a parsed "ready"
// outcome, or reporting blocked/missing/malformed otherwise, then posting
// the usage comment. Called immediately after a Box exits so each issue
// reaches CompleteLabel or its failed label independently of its wave
// siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	logRejectedSignals(num, result)
	if result.ParseErr != nil {
		// A malformed outcome line gets the same PR-adoption safety net as no
		// outcome line at all: the box may still have landed a real, open,
		// green PR before mangling its last print, and ADR 0012 reserves
		// agent-failed for "never produced a green PR," not "produced one but
		// said so badly."
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
		// A read-only run's !Resolved.Found may just mean the Box crashed
		// before printing any parseable outcome line — with no ADR 0036
		// synthetic backstop to key off of, unlike the "blocked" arm below.
		if s.tryAdoptRelayedBranchNoOutcome(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart (ADR 0039): local has no
		// PR-shaped adopt path (s.pr is always nil), so the call above always
		// returns false here. tryMarkRecoverable checks the same self-report
		// fingerprint but promotes to Recoverable, leaving the land itself to
		// `spindrift recover`.
		if s.tryMarkRecoverable(num, result) {
			return
		}
		s.settleUnresolved(num, clsNote, "no outcome in log")
		return
	}

	o := result.Resolved.Outcome
	s.recordLanding(num, o.Landing)
	// Best-effort, ahead of the status switch so it runs on every outcome
	// status alike: a run's findings are worth tracking whether it landed
	// ready or blocked, and a filing failure must never change the switch
	// below's landing decision.
	fileIssueIntents(s.it, num, result, "agent-review-finding")
	switch o.Status {
	case outcome.StatusBlocked:
		// A read-only run's status=blocked may just be the ADR 0036 synthetic
		// backstop stitched in over a Box that crashed before its final
		// print, not a genuine "never finished". tryAdoptRelayedBranch looks
		// at the driver's last genuine self-report for evidence the run
		// actually succeeded and, if so and a branch was relayable, opens a
		// PR and drives the normal merge lifecycle instead of parking
		// agent-failed below.
		if s.tryAdoptRelayedBranch(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart (ADR 0039). The synthetic
		// guard is repeated explicitly here rather than left to
		// tryMarkRecoverable: a genuine status=blocked is the driver's own
		// authoritative outcome, not the backstop this override exists to
		// second-guess, so it must still park Failed below.
		if result.Resolved.Provenance == outcome.ProvenanceSynthetic && s.tryMarkRecoverable(num, result) {
			return
		}
		fmt.Printf("    #%s  landing=%s  status=%s  !! %s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Failed)
		// A read-only Box never pushes or opens a PR in-box, so without this
		// a bundle it wrote to the outbox and a PR-intent line it printed on
		// its way to IF BLOCKED would be silently stranded once the container
		// exits. Best-effort and additive only: unlike hostMediateDraftPR,
		// failure here never changes the blocked outcome recorded above.
		if s.readOnly {
			s.relayBlockedWork(num, result)
		}
		s.postBlockedNoteComment(num, o.Note)
		s.postUsageComment(num, d)
	case outcome.StatusReady:
		pr := o.Landing
		// A read-only PR-shaped Code Forge never opens its own PR in-box, so
		// o.Landing carries the branch name, not a PR URL — resolve the real
		// one host-side before selfHeal can watch CI on it. Push-only forges
		// under read-only (s.pr == nil) are already covered by landPushOnly's
		// RelayBundle call.
		if s.readOnly && s.pr != nil {
			var ok bool
			pr, ok = s.hostMediateDraftPR(num, result)
			if !ok {
				s.postUsageComment(num, d)
				return
			}
			// Upgrade the placeholder branch-name landing recorded above to
			// the real PR URL.
			s.recordLanding(num, pr)
		}
		landing, reason := s.selfHeal(d, num, gen, pr)
		switch landing {
		case landingMerged:
			// verifyMerged reads PR state, which a push-only Code Forge does
			// not have — landPushOnly's cf.Merge success already confirms the
			// push landed. pr, not o.Landing, so a host-mediated landing
			// verifies against the PR settle just created rather than the
			// Box's placeholder branch-name landing= value.
			if s.pr != nil {
				s.verifyMerged(num, pr)
			}
		case landingFailed:
			fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, pr, reason)
		case landingAbandoned:
			// Terminate already recorded its own comment and log line; a
			// usage comment here would be noise on an issue it reclaimed.
			return
		}
		s.postUsageComment(num, d)
	case "merged":
		// status=merged is off-script — no prompt fragment instructs a Box to
		// print it — so o.Landing here is Agent-controlled input with no
		// legitimate provenance. It is deliberately absent from
		// lib/prompt-contract.nix's outcomeStatusSets registry and has no
		// outcome.Status* constant, which is why this arm alone is a bare
		// string literal. Resolve the ref host-side from AgentBranch rather
		// than forwarding o.Landing into a forge read; PRForBranch then gives
		// verifyMerged a real PR URL, keeping its full-URL contract.
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
			// No PR state to read on a push-only forge, so log instead. Print
			// the host-derived branch, never the Agent-controlled o.Landing,
			// so landing= stays provenance-clean across both arms.
			fmt.Printf("    #%s  landing=%s  status=%s\n", num, branch, o.Status)
		}
		s.postUsageComment(num, d)
	case outcome.StatusAmbiguous:
		// The Box detected an internally-contradictory issue and halted
		// before scouting/implementing. This is a successful, non-crash stop
		// (mirroring agent-research-unclear), so it must never fall through
		// to agent-failed. The Box never posts this comment in-box, so settle
		// always posts o.Note host-side, unconditionally — unlike
		// postBlockedNoteComment's landing/readOnly-gated relay.
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

// logRejectedSignals warns about nonce-gated result channels (comment,
// PR-intent, issue-intent) that saw at least one nonce-mismatched line: a
// line carrying the right token but the wrong nonce is dropped rather than
// surfaced on the channel's Found/value fields.
//
// The Found guards on comment and PR-intent avoid duplicating a warning
// retry.go's own scan already prints when *every* line on those channels was
// rejected; this only covers the case that scan leaves silent (a verifying
// match plus other rejected lines). Issue-intent has no such scan warning, so
// it fires unconditionally.
func logRejectedSignals(num string, result dispatch.Result) {
	if result.CommentFound && result.CommentRejected > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d nonce-mismatched comment line(s) rejected\n", num, result.CommentRejected)
	}
	if result.PRIntentFound && result.PRIntentRejected > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d nonce-mismatched pr-intent line(s) rejected\n", num, result.PRIntentRejected)
	}
	if result.IssueIntentsRejected > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: %d nonce-mismatched issue-intent line(s) rejected\n", num, result.IssueIntentsRejected)
	}
}

// settleUnresolved is the shared safety net for a box result that carries no
// usable outcome line — either an unparseable SPINDRIFT_OUTCOME (ParseErr)
// or none at all (!Resolved.Found). clsNote is classification detail to log
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
	// No transitionState here, on purpose, regardless of draft-ness: an open
	// PR is a real, if unmergeable-right-now, result, and ADR 0012 reserves
	// agent-failed for "never produced a green PR." A non-draft PR only got
	// that way via this launcher's own MarkReady at green, so it is if
	// anything more likely to have gone green than a draft one.
	fmt.Printf("    #%s  landing=%s  status=blocked  note=no outcome line; PR on %s left for manual adopt\n", num, res.URL, branch)
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the launcher's original behaviour.
func (s *Settle) transitionState(num string, from, to forge.DispatchState) {
	if err := s.it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// postBlockedNoteComment posts note as a comment when s.landing != nil or
// s.readOnly is true — a no-op otherwise, or when note is empty. Best-effort,
// matching postUsageComment's log-but-don't-propagate contract.
//
// Both conditions mean the same thing: the Box has no way to post the
// blocked-note comment in-box, so settle relays it host-side — via the local
// content plane (ADR 0032) or, under BOX_FORGE_AND_ISSUE_ACCESS=read-only,
// for a github/jira Box stripped of its write token.
func (s *Settle) postBlockedNoteComment(num, note string) {
	if (s.landing == nil && !s.readOnly) || note == "" {
		return
	}
	if err := s.it.Comment(num, note); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post blocked-note comment: %v\n", num, err)
	}
}

// recordLanding persists landing onto the tracker issue via the optional
// LandingRecorder surface (ADR 0029), so a later reconcile has a pointer to
// check without re-deriving it. A no-op for a tracker that doesn't implement
// it, or when landing is empty — a blank write must never clear an
// already-recorded ref. Best-effort, matching transitionState's
// log-but-don't-propagate contract.
func (s *Settle) recordLanding(num, landing string) {
	if s.landing == nil || landing == "" {
		return
	}
	if err := s.landing.RecordLanding(num, landing); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not record landing: %v\n", num, err)
	}
}

// closeIssue closes num through the tracker's optional MergeCloser surface
// once verifyMerged has confirmed a genuine merge — a deterministic backstop
// for github's own merged-PR auto-close, which only fires when the agent's PR
// body happens to carry a literal Closes #<N> keyword. A no-op for a tracker
// that doesn't implement it: jira, and local too (local's closed: axis is
// reconcile's sole write path, ADR 0029). MergeCloser is a distinct surface
// rather than a reuse of IssueCloser precisely so this stays a no-op for
// local even when local is paired with a PRForge-backed Code Forge — a valid
// independent combination. Best-effort, matching transitionState's
// log-but-don't-propagate contract.
func (s *Settle) closeIssue(num string) {
	closer, ok := s.it.(forge.MergeCloser)
	if !ok {
		return
	}
	if err := closer.CloseMergedIssue(num); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not close issue: %v\n", num, err)
	}
}
