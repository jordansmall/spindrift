package settle

import (
	"fmt"
	"os"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/report"
)

// Settle interprets result and drives num to its terminal label, routing a
// parsed "ready" outcome to the self-heal merge gate. Called immediately after
// a Box exits so each issue settles independently of its wave siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	defer s.flushSettled(d, num)
	RecordSettleWarnings(d, num, "", result)
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
	filed := fileIssueIntentsDetailed(s.it, num, result, dispatchkind.Work.FindingLabel, "")
	reportFiled(num, filed)
	// Placed and best-effort for the same reasons as reportFiled above; why the
	// work path needs its own post is on postSkippedComment (issue #3811).
	postSkippedComment(s.it, num, filed)

	// An already-resolved claim with commits behind it must not close the
	// issue (issue #4016, ADR 0039). The Box's harness demotes one it can
	// count commits for to a synthetic blocked line; an outbox bundle is the
	// launcher's own evidence of commits the claim still stands over.
	demotedResolved := o.Status == outcome.StatusBlocked && result.Resolved.Provenance == outcome.ProvenanceSynthetic &&
		result.Resolved.SelfReportFound && result.Resolved.SelfReport.Status == outcome.StatusAlreadyResolved
	if o.Status == outcome.StatusAlreadyResolved && s.bundlePresent(num) {
		o.Status = outcome.StatusBlocked
		o.Note = fmt.Sprintf("agent reported already-resolved but a bundle of commits exists for %s", s.cf.AgentBranch(num))
		demotedResolved = true
	}

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
		s.transitionState(num, forge.InProgress, forge.Failed, o.Note, ReasonBlocked)
		// A read-only Box never pushes or opens a PR in-box (issue #1933), so a
		// bundle it wrote to the outbox and a PR-intent line it printed would
		// be stranded once the container exits. Applies to PR-shaped and
		// push-only forges alike (issue #1946). Best-effort and additive:
		// failure never changes the blocked outcome recorded above.
		if s.readOnly && s.relayBlockedWork(num, gen, result) {
			return
		}
		// A read-write Box may have opened its draft PR before stopping.
		s.latchBranchPR(num)
		if demotedResolved {
			// postBlockedNoteComment skips read-write on the assumption the
			// Box commented itself, but an agent that believed it was done
			// never did.
			if err := s.it.Comment(num, o.Note); err != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: could not post already-resolved-demoted comment: %v\n", num, err)
			}
		} else {
			s.postBlockedNoteComment(num, o.Note)
		}
		s.postUsageComment(num, d)
	case outcome.StatusReady:
		pr := o.Landing
		// A read-only PR-shaped Code Forge never opens its own PR in-box (issue
		// #1919), so o.Landing carries the branch name, not a PR URL, and
		// selfHeal cannot watch CI on it. Push-only forges (s.pr == nil) need
		// no such step: landPushOnly's own RelayBundle call covers them.
		if s.readOnly && s.pr != nil {
			var handoff handoffResult
			pr, handoff = s.hostMediateDraftPR(num, gen, result)
			switch handoff {
			case handoffAbandoned:
				return
			case handoffBlocked:
				s.postUsageComment(num, d)
				return
			}
			// Upgrade the placeholder branch-name landing recorded above to the
			// real PR URL. A no-op for every tracker but local's, and local
			// never reaches this branch (s.pr is nil for its push-only forge).
			s.recordLanding(num, pr)
		}
		// A read-write Box opened its PR before printing ready.
		s.latchBranchPR(num)
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
			// See landingAbandoned: another actor owns the issue, so a usage
			// comment here would be noise.
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
				const reason = "no PR found on branch to verify merge"
				fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, branch, reason)
				s.transitionState(num, forge.InProgress, forge.Failed, reason, ReasonFailed)
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
		s.transitionState(num, forge.InProgress, forge.Ambiguous, o.Note, ReasonAmbiguous)
		s.postUsageComment(num, d)
	case outcome.StatusAlreadyResolved:
		// The Box found the change already on the default branch: zero
		// commits and no PR, yet a success (issue #4015). No PR carries a
		// Closes #<N> here, so settle closes the issue itself.
		msg := "Closing this issue as completed: the work it asks for is already complete on the default branch."
		if o.Note != "" {
			msg += "\n\n" + o.Note
		}
		if err := s.it.Comment(num, msg); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not post already-resolved comment: %v\n", num, err)
		}
		fmt.Printf("    #%s  landing=%s  status=%s  note=%s\n", num, o.Landing, o.Status, o.Note)
		s.transitionState(num, forge.InProgress, forge.Complete, o.Note, ReasonAlreadyResolved)
		s.closeResolvedIssue(num)
		s.postUsageComment(num, d)
	default:
		fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		s.postUsageComment(num, d)
	}
}

// RecordSettleWarnings is the one seam that prints a settle's rejection
// warnings to stderr and records result.Warnings plus those rejections to d's
// sidecar (issue #3744). tag prefixes every entry and stderr line ("" for the
// initial settle, "fix pass N: " for a fix pass); an empty fix-pass batch
// records nothing, so a clean pass cannot delete an earlier run's file.
//
// An issue-intent rejection leaves no other trace, so it always warns.
// retry.go's own scan already warns when every comment or pr-intent line was
// rejected, so for those two this covers only a verifying match alongside
// rejections.
func RecordSettleWarnings(d dispatch.Dispatcher, num, tag string, result dispatch.Result) {
	var rejections []string
	if result.CommentFound {
		rejections = appendRejection(rejections, "comment", result.CommentRejected)
	}
	if result.PRIntentFound {
		rejections = appendRejection(rejections, "pr-intent", result.PRIntentRejected)
	}
	rejections = appendRejection(rejections, "issue-intent", result.IssueIntentsRejected)

	batch := slices.Concat(result.Warnings, rejections)
	if tag != "" {
		for i, w := range batch {
			batch[i] = tag + w
		}
	}
	for _, w := range rejections {
		dispatch.PrintWarning(num, tag+w)
	}
	if tag != "" && len(batch) == 0 {
		return
	}
	d.RecordWarnings(batch)
}

// appendRejection adds a warning naming the actual cause -- nonce-mismatched,
// malformed, or both (issues #2976, #3670) -- when r holds any rejection.
func appendRejection(warnings []string, channel string, r outcome.Rejections) []string {
	if r.Total() == 0 {
		return warnings
	}
	return append(warnings, fmt.Sprintf("%d %s %s line(s) rejected", r.Total(), r.Cause(), channel))
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
		// Same detail the log line above prints, folded into one note (issue
		// #3627) so a settled record is diagnosable on disk without the
		// daemon's terminal.
		note := missingNote + clsNote
		s.transitionState(num, forge.InProgress, forge.Failed, note, ReasonMissing)
		return
	}
	// No transitionState here, on purpose, regardless of draft-ness (issue
	// #1654): an open PR is a real, if unmergeable-right-now, result, and ADR
	// 0012 reserves agent-failed for "never produced a green PR". A non-draft
	// PR only got that way via this launcher's own MarkReady at green (issue
	// #1651).
	fmt.Printf("    #%s  landing=%s  status=blocked  note=no outcome line; PR on %s left for manual adopt\n", num, res.URL, branch)
}

// settledLatch is one issue's most recently latched terminal decision,
// awaiting flushSettled (issue #3627).
type settledLatch struct {
	state string
	// reason is the status= class of the path that latched, the vocabulary the
	// path already prints (fix-exhausted, ci-red, merge-guard-hit, ...).
	reason string
	note   string
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors. note is the most specific reason live at the
// call site, "" where none exists; reason is that site's status= class — see
// flushSettled for why this only latches rather than emits.
func (s *Settle) transitionState(num string, from, to forge.DispatchState, note, reason string) {
	// An unclaimed issue (issue #4076) never went agent-in-progress, so there
	// is nothing to leave and no agent-failed to apply here — only a real
	// merge (Complete, below) commits tracker state.
	if s.cfg.Unclaimed && to != forge.Complete {
		return
	}
	if err := s.it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %s\n", num, to)
	}
	if !to.Terminal() {
		return
	}
	// Latched, not emitted, here (issue #3627): one issue's settle path can
	// reach a second, contradicting terminal transition on the same run — a
	// green completeLanding (Complete) that verifyMerged then demotes to
	// Failed after re-checking PR state (adopt.go) — so emitting per call
	// would let a stale Complete race a real Failed to the daemon. Last write
	// wins: it is always the tracker's own final decision, checked in
	// call order. The mutex exists because dispatchWave goroutines settle
	// different issues on the same *Settle concurrently.
	s.settledMu.Lock()
	if s.settledLatch == nil {
		s.settledLatch = make(map[string]settledLatch)
	}
	s.settledLatch[num] = settledLatch{state: to.String(), reason: reason, note: note}
	s.settledMu.Unlock()
}

// relatchReason replaces the reason of num's already-latched terminal decision.
// landPushOnly commits Complete before its merge runs, so a merge that then
// fails corrects the class here. A no-op when nothing is latched.
func (s *Settle) relatchReason(num, reason string) {
	s.settledMu.Lock()
	defer s.settledMu.Unlock()
	if rec, ok := s.settledLatch[num]; ok {
		rec.reason = reason
		s.settledLatch[num] = rec
	}
}

// flushSettled emits num's latched terminal decision, if any, exactly once
// (issue #3627). Call it via defer at every entry point that can drive one
// issue to a terminal transitionState call, so an issue that never reaches a
// terminal state still produces no record, as before this latch existed.
// Emitting here, not in transitionState, means a path that latches twice
// (Complete demoted to Failed) still appends one dispatch_settled op.
func (s *Settle) flushSettled(d dispatch.Dispatcher, num string) {
	s.settledMu.Lock()
	rec, ok := s.settledLatch[num]
	pr := s.prLatch[num]
	delete(s.settledLatch, num)
	delete(s.prLatch, num)
	s.settledMu.Unlock()
	if !ok {
		return
	}
	// A nil Dispatcher (some callers hold none) has no Record.
	var recordID string
	if d != nil {
		recordID = d.RecordID()
	}
	var path string
	if s.cfg.LogPath != nil {
		path = s.cfg.LogPath(num)
	}
	Settled(dispatchkey.Issue(num), path, claude.DispatchSettled{RecordID: recordID, State: rec.state, Reason: rec.reason, Note: rec.note, PRURL: pr})
}

// Settled is the single terminal-record emitter for every settle path: it
// appends ds as a dispatch_settled op to the Dispatch's primary Pass log, then
// reports the settled record. A ds with no RecordID takes the one stamped at
// the head of logPath, which covers a Dispatch that never Ran (recover's) and
// so minted none of its own.
//
// The append is best-effort: it warns and never changes the settle outcome.
// The log is opened without O_CREATE: a Dispatch that wrote no primary log has
// no Record to settle, and a stub file would only fake one. With no log path or
// no Record ID nothing is appended.
func Settled(key dispatchkey.Key, logPath string, ds claude.DispatchSettled) {
	if ds.RecordID == "" && logPath != "" {
		ds.RecordID = dispatchrecord.StampRecordID(logPath)
	}
	if logPath != "" && ds.RecordID != "" {
		line := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchSettled, Settled: &ds})
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, err = f.WriteString(line)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? %s: could not append %s to %s: %v\n", key, claude.OpDispatchSettled, logPath, err)
		}
	}
	report.Settled(key, ds.State, ds.Note, ds.PRURL, ds.RecordID)
}

// latchPR records the PR num's gate is working, for flushSettled to name on
// the settled record. Only host-proven URLs latch, each at its source: Open's
// result, the agent branch's open PR, and SettleAdopted's prURL. The Box's own
// landing= is never latched. Latching is not tied to merge, so a red-CI or
// failed landing still names its PR.
func (s *Settle) latchPR(num, pr string) {
	s.settledMu.Lock()
	defer s.settledMu.Unlock()
	if s.prLatch == nil {
		s.prLatch = make(map[string]string)
	}
	s.prLatch[num] = pr
}

// latchBranchPR latches the open PR on num's agent branch. The branch is
// reused across retries, so only an open PR counts: an earlier run's closed PR
// must not be named on this run's record (ResolveOpenPR, unlike PRForBranch,
// ignores it). A no-op for read-only and push-only forges; read-only latches at
// Open's source.
func (s *Settle) latchBranchPR(num string) {
	if s.readOnly || s.pr == nil {
		return
	}
	res, err := forge.ResolveOpenPR(s.cf, num)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not resolve PR for settled record: %v\n", num, err)
		return
	}
	if res.Found {
		s.latchPR(num, res.URL)
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

// reportCloseErr logs a close failure; a failed close never fails settle.
func reportCloseErr(num string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not close issue: %v\n", num, err)
	}
}

// closeIssue closes num through the tracker's optional MergeCloser (issue
// #1892), a backstop for github's merged-PR auto-close, which only fires when
// the PR body carries a literal Closes #<N>. MergeCloser rather than
// IssueCloser keeps this a no-op for local, whose closed: axis only reconcile
// writes for landed work (ADR 0029), even paired with a github Code Forge.
// Reports whether the tracker implements MergeCloser.
func (s *Settle) closeIssue(num string) bool {
	closer, ok := s.it.(forge.MergeCloser)
	if !ok {
		return false
	}
	reportCloseErr(num, closer.CloseMergedIssue(num))
	return true
}

// closeResolvedIssue closes num for a status=already-resolved outcome (issue
// #4017), which has no PR and so no merge for closeIssue's MergeCloser path
// to key off. No PR and no merge means nothing landed for reconcile to
// observe either, so it would leave a local issue open forever; this is the
// one direct close outside ADR 0029's reconcile-only closed: axis. Prefers
// MergeCloser (forgejo, github) when present, falling back to IssueCloser
// (local) otherwise.
func (s *Settle) closeResolvedIssue(num string) {
	if s.closeIssue(num) {
		return
	}
	if closer, ok := s.it.(forge.IssueCloser); ok {
		reportCloseErr(num, closer.CloseIssue(num))
	}
}
