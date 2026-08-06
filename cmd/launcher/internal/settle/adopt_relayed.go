package settle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

// tryAdoptRelayedBranch is gate.go's "blocked" arm's first move (issue
// #2224): a read-only Box's own outcome line is Agent-controlled, so a Box
// that crashed, hung, or otherwise never emitted a real SPINDRIFT_OUTCOME
// line before exiting gets the ADR 0036 synthetic status=blocked backstop
// stitched in host-side — normally an honest "never finished" signal that
// belongs on the park-agent-failed path below. But the same driver log can
// also carry the driver's own last genuine (non-synthetic) leading-token
// self-report (issue #2223), surfaced separately as Result.Resolved.SelfReport
// precisely so it is never shadowed by the backstop's last-line-wins. When
// that self-report says the run actually succeeded, the backstop's
// "blocked" is a false negative: the Box likely did finish its work and
// bundle a real branch to the outbox before whatever cut its final print
// short. Rather than park that as agent-failed, adopt the relayed branch
// into a real PR and let the normal merge lifecycle judge it on CI, the
// same way a genuine status=ready outcome would.
//
// The self-report is unauthenticated advisory input (outcome.SelfReport's
// own doc) — this function is where that trust gets spent, and spends it
// narrowly: only for a read-only PR-shaped Code Forge (s.pr != nil,
// mirroring hostMediateDraftPR's own precondition — a push-only forge's
// landPushOnly already relays independently of the outcome line's status),
// and only once adoptRelayedBranch below confirms a real bundle was there to
// relay and a PR was actually opened on it. Returns false — falling through
// to the caller's normal blocked handling unchanged — the moment any part of
// that fingerprint or the adoption itself doesn't hold, so a Box that
// genuinely never finished (no self-report, or one that itself reports
// something other than success) is never granted this override.
func (s *Settle) tryAdoptRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if result.Resolved.Provenance != outcome.ProvenanceSynthetic || !s.readOnly || s.pr == nil ||
		!result.Resolved.SelfReportFound || !isSuccessSelfReport(result.Resolved.SelfReport.Status) {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch")
}

// tryAdoptRelayedBranchNoOutcome is gate.go's "no outcome line at all" arm's
// first move (issue #2253): a read-only Box that crashed, hung, or was
// killed before it ever emitted a parseable SPINDRIFT_OUTCOME line leaves
// result.Resolved.Found false — no synthetic status=blocked backstop gets
// stitched in for this case the way ADR 0036 does when a driver log carries
// at least a trailing partial line, so tryAdoptRelayedBranch's own
// Provenance == ProvenanceSynthetic gate never fires here. This is otherwise
// the same fingerprint and the same false-negative risk tryAdoptRelayedBranch
// guards against: the driver's own last genuine self-report
// (Result.Resolved.SelfReport, issue #2223) may still say the run succeeded,
// and a real branch may still be sitting in the outbox waiting to be
// relayed. Rather than park that as agent-failed, this reuses adoptAndGate's
// exact same adopt-then-gate tail — see tryAdoptRelayedBranch's own doc
// comment for the full reasoning behind the self-report trust boundary and
// the remaining gate conditions, which this function mirrors unchanged apart
// from the outcome-line check itself.
//
// No explicit !result.Resolved.Found guard here: gate.go's sole caller
// already sits inside the `!result.Resolved.Found` branch, so
// Resolved.Found is always false on entry.
func (s *Settle) tryAdoptRelayedBranchNoOutcome(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if !s.readOnly || s.pr == nil ||
		!result.Resolved.SelfReportFound || !isSuccessSelfReport(result.Resolved.SelfReport.Status) {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "no outcome line; genuine success self-report and relayed bundle; PR opened on relayed branch")
}

// adoptAndGate is the shared adopt+gate tail behind both
// tryAdoptRelayedBranch (issue #2224, the read-only backstop-override arm)
// and SettleRelayedBranch (issue #2225, recover's operator-driven arm): open
// a PR on num's relayed branch via adoptRelayedBranch, print the
// status=adopted line with note attached, then drive the exact same merge
// gate (recordLanding, selfHeal, verifyMerged/failed-print/abandoned-return,
// postUsageComment) either caller's own "ready" or "adopted" siblings use.
// Returns false — with no side effect beyond adoptRelayedBranch's own
// early-out — the moment adoptRelayedBranch itself fails to open a PR.
func (s *Settle) adoptAndGate(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result, note string) bool {
	pr, ok := s.adoptRelayedBranch(num, result)
	if !ok {
		return false
	}

	fmt.Printf("    #%s  landing=%s  status=adopted  note=%s\n", num, pr, note)
	s.recordLanding(num, pr)
	landing, reason := s.selfHeal(d, num, gen, pr)
	switch landing {
	case landingMerged:
		// s.pr is guaranteed non-nil by tryAdoptRelayedBranch's fingerprint
		// gate, but SettleRelayedBranch carries no such guarantee (recover
		// runs read-write, so s.pr may be nil for a push-only Code Forge);
		// the nil check keeps this correct for both callers.
		if s.pr != nil {
			s.verifyMerged(num, pr)
		}
	case landingFailed:
		fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, pr, reason)
	case landingAbandoned:
		// Terminate already recorded its own comment and log line; a usage
		// comment here would be noise on an issue it reclaimed.
		return true
	}
	s.postUsageComment(num, d)
	return true
}

// SettleRelayedBranch is spindrift recover's adopt-a-relayed-branch arm
// (issue #2225). With no open PR on num, recover consults the driver's own
// last genuine success self-report (result.Resolved.SelfReport, issue #2223 —
// recovered from disk by dispatch.ResolveFromLogs) for evidence a
// prior run finished the work and relayed its branch to the outbox before
// stranding without a PR. Unlike tryAdoptRelayedBranch, this does NOT
// require result.Resolved.Provenance == outcome.ProvenanceSynthetic or
// s.readOnly — recover is operator-driven and runs read-write.
//
// Two shapes fall out of the same self-report fingerprint. A CODE_FORGE=local
// push-only run (ADR 0039, issue #2254) has no PR surface to adopt at all —
// adoptRelayedBranch's DraftPRCreator assertion always fails for it — so that
// shape (s.pr == nil and cf implements forge.BundleRelay, the same gate
// tryMarkRecoverable used to promote the issue to Recoverable in the first
// place) is routed to landRelayedBranchPushOnly instead, which lands the
// branch directly via the same RelayBundle+merge machinery a genuine
// status=ready outcome uses. Every other shape — a PR-shaped forge, or plain
// git push-only (s.pr == nil but no BundleRelay) — falls through to
// adoptAndGate unchanged; the capability gate inside adoptRelayedBranch
// (BundleRelay + DraftPRCreator + OutboxDir) is what still scopes that path,
// correctly returning false for git the same way it always has, and it keeps
// requiring a genuine success self-report exactly as before.
//
// The local push-only shape alone gets one further leniency (issue #2378): a
// signal-killed Box never gets the chance to print an outcome or self-report
// line at all, so recover — a separate, later process with no access to the
// original dispatch.Result.KilledBySignal bit tryMarkRecoverable observed
// live — cannot re-derive "killed by signal" from disk the way it re-derives
// a self-report via dispatch.ResolveFromLogs. Since a bundle actually sitting
// in the outbox is the same hard physical precondition tryMarkRecoverable
// already required alongside either kind of evidence before ever promoting
// the issue to Recoverable in the first place, and since `spindrift recover
// <n>` targeting one specific issue is itself a deliberate operator act
// gated upstream by the agent-recover label workflow, s.bundlePresent(num)
// alone is accepted as sufficient evidence for this shape when there is no
// self-report to fall back on. Returns false — leaving recover's unchanged
// "no open PR" exit — the moment neither a genuine success self-report nor a
// present bundle backs the local push-only shape, or (for every other shape)
// the self-report isn't a genuine success.
func (s *Settle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	selfReportOK := result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status)
	cf := s.cfForNum(num)
	if _, ok := cf.(forge.BundleRelay); ok && s.pr == nil {
		if selfReportOK {
			return s.landRelayedBranchPushOnly(d, num, gen, "genuine success self-report; relayed branch landed")
		}
		if s.bundlePresent(num) {
			return s.landRelayedBranchPushOnly(d, num, gen, "bundle present in outbox; relayed branch landed")
		}
		return false
	}
	if !selfReportOK {
		return false
	}
	return s.adoptAndGate(d, num, gen, result, "genuine success self-report; PR opened on relayed branch")
}

// landRelayedBranchPushOnly is SettleRelayedBranch's local push-only
// counterpart to adoptAndGate: local has no PR surface to open
// (adoptRelayedBranch's DraftPRCreator assertion always fails for it), so
// recovering a Recoverable issue instead drives the exact same
// RelayBundle+merge landing path a genuine status=ready outcome uses
// (selfHealGate's landPushOnly arm), keyed off the issue's own derived
// agent branch — never any outcome-line field (issue #1949 provenance
// discipline; there may be no parsed outcome line at all on this path). note
// is printed verbatim in the status=adopted line, distinguishing the two
// kinds of evidence SettleRelayedBranch may have accepted to reach here — a
// genuine self-report, or (issue #2378) a bundle present in the outbox alone.
func (s *Settle) landRelayedBranchPushOnly(d dispatch.Dispatcher, num string, gen uint64, note string) bool {
	branch := s.cfForNum(num).AgentBranch(num)
	fmt.Printf("    #%s  landing=%s  status=adopted  note=%s\n", num, branch, note)
	s.recordLanding(num, branch)
	landing, reason := s.selfHeal(d, num, gen, branch)
	switch landing {
	case landingFailed:
		fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, branch, reason)
	case landingAbandoned:
		return true
	}
	s.postUsageComment(num, d)
	return true
}

// adoptRelayedBranch is tryAdoptRelayedBranch's PR-opening step: relay num's
// finished branch out of the outbox and open a real PR on it, the same
// relay-then-create shape hostMediateDraftPR uses for a genuine
// status=ready outcome.
//
// branch is derived from cf.AgentBranch(num), never result.Resolved.Outcome.Landing
// (issue #1949, same reasoning as hostMediateDraftPR/relayBlockedWork): a
// prompt-injected read-only Box controls the outcome line's landing= field,
// but not the one ref its own bundle is actually keyed to host-side.
//
// Returns ok=false — with no PR opened and no side effect the caller must
// unwind — whenever the Code Forge lacks either capability, OutboxDir is
// unset, or RelayBundle itself fails (an absent/empty bundle means there is
// no finished branch to adopt at all, so the caller falls back to the
// normal blocked handling rather than open an empty PR on nothing).
//
// Unlike hostMediateDraftPR, a missing or malformed PR-intent line does NOT
// block here: hostMediateDraftPR's Box printed status=ready and had every
// opportunity (including issue #2045's nudge) to leave a usable intent line,
// so a missing one there is treated as a genuine hand-off failure. This
// path's Box instead crashed or was cut short before its final print — it
// may never have reached the PR-intent step at all — so a missing line here
// falls back to an issue-derived default rather than blocking the one
// signal (a genuine success self-report) that got the run this far.
func (s *Settle) adoptRelayedBranch(num string, result dispatch.Result) (string, bool) {
	cf := s.cfForNum(num)
	branch := cf.AgentBranch(num)

	br, ok := cf.(forge.BundleRelay)
	if !ok {
		return "", false
	}
	dpc, ok := cf.(forge.DraftPRCreator)
	if !ok {
		return "", false
	}
	if s.cfg.OutboxDir == nil {
		return "", false
	}
	if err := br.RelayBundle(s.cfg.OutboxDir(num), branch); err != nil {
		return "", false
	}

	title, body, ok := parsePRIntent(result)
	if !ok {
		title, body = s.defaultAdoptPRText(num)
	}
	body = ensureClosesReference(body, num, s.it)

	url, err := dpc.CreateDraftPR(title, body, s.cfg.BaseBranch, branch)
	if err != nil {
		return "", false
	}
	return url, true
}

// defaultAdoptPRText builds the fallback title/body adoptRelayedBranch uses
// when the box's log carried no usable PR-intent line — a real possibility
// here, since this path's box may have crashed before ever reaching that
// print. title prefers the tracker issue's own title when available, falling
// back to a generic "Adopt agent work for #<num>" when the issue lookup
// fails or the issue carries no title. body explains the adoption's
// provenance; adoptRelayedBranch's unconditional ensureClosesReference call
// is what guarantees the "Closes #<num>" reference, the same way an
// agent-authored PR body normally gets one.
func (s *Settle) defaultAdoptPRText(num string) (title, body string) {
	title = fmt.Sprintf("Adopt agent work for #%s", num)
	if iss, err := s.it.Issue(num); err == nil && strings.TrimSpace(iss.Title) != "" {
		title = iss.Title
	}
	body = "Auto-adopted PR for the relayed agent branch: the run's driver self-reported success but its outcome line was missing or degraded to the synthetic backstop (ADR 0036/0039); this PR was opened host-side from the relayed outbox bundle."
	return title, body
}

// tryMarkRecoverable is settle's local push-only counterpart to
// tryAdoptRelayedBranch/tryAdoptRelayedBranchNoOutcome (ADR 0039): a
// CODE_FORGE=local push-only run has no PR-shaped adopt path at all
// (adoptRelayedBranch's DraftPRCreator assertion always fails for it, since
// local never implements it) — instead of parking Failed, either a genuine
// success self-report or an external signal kill (SIGTERM/SIGKILL, issue
// #2378 — the box died before it ever got to print an outcome or self-report
// line) plus a bundle actually sitting in the outbox promotes the issue to
// Recoverable, leaving the actual land (RelayBundle + fast-forward merge
// into the Integration branch) to the operator-driven `spindrift recover`.
// This function only stats the outbox — it never calls RelayBundle/Merge
// itself, so a local issue is never auto-fast-forwarded on unauthenticated
// evidence alone.
func (s *Settle) tryMarkRecoverable(num string, result dispatch.Result) bool {
	cf := s.cfForNum(num)
	selfReportOK := result.Resolved.SelfReportFound && isSuccessSelfReport(result.Resolved.SelfReport.Status)
	if _, ok := cf.(forge.BundleRelay); !ok || s.pr != nil ||
		(!selfReportOK && !result.KilledBySignal) ||
		!s.bundlePresent(num) {
		return false
	}
	reason := "genuine success self-report"
	if !selfReportOK {
		reason = "killed by signal"
	}
	fmt.Printf("    #%s  status=recoverable  note=%s; bundle present in outbox; run `spindrift recover %s` to land it\n", num, reason, num)
	s.transitionState(num, forge.InProgress, forge.Recoverable)
	return true
}

// bundlePresent reports whether num's outbox holds a relayable bundle file —
// a plain stat, not a RelayBundle call: detecting Recoverable must never
// itself import or land anything (see tryMarkRecoverable).
func (s *Settle) bundlePresent(num string) bool {
	if s.cfg.OutboxDir == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.cfg.OutboxDir(num), seambundle.FileName))
	return err == nil
}

// isSuccessSelfReport reports whether status — a driver self-report's
// best-effort Status field (outcome.SelfReport) — indicates the run
// succeeded. Both the grammar's own "ready" success value and the bare
// "success" a paraphrasing model emits when it drops the rest of the
// grammar (issue #2223) count; anything else (including "blocked" or an
// empty/unrecognised word) does not.
func isSuccessSelfReport(status string) bool {
	return status == "ready" || status == "success"
}
