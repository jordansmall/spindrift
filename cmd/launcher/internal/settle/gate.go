package settle

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/report"
)

// Settle interprets result and drives num to its terminal label, routing a
// parsed "ready" outcome to the self-heal merge gate. Called immediately after
// a Box exits so each issue settles independently of its wave siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) (st Settlement) {
	defer func() { st = s.flushSettled(d, num) }()
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
		// to `spindrift recover` (or settling Failed/unreviewed instead).
		if s.tryMarkRecoverable(d, num, result) {
			return
		}
		s.settleUnresolved(num, clsNote, "no outcome in log")
		return
	}

	o := result.Resolved.Outcome
	// Ahead of the status switch so it runs on every outcome status alike
	// (issue #2019): a run's own findings are worth tracking whether it landed
	// ready or blocked. Best-effort, so a filing failure never changes the
	// switch's landing decision.
	filed := fileIssueIntentsDetailed(s.cfg.Progress, s.it, num, result, s.cfg.FindingLabel, "")
	reportFiled(s.cfg.Progress, num, filed)
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
		if result.Resolved.Provenance == outcome.ProvenanceSynthetic && s.tryMarkRecoverable(d, num, result) {
			return
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: o.Landing, Status: Status(o.Status), Detail: "!! " + o.Note})
		s.transitionState(num, ForReason(dispatchrecord.ReasonBlocked), o.Note)
		// A read-only Box never pushes or opens a PR in-box (issue #1933), so a
		// bundle it wrote to the outbox and a PR-intent line it printed would
		// be stranded once the container exits. Applies to PR-shaped and
		// push-only forges alike (issue #1946). Best-effort and additive:
		// failure never changes the blocked outcome recorded above.
		if s.readOnly && errors.Is(s.relayBlockedWork(num, gen, result), errAbandoned) {
			return
		}
		// A read-write Box may have opened its draft PR before stopping.
		s.lander.latchOpenPR(num)
		if demotedResolved {
			// postBlockedNoteComment skips a writable tracker's read-write run
			// on the assumption the Box commented itself, but an agent that
			// believed it was done never did, so post directly.
			if err := s.it.Comment(num, o.Note); err != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: could not post already-resolved-demoted comment: %v\n", num, err)
			}
		} else {
			s.postBlockedNoteComment(num, o.Note)
		}
		s.postUsageComment(num, d)
	case outcome.StatusReady:
		// A ready the land pass did not report is not trusted (issue #5973):
		// land nothing, and never reach selfHeal.
		if passmanifest.UnreviewedReady(result.Passes) {
			s.failUnreviewed(d, num, o.Landing)
			return
		}
		pr, ok := s.lander.landingRef(d, num, gen, result)
		if !ok {
			return
		}
		landing, reason, err := s.selfHeal(d, num, gen, pr, result.Passes)
		if errors.Is(err, errAbandoned) {
			// Another actor owns the issue, so a usage comment here would be
			// noise.
			return
		}
		if landing == landingFailed {
			s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFailed, Detail: "!! " + reason})
		}
		s.postUsageComment(num, d)
	case "merged":
		// status=merged is off-script: no prompt fragment instructs a Box to
		// print it, so o.Landing is Agent-controlled with no legitimate
		// provenance. It is absent from lib/prompt-contract.nix's
		// outcomeStatusSets (issue #2504), hence a bare string literal.
		s.lander.settleReportedMerge(num)
		s.postUsageComment(num, d)
	case outcome.StatusAmbiguous:
		// The Box detected an internally-contradictory issue and halted before
		// scouting (issue #2275). This is a successful, non-crash stop, so it
		// must never fall through to agent-failed. The Box has no fragment to
		// post this comment in-box, so settle always posts o.Note host-side,
		// unlike postBlockedNoteComment's relay, gated on landing, readOnly
		// and InBoxUnwritableTracker.
		if o.Note != "" {
			if err := s.it.Comment(num, o.Note); err != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: could not post ambiguous-spec comment: %v\n", num, err)
			}
		}
		s.cfg.Progress.Step(Step{Key: num, Landing: o.Landing, Status: Status(o.Status), Detail: "note=" + o.Note})
		s.transitionState(num, ForReason(dispatchrecord.ReasonAmbiguous), o.Note)
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
		s.cfg.Progress.Step(Step{Key: num, Landing: o.Landing, Status: Status(o.Status), Detail: "note=" + o.Note})
		s.transitionState(num, ForReason(dispatchrecord.ReasonAlreadyResolved), o.Note)
		s.closeResolvedIssue(num)
		s.postUsageComment(num, d)
	default:
		s.cfg.Progress.Step(Step{Key: num, Landing: o.Landing, Status: Status(o.Status)})
		// Never transitions, so Settled never sees this Dispatch. o.Status is
		// Box-controlled, so the banner prints the in-progress state, never
		// the status itself.
		s.latchUnrecorded(num, ForReason(dispatchrecord.ReasonOffScript), "")
		s.postUsageComment(num, d)
	}
	return
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
		s.cfg.Progress.Step(Step{Key: num, Status: StatusMissing, Detail: strings.TrimPrefix(clsNote+fmt.Sprintf("  note=PR lookup failed: %v", prErr), "  ")})
		return
	}
	if !res.Found {
		s.cfg.Progress.Step(Step{Key: num, Status: StatusMissing, Detail: strings.TrimPrefix(clsNote+"  note="+missingNote, "  ")})
		// Same detail the log line above prints, folded into one note (issue
		// #3627) so a settled record is diagnosable on disk without the
		// daemon's terminal.
		note := missingNote + clsNote
		s.transitionState(num, ForReason(dispatchrecord.ReasonNoOutcome), note)
		return
	}
	// No transitionState here, on purpose, regardless of draft-ness (issue
	// #1654): an open PR is a real, if unmergeable-right-now, result, and ADR
	// 0012 reserves agent-failed for "never produced a green PR". A non-draft
	// PR only got that way via this launcher's own MarkReady at green (issue
	// #1651).
	s.cfg.Progress.Step(Step{Key: num, Landing: res.URL, Status: StatusBlocked, Detail: fmt.Sprintf("note=no outcome line; PR on %s left for manual adopt", branch)})
}

// Transition moves num from one dispatch state to v's state and is the one
// place a refused TransitionState (a status-lifecycle tracker that could not
// move the card, issue #6353) is recognised. A refusal is posted as a comment,
// since no label was applied and the card would otherwise carry no trace of
// why it did not move, and the returned Verdict and note are the ones the
// Record should settle with: transition-refused, keeping v's state, since the
// displaced outcome still happened. For any other outcome v and note come back
// unchanged. The TransitionState error is returned as is (nil on success);
// logging it is the caller's, since each site words it differently.
func Transition(it forge.IssueTracker, num string, from forge.DispatchState, v Verdict, note string) (Verdict, string, error) {
	err := it.TransitionState(num, from, v.State())
	var refusal *forge.TransitionRefusedError
	if !errors.As(err, &refusal) {
		return v, note, err
	}
	if cerr := it.Comment(num, "spindrift could not move this issue: "+refusal.Error()); cerr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not comment the refused transition: %v\n", num, cerr)
	}
	// The displaced reason and note ride in the new note: a merged PR whose
	// card could not reach Done still landed.
	displaced := v.Reason()
	if note != "" {
		displaced += ": " + note
	}
	return refused(v), fmt.Sprintf("%s (settle reason was %s)", refusal.Error(), displaced), err
}

// settledLatch is one issue's most recently latched decision (terminal, or an
// unrecorded in-progress one), awaiting flushSettled (issue #3627).
type settledLatch struct {
	// verdict's reason is the status= class of the path that latched, the
	// vocabulary the path already prints (fix-exhausted, ci-red,
	// merge-guard-hit, ...).
	verdict Verdict
	note    string
	// unrecorded marks a decision for a Dispatch that never transitions
	// (blockHandoff, the off-script status), so flushSettled returns it without
	// a Record or report.
	unrecorded bool
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors. note is the most specific reason live at the
// call site, "" where none exists; v carries that site's status= class and
// the state it fixes — see flushSettled for why this only latches rather than
// emits.
func (s *Settle) transitionState(num string, v Verdict, note string) {
	// An unclaimed issue (issue #4076) never went agent-in-progress, so there
	// is nothing to leave and no agent-failed to apply here — only a real
	// merge (Complete, below) commits tracker state.
	if s.cfg.Unclaimed && v.State() != forge.Complete {
		return
	}
	v, note, err := Transition(s.it, num, forge.InProgress, v, note)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %s: %v\n", num, v.State(), err)
	}
	// Latched, not emitted, here (issue #3627): one issue's settle path can
	// reach a second, contradicting terminal transition on the same run — a
	// green completeLanding (Complete) that verifyMerged then demotes to
	// Failed after re-checking PR state (pr_lander.go) — so emitting per call
	// would let a stale Complete race a real Failed to the daemon. Last write
	// wins: it is always the tracker's own final decision, checked in
	// call order. The mutex exists because dispatchWave goroutines settle
	// different issues on the same *Settle concurrently.
	s.settledMu.Lock()
	if s.settledLatch == nil {
		s.settledLatch = make(map[string]settledLatch)
	}
	s.settledLatch[num] = settledLatch{verdict: v, note: note}
	s.settledMu.Unlock()
}

// latchUnrecorded latches an in-progress decision no transition backs, for
// flushSettled to return as the Settlement without recording it. It never
// displaces a terminal latch, so that latch's dispatch_settled op still lands.
func (s *Settle) latchUnrecorded(num string, v Verdict, note string) {
	s.settledMu.Lock()
	defer s.settledMu.Unlock()
	if s.settledLatch == nil {
		s.settledLatch = make(map[string]settledLatch)
	}
	if prev, ok := s.settledLatch[num]; ok && !prev.unrecorded {
		return
	}
	s.settledLatch[num] = settledLatch{verdict: v, note: note, unrecorded: true}
}

// flushSettled settles num's latched decision, if any, exactly once
// (issue #3627). Call it via defer at every settle entry point. A terminal
// latch is emitted as one dispatch_settled op; an unrecorded one is only
// returned, so an issue that never reaches a terminal state still produces no
// record. Emitting here, not in transitionState, means a path that latches
// twice (Complete demoted to Failed) still appends one op. The returned
// Settlement is that same latched decision, zero when nothing latched.
func (s *Settle) flushSettled(d dispatch.Dispatcher, num string) Settlement {
	s.settledMu.Lock()
	rec, ok := s.settledLatch[num]
	pr := s.prLatch[num]
	_, owned := s.reportKey[num]
	delete(s.settledLatch, num)
	delete(s.prLatch, num)
	delete(s.reportKey, num)
	s.settledMu.Unlock()
	if !ok {
		return Settlement{}
	}
	if rec.unrecorded {
		return Settlement{Verdict: rec.verdict, Note: rec.note}
	}
	if owned {
		// A Chore owns this issue (SettlePatch) and already sent its one
		// terminal record; an issue-keyed one would be a kind mismatch
		// (issue #4966).
		return Settlement{Verdict: rec.verdict, PRURL: pr, Note: rec.note}
	}
	ds := hostop.DispatchSettled{Note: rec.note, PRURL: pr}
	if d != nil && dispatchrecord.ReasonLanded(rec.verdict.Reason()) && pr != "" {
		ds.MergeCommit = readMergeCommit(s.cfg.Capabilities.MergeCommitReader, "#"+num, pr, os.Stderr)
	}
	if d == nil {
		// A nil Dispatcher has no Record, so nothing is appended: the primary
		// log's stamp may name an unrelated one.
		return Settled(dispatchkey.Issue(num), "", rec.verdict, ds)
	}
	ds.RecordID = d.RecordID()
	var path string
	if s.cfg.LogPath != nil {
		path = s.cfg.LogPath(num)
	}
	// An adopt's Dispatcher never Ran: it carries the prior Record's ID when a
	// fix or conflict pass adopted it, else none and the stamp supplies it.
	return SettledPrior(dispatchkey.Issue(num), path, rec.verdict, ds)
}

// Settled is the single terminal-record emitter for every settle path (only
// LateMerges' reconcile upgrade appends one outside it, through
// dispatchrecord.AppendSettled): it records ds as a dispatch_settled op in the
// Dispatch's primary Pass log and the root's Record store, then reports the
// settled record. ds's State and Reason are v's, overwritten here. Best-effort;
// what gates the append is documented on dispatchrecord.AppendSettled. It
// returns the Settlement it recorded.
func Settled(key dispatchkey.Key, logPath string, v Verdict, ds hostop.DispatchSettled) Settlement {
	if v.reason == "" {
		panic("settle: Settled needs a Verdict from ForReason, not the zero value")
	}
	if v.State() == forge.InProgress {
		panic("settle: Settled got an in-progress Verdict; those reasons are never recorded")
	}
	ds.State, ds.Reason = v.State().String(), v.Reason()
	dispatchrecord.RecordSettled(os.Stderr, key.String(), logPath, ds)
	report.Settled(key, ds.State, ds.Note, ds.PRURL, ds.RecordID)
	return Settlement{Verdict: v, PRURL: ds.PRURL, Note: ds.Note}
}

// SettledPrior is Settled for a Dispatch that never Ran and so minted no
// Record ID (recover's, an adopt's): the Record it settles is the prior
// Dispatch's, so a ds with no RecordID takes the one stamped at the head of
// logPath. Use it only where that prior Record is genuinely the target.
func SettledPrior(key dispatchkey.Key, logPath string, v Verdict, ds hostop.DispatchSettled) Settlement {
	if ds.RecordID == "" && logPath != "" {
		if s, ok := dispatchrecord.ReadStamp(logPath); ok {
			ds.RecordID = s.RecordID
		}
	}
	return Settled(key, logPath, v, ds)
}

// SettledBy settles d's own Record: the terminal state of a Dispatch that was
// Run (or failed trying), keyed by what d minted. A Dispatch that failed before
// minting a Record ID appends nothing rather than touching an earlier one.
func SettledBy(d dispatch.Dispatcher, key dispatchkey.Key, v Verdict, note string) Settlement {
	return Settled(key, d.LogPath(), v, hostop.DispatchSettled{RecordID: d.RecordID(), Note: note})
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

// keyFor is the key num's report records go out under.
func (s *Settle) keyFor(num string) dispatchkey.Key {
	s.settledMu.Lock()
	defer s.settledMu.Unlock()
	if k, ok := s.reportKey[num]; ok {
		return k
	}
	return dispatchkey.Issue(num)
}

// postBlockedNoteComment posts note when the Box had no way to post it in-box:
// the local content plane (ADR 0032, issue #1692), a read-only github/jira
// Box stripped of its write token (issue #1917), or a tracker the Box can never
// write even read-write (InBoxUnwritableTracker, jira; issue #5263).
// Best-effort, matching postUsageComment's log-but-don't-propagate contract.
func (s *Settle) postBlockedNoteComment(num, note string) {
	inBoxUnwritable := s.cfg.Capabilities.TrackerDescriptor.InBoxUnwritableTracker
	if (s.landing == nil && !s.readOnly && !inBoxUnwritable) || note == "" {
		return
	}
	if err := s.it.Comment(num, note); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post blocked-note comment: %v\n", num, err)
	}
}

// recordLanding persists landing onto the tracker issue via the optional
// LandingRecorder interface (ADR 0029), then the pass that produced it. An
// empty landing is a no-op: a blank write must never clear an already-recorded
// ref. Best-effort, matching transitionState's log-but-don't-propagate
// contract. A failed landing write skips the pass write so the pair never
// disagrees.
func (s *Settle) recordLanding(num, landing string, passes []passmanifest.Entry) {
	if s.landing == nil || landing == "" {
		return
	}
	if err := s.landing.RecordLanding(num, landing); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not record landing: %v\n", num, err)
		return
	}
	s.recordLandingPass(num, passes)
}

// recordLandingPass persists which pass produced the landing just recorded
// (issue #2983). It runs only from recordLanding, never off the Box-controlled
// outcome line (issue #5189). Picks the last entry with OutcomeFound true,
// else the last entry. With no manifest evidence it clears the recorded pass, so a
// landing written without pass evidence (e.g. `spindrift recover`) never
// inherits an earlier run's pass. Best-effort, matching transitionState's
// log-but-don't-propagate contract. Fix passes that settle dispatches later
// never call it (see forge.LandingPassRecorder).
func (s *Settle) recordLandingPass(num string, passes []passmanifest.Entry) {
	if s.landingPass == nil {
		return
	}
	var entry passmanifest.Entry
	if len(passes) > 0 {
		entry = passes[len(passes)-1]
	}
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

// unreviewedLanding is the landing= value for an unreviewed fail: the outcome
// line's own when there is one, else the issue's derived agent branch.
func (s *Settle) unreviewedLanding(num string, result dispatch.Result) string {
	if result.Resolved.Found {
		return result.Resolved.Outcome.Landing
	}
	return s.cfForNum(num).AgentBranch(num)
}

// failUnreviewed settles num as agent-failed for a run the review loop never
// carried through its land pass: a ready from another pass (issue #5973) or a
// signal-killed run stopped before land (issue #5984). Nothing is landed and
// selfHeal never runs.
func (s *Settle) failUnreviewed(d dispatch.Dispatcher, num, landing string) {
	s.cfg.Progress.Step(Step{Key: num, Landing: landing, Status: StatusFailed, Detail: "!! " + dispatchrecord.UnreviewedNote})
	s.transitionState(num, ForReason(dispatchrecord.ReasonUnreviewed), dispatchrecord.UnreviewedNote)
	retry := "re-apply the dispatch label"
	if s.cfg.DispatchLabel != "" {
		retry = fmt.Sprintf("re-apply `%s`", s.cfg.DispatchLabel)
	}
	body := "The run's review loop stopped before its land pass finished, so the work was never reviewed and landed; nothing was landed. To re-run it, " + retry + "."
	if err := s.it.Comment(num, body); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post unreviewed comment: %v\n", num, err)
	}
	s.postUsageComment(num, d)
}
