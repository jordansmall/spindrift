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

// Settle interprets result (a Dispatcher's Run outcome) and drives num to its
// terminal label: routing to the self-heal merge gate on a parsed "ready"
// outcome, or reporting blocked/missing/malformed otherwise, then posting
// the usage comment. Called immediately after a Box exits so each issue
// reaches CompleteLabel or its failed label independently of its wave
// siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	logRejectedSignals(num, result)
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
		// A read-only run's !Resolved.Found here may just mean the Box crashed
		// or was cut short before it ever printed a parseable outcome line
		// at all — no ADR 0036 synthetic backstop line to key off of, unlike
		// the "blocked" arm below (issue #2253). tryAdoptRelayedBranchNoOutcome
		// checks the same self-report fingerprint tryAdoptRelayedBranch does;
		// see its own doc comment for the full reasoning.
		if s.tryAdoptRelayedBranchNoOutcome(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart to the adopt call above
		// (ADR 0039): local has no PR-shaped adopt path at all (s.pr is
		// always nil for it), so tryAdoptRelayedBranchNoOutcome's own
		// s.pr != nil gate always returns false here — tryMarkRecoverable
		// checks the same self-report fingerprint but promotes to
		// Recoverable instead, leaving the land itself to `spindrift
		// recover`.
		if s.tryMarkRecoverable(num, result) {
			return
		}
		s.settleUnresolved(num, clsNote, "no outcome in log")
		return
	}

	o := result.Resolved.Outcome
	s.recordLanding(num, o.Landing)
	s.recordLandingPass(num, o.Landing, result.Passes)
	// Best-effort, ahead of the status switch so it runs on every outcome
	// status alike (issue #2019, wiring #2018's dormant fileIssueIntents into
	// this entry point): a run's own findings are worth tracking whether that
	// run itself landed ready or blocked, and a filing failure must never
	// change the switch below's own landing decision. Only reachable once a
	// SPINDRIFT_OUTCOME line was actually parsed (the ParseErr/!Resolved.Found
	// branches above both return first) -- a crashed or outcome-less run
	// never reaches FILE ISSUES in its own prompt either, so there is
	// nothing for this call to find in that case.
	fileIssueIntents(s.it, num, result, "agent-review-finding")
	switch o.Status {
	case outcome.StatusBlocked:
		// A read-only run's status=blocked here may just be the ADR 0036
		// synthetic backstop stitched in over a Box that crashed or was cut
		// short before its own final print — not a genuine "never
		// finished" (issue #2224). tryAdoptRelayedBranch checks the driver's
		// own last genuine self-report (Result.Resolved.SelfReport, issue #2223) for
		// evidence the run actually succeeded and, if the fingerprint holds
		// and a branch was actually relayable, opens a PR on the relayed
		// branch and drives the normal merge lifecycle in place of the
		// park-agent-failed path below. A false return means the
		// fingerprint didn't match (not synthetic, not read-only, no
		// self-report, or a self-report that itself isn't success) or
		// nothing was actually relayable (no bundle in the outbox) — in
		// either case the normal blocked handling below runs unchanged.
		if s.tryAdoptRelayedBranch(d, num, gen, result) {
			return
		}
		// CODE_FORGE=local push-only counterpart to the adopt call above
		// (ADR 0039): local has no PR-shaped adopt path at all, so
		// tryAdoptRelayedBranch's own s.pr != nil gate always returns false
		// here. The Resolved.Provenance == ProvenanceSynthetic guard is
		// repeated explicitly here (rather than left to tryMarkRecoverable)
		// because a genuine (non-synthetic) status=blocked is the driver's
		// own authoritative outcome line, not the ADR 0036 backstop this
		// override exists to second-guess — it must still park Failed below.
		if result.Resolved.Provenance == outcome.ProvenanceSynthetic && s.tryMarkRecoverable(num, result) {
			return
		}
		fmt.Printf("    #%s  landing=%s  status=%s  !! %s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Failed)
		// A read-only Box never pushes or opens a PR in-box (issue #1933,
		// same reasoning as the "ready" case's hostMediateDraftPR call
		// below): without this, a bundle it wrote to the outbox and a
		// PR-intent line it printed on its way to IF BLOCKED would
		// otherwise be silently stranded once the container exits. This
		// applies under any read-only Code Forge, PR-shaped or push-only
		// (issue #1946) -- relayBlockedWork's own cf.(forge.BundleRelay)/
		// cf.(forge.DraftPRCreator) assertions already decide what a given
		// forge supports. Best-effort and additive only -- unlike
		// hostMediateDraftPR, failure here never changes the blocked
		// outcome already recorded above.
		if s.readOnly {
			s.relayBlockedWork(num, result)
		}
		s.postBlockedNoteComment(num, o.Note)
		s.postUsageComment(num, d)
	case outcome.StatusReady:
		pr := o.Landing
		// A read-only PR-shaped Code Forge (github, issue #1919) never opens
		// its own PR in-box, so o.Landing carries the branch name, not a PR
		// URL — resolve the real one host-side before selfHeal can watch CI
		// on it. Push-only forges under read-only (s.pr == nil) need no such
		// step: landPushOnly's own RelayBundle call (ready.go) already
		// covers them.
		if s.readOnly && s.pr != nil {
			var ok bool
			pr, ok = s.hostMediateDraftPR(num, result)
			if !ok {
				s.postUsageComment(num, d)
				return
			}
			// Upgrade the placeholder branch-name landing recorded above to
			// the real PR URL just resolved — a no-op for every tracker but
			// local's (only implementor of LandingRecorder), and local never
			// reaches this branch (s.pr is nil for its push-only forge).
			s.recordLanding(num, pr)
		}
		landing, reason := s.selfHeal(d, num, gen, pr)
		switch landing {
		case landingMerged:
			// verifyMerged reads PR state, which a push-only Code Forge
			// does not have — landPushOnly's own cf.Merge success already
			// confirms the push landed, so there is nothing left to verify.
			// pr (not o.Landing) so a host-mediated landing verifies against
			// the PR settle itself just created, not the Box's placeholder
			// branch-name landing= value.
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
		// status=merged is off-script — no prompt fragment instructs a Box
		// to print it (issue-prompt.md documents only "ready"/"blocked"), so
		// o.Landing here carries Agent-controlled input with no legitimate
		// provenance. It is deliberately absent from lib/prompt-contract.nix's
		// outcomeStatusSets registry (issue #2504) and has no outcome.Status*
		// constant of its own -- unlike the typed cases above, this arm stays
		// a bare string literal because there is nothing to generate a
		// constant from. Resolve the ref verifyMerged reads host-side from the
		// Code Forge's own AgentBranch (issue #1955) rather than forwarding
		// o.Landing straight into a forge read — the same host-derived-ref
		// discipline #1949 gave the "ready" arm and settleUnresolved/
		// SettleAdopted already follow. PRForBranch resolves the branch to a
		// real PR URL, so verifyMerged's PRState call keeps its documented
		// full-URL contract (no bare-ref/--repo ambiguity).
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
			// verifyMerged reads PR state, which a push-only Code Forge does
			// not have — log the status line instead. Print the host-derived
			// branch, never the Agent-controlled o.Landing, so the landing=
			// label carries the same provenance-clean ref across both arms.
			fmt.Printf("    #%s  landing=%s  status=%s\n", num, branch, o.Status)
		}
		s.postUsageComment(num, d)
	case outcome.StatusAmbiguous:
		// The Box detected an internally-contradictory issue and halted
		// before scouting/implementing, per issue #2275 — this is a
		// successful, non-crash stop (mirrors agent-research-unclear), so it
		// must never fall through to agent-failed. The Box never posts this
		// comment itself in-box (no per-forge fragment for it, unlike IF
		// BLOCKED's in-box comment), so settle always posts o.Note as the
		// escalation comment host-side, unconditionally — unlike
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

// logRejectedSignals settle-logs a warning for a nonce-gated result channel
// (comment, PR-intent, issue-intent) that saw at least one nonce-mismatched
// line (issue #2976): a line carrying the right token but the wrong nonce is
// dropped rather than surfaced on the channel's own Found/value fields.
//
// The three channels differ in what other trace a rejection leaves.
// outcome.AllIssueIntentLinesInLog never returns an error for a rejected
// issue-intent line regardless of whether any other line on that channel
// verified, so this warning is the *only* trace of an issue-intent
// rejection and fires whenever IssueIntentsRejected > 0. Comment and
// PR-intent are different: dispatch.outcomeResult's own scan
// (outcome.LastCommentLineInLog / outcome.LastPRIntentInLog, see retry.go)
// already prints an incidental "comment scan"/"pr-intent scan" warning
// whenever *every* line on that channel was rejected (no verifying match,
// so Found stays false and the scan returns an error) -- warning here too
// would just duplicate it. This helper only adds coverage for those two
// channels in the case retry.go's scan leaves silent: a verifying match was
// found *and* one or more other lines on the same channel were rejected
// (Found true, Rejected > 0).
//
// Shared (rather than duplicated inline) so gate.go's work-path Settle and
// research.go's ResearchSettle.Settle both get it -- even though
// ResearchSettle never actually populates PRIntentRejected (it has no
// PR-intent scan of its own to reject from); checking it there costs
// nothing.
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

// recordLandingPass persists which pass produced the outcome, alongside
// recordLanding's own landing ref (issue #2983) — a no-op when s.landingPass
// is nil (every tracker but local), when landing is empty (mirroring
// recordLanding's own guard: a blocked run with no landing must never
// overwrite the pass provenance an earlier run's genuine landing recorded),
// or when passes is empty (no manifest evidence available for this run).
// Picks the LAST entry with OutcomeFound true (the pass whose own log the
// settled outcome was actually parsed from), falling back to the last entry
// overall if none has OutcomeFound set (e.g. a manifest present but the
// outcome came from the synthetic backstop tier, never a genuine in-pass
// marker) -- best-effort, log-but-don't-propagate, matching recordLanding's
// own contract.
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
