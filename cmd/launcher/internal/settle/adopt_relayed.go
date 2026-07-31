package settle

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// tryAdoptRelayedBranch is gate.go's "blocked" arm's first move (issue
// #2224): a read-only Box's own outcome line is Agent-controlled, so a Box
// that crashed, hung, or otherwise never emitted a real SPINDRIFT_OUTCOME
// line before exiting gets the ADR 0036 synthetic status=blocked backstop
// stitched in host-side — normally an honest "never finished" signal that
// belongs on the park-agent-failed path below. But the same driver log can
// also carry the driver's own last genuine (non-synthetic) leading-token
// self-report (issue #2223), surfaced separately as Result.SelfReport
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
	if !result.Outcome.Synthetic || !s.readOnly || s.pr == nil ||
		!result.SelfReportFound || !isSuccessSelfReport(result.SelfReport.Status) {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch")
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
	switch s.selfHeal(d, num, gen, pr) {
	case landingMerged:
		// s.pr is guaranteed non-nil by tryAdoptRelayedBranch's fingerprint
		// gate, but SettleRelayedBranch carries no such guarantee (recover
		// runs read-write, so s.pr may be nil for a push-only Code Forge);
		// the nil check keeps this correct for both callers.
		if s.pr != nil {
			s.verifyMerged(num, pr)
		}
	case landingFailed:
		fmt.Printf("    #%s  landing=%s  status=failed  !! CI or merge failed\n", num, pr)
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
// last genuine success self-report (result.SelfReport, issue #2223 —
// recovered from disk by dispatch.LastSelfReportFromLogs) for evidence a
// prior run finished the work and relayed its branch to the outbox before
// stranding without a PR. Unlike tryAdoptRelayedBranch, this does NOT
// require result.Outcome.Synthetic or s.readOnly — recover is
// operator-driven and runs read-write; the capability gate inside
// adoptRelayedBranch (BundleRelay + DraftPRCreator + OutboxDir) is what
// still scopes it. Returns false — leaving recover's unchanged "no open PR"
// exit — the moment the self-report isn't a genuine success or nothing was
// actually relayable.
func (s *Settle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if !result.SelfReportFound || !isSuccessSelfReport(result.SelfReport.Status) {
		return false
	}
	return s.adoptAndGate(d, num, gen, result, "genuine success self-report; PR opened on relayed branch")
}

// adoptRelayedBranch is tryAdoptRelayedBranch's PR-opening step: relay num's
// finished branch out of the outbox and open a real PR on it, the same
// relay-then-create shape hostMediateDraftPR uses for a genuine
// status=ready outcome.
//
// branch is derived from cf.AgentBranch(num), never result.Outcome.Landing
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
// provenance and appends a literal "Closes #<num>" so a merge auto-closes
// the issue the same way an agent-authored PR body normally would.
func (s *Settle) defaultAdoptPRText(num string) (title, body string) {
	title = fmt.Sprintf("Adopt agent work for #%s", num)
	if iss, err := s.it.Issue(num); err == nil && strings.TrimSpace(iss.Title) != "" {
		title = iss.Title
	}
	body = fmt.Sprintf(
		"Auto-adopted PR for the relayed agent branch (issue #2224): the run succeeded but its outcome degraded to the synthetic backstop (ADR 0036); this PR was opened host-side from the relayed outbox bundle.\n\nCloses #%s",
		num,
	)
	return title, body
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
