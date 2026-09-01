package settle

import (
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

// tryAdoptRelayedBranch is gate.go's "blocked" arm's first move: a Box that
// never emitted a real SPINDRIFT_OUTCOME line gets the ADR 0036 synthetic
// status=blocked backstop stitched in host-side. But the same driver log can
// carry the driver's own last genuine self-report, surfaced separately as
// Result.Resolved.SelfReport so the backstop's last-line-wins never shadows
// it. When that self-report says the run succeeded, "blocked" is a false
// negative — the Box likely did finish and bundle a real branch before
// whatever cut its final print short — so adopt the relayed branch into a
// real PR and let the normal merge lifecycle judge it on CI.
//
// The self-report is unauthenticated advisory input (outcome.SelfReport's own
// doc); this function is where that trust gets spent, and it spends it
// narrowly: only for a read-only PR-shaped Code Forge, and only once
// adoptRelayedBranch confirms a real bundle was there to relay and a PR was
// actually opened. Returns false — falling through to the caller's normal
// blocked handling — the moment any part of that fingerprint doesn't hold.
func (s *Settle) tryAdoptRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if result.Resolved.Provenance != outcome.ProvenanceSynthetic || !s.readOnly || s.pr == nil {
		return false
	}
	// Deferred until after the cheap checks above so bundlePresent's stat never
	// runs on a call those already decline. openPRFound is false: this function
	// never checks for one — see Situation's doc comment.
	sit := s.situationFor(num, false, result)
	if !sit.SelfReportSuccess {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch")
}

// tryAdoptRelayedBranchNoOutcome is gate.go's "no outcome line at all" arm's
// first move. ADR 0036 stitches in the synthetic backstop only when the driver
// log carries at least a trailing partial line, so tryAdoptRelayedBranch's
// ProvenanceSynthetic gate never fires here — but the fingerprint and the
// false-negative risk are otherwise identical, so this mirrors it unchanged
// apart from the outcome-line check. See tryAdoptRelayedBranch for the
// self-report trust boundary.
//
// No explicit !result.Resolved.Found guard: gate.go's sole caller already sits
// inside the `!result.Resolved.Found` branch.
func (s *Settle) tryAdoptRelayedBranchNoOutcome(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if !s.readOnly || s.pr == nil {
		return false
	}
	// Deferred after the cheap checks, as in tryAdoptRelayedBranch.
	sit := s.situationFor(num, false, result)
	if !sit.SelfReportSuccess {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "no outcome line; genuine success self-report and relayed bundle; PR opened on relayed branch")
}

// adoptAndGate is the shared adopt+gate tail behind tryAdoptRelayedBranch and
// SettleRelayedBranch: open a PR on num's relayed branch, print status=adopted
// with note attached, then drive the same merge gate a genuine "ready" outcome
// uses. Returns false, with no side effect, when no PR could be opened.
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
		// tryAdoptRelayedBranch's gate guarantees s.pr non-nil, but
		// SettleRelayedBranch does not — recover runs read-write, so s.pr may be
		// nil for a push-only Code Forge.
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

// SettleRelayedBranch is spindrift recover's adopt-a-relayed-branch arm. Its
// whole contract rests on "no open PR on num" — the caller already resolved
// that, but sit.OpenPRFound is re-checked as a hard guard, since an existing
// open PR is SettleAdopted's job and reaching here with one would double-adopt.
// Unlike tryAdoptRelayedBranch this requires neither ProvenanceSynthetic nor
// s.readOnly: recover is operator-driven and runs read-write.
//
// Two shapes fall out of the same self-report fingerprint. A CODE_FORGE=local
// push-only run (ADR 0039) has no PR surface to adopt — adoptRelayedBranch's
// DraftPRCreator assertion always fails for it — so that shape is routed to
// landRelayedBranchPushOnly, which lands the branch via the same
// RelayBundle+merge machinery a genuine status=ready outcome uses. Every other
// shape, including plain git push-only, falls through to adoptAndGate, scoped
// by adoptRelayedBranch's own capability gate.
//
// The local push-only shape alone accepts a present bundle as sufficient
// evidence without a self-report (issue #2378): a signal-killed Box never
// prints either line, and recover — a separate, later process — cannot
// re-derive KilledBySignal from disk the way it re-derives a self-report. A
// bundle in the outbox is the same physical precondition tryMarkRecoverable
// required to promote the issue to Recoverable, and `spindrift recover <n>` is
// itself a deliberate operator act gated by the agent-recover workflow.
func (s *Settle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) bool {
	if sit.OpenPRFound {
		return false
	}
	cf := s.cfForNum(num)
	if _, ok := cf.(forge.BundleRelay); ok && s.pr == nil {
		if sit.SelfReportSuccess {
			return s.landRelayedBranchPushOnly(d, num, gen, "genuine success self-report; relayed branch landed")
		}
		if sit.BundlePresent {
			return s.landRelayedBranchPushOnly(d, num, gen, "bundle present in outbox; relayed branch landed")
		}
		return false
	}
	if !sit.SelfReportSuccess {
		return false
	}
	return s.adoptAndGate(d, num, gen, result, "genuine success self-report; PR opened on relayed branch")
}

// landRelayedBranchPushOnly is SettleRelayedBranch's local push-only
// counterpart to adoptAndGate: it drives the same RelayBundle+merge landing
// path a genuine status=ready outcome uses, keyed off the issue's own derived
// agent branch — never any outcome-line field (issue #1949 provenance
// discipline; there may be no parsed outcome line at all on this path).
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

// adoptRelayedBranch relays num's finished branch out of the outbox and opens
// a real PR on it via Mediation.Open — the same relay-then-create shape
// hostMediateDraftPR uses for a genuine status=ready outcome.
//
// branch is derived from cf.AgentBranch(num), never
// result.Resolved.Outcome.Landing (issue #1949): a prompt-injected read-only
// Box controls the outcome line's landing= field, but not the one ref its own
// bundle is keyed to host-side.
//
// Returns ok=false — no PR opened, nothing for the caller to unwind —
// whenever Open fails for any reason: a missing capability, an unset
// OutboxDir, or a RelayBundle failure (an absent/empty bundle means there is
// no finished branch to adopt, so the caller falls back to normal blocked
// handling rather than opening an empty PR on nothing).
//
// Passing FallbackDefault means a missing or malformed PR-intent line does NOT
// block here, unlike hostMediateDraftPR: that Box printed status=ready and had
// every opportunity to leave a usable intent line, whereas this path's Box was
// cut short and may never have reached the PR-intent step at all.
func (s *Settle) adoptRelayedBranch(num string, result dispatch.Result) (string, bool) {
	branch, m := s.mediationFor(num)
	url, _, _, err := m.Open(num, branch, result, FallbackDefault)
	if err != nil {
		return "", false
	}
	return url, true
}

// tryMarkRecoverable is settle's local push-only counterpart to
// tryAdoptRelayedBranch (ADR 0039), for a run with no PR-shaped adopt path.
// Instead of parking Failed, either a genuine success self-report or an
// external signal kill — plus a bundle actually in the outbox — promotes the
// issue to Recoverable, leaving the land itself to the operator-driven
// `spindrift recover`. This function only stats the outbox, never calling
// RelayBundle/Merge, so a local issue is never auto-fast-forwarded on
// unauthenticated evidence alone.
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
// succeeded. Only the grammar's own outcome.StatusReady counts; anything
// else (including "blocked" or an empty/unrecognised word) does not. Per issue
// #2981, no scanner or settle path may accept a status word outside the
// generated vocabulary (outcome.WorkStatuses) — so a driver that paraphrases
// down to a bare "success" is deliberately not adopted here.
func isSuccessSelfReport(status string) bool {
	return status == outcome.StatusReady
}
