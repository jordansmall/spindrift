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

// tryAdoptRelayedBranch adopts a read-only Box's relayed branch into a PR when
// the ADR 0036 synthetic status=blocked backstop contradicts the driver's own
// last genuine success self-report (issues #2223, #2224). The self-report is
// unauthenticated input, so this trusts it only for a PR-shaped read-only forge
// and returns false on any doubt, leaving the caller's blocked handling to run.
func (s *Settle) tryAdoptRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if result.Resolved.Provenance != outcome.ProvenanceSynthetic || !s.readOnly || s.pr == nil {
		return false
	}
	// situationFor stats the outbox, so it runs only after the cheap checks
	// above. openPRFound is false: this function never looks for an open PR.
	sit := s.situationFor(num, false, result)
	if !sit.SelfReportSuccess {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch")
}

// tryAdoptRelayedBranchNoOutcome is tryAdoptRelayedBranch for a Box that died
// before emitting any parseable outcome line (issue #2253): Resolved.Found is
// false, so no ADR 0036 backstop is stitched in and the Provenance gate there
// never fires. gate.go's sole caller already sits inside the
// !result.Resolved.Found branch, so no explicit guard for it is needed here.
func (s *Settle) tryAdoptRelayedBranchNoOutcome(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool {
	if !s.readOnly || s.pr == nil {
		return false
	}
	// situationFor stats the outbox, so it runs only after the cheap checks
	// above. openPRFound is false: this function never looks for an open PR.
	sit := s.situationFor(num, false, result)
	if !sit.SelfReportSuccess {
		return false
	}

	return s.adoptAndGate(d, num, gen, result, "no outcome line; genuine success self-report and relayed bundle; PR opened on relayed branch")
}

// adoptAndGate is the shared adopt+gate tail behind tryAdoptRelayedBranch
// (#2224) and SettleRelayedBranch (#2225): open a PR on num's relayed branch,
// print the status=adopted line, then drive the same merge gate the "ready"
// path uses. Returns false, with no side effect, when no PR could be opened.
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
		// recover runs read-write, so s.pr may be nil here for a push-only Code
		// Forge even though tryAdoptRelayedBranch guarantees it is not.
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
// (#2225); sit comes from the caller (#2501). Recover is read-write, so it
// needs neither synthetic provenance nor a read-only Box. An open PR is
// SettleAdopted's job, so sit.OpenPRFound returns false. Local push-only has
// no PR to open (ADR 0039, #2254), where a bundle alone is evidence (#2378).
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

// landRelayedBranchPushOnly lands num's relayed branch directly, the local
// push-only counterpart to adoptAndGate. The branch comes from the issue's own
// derived agent branch, never an outcome-line field (issue #1949): there may
// be no parsed outcome line at all on this path. note is printed verbatim.
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
// a PR on it. branch comes from cf.AgentBranch(num), never the outcome line's
// landing= field (#1949): a prompt-injected read-only Box controls that field.
// FallbackDefault means a missing PR-intent line falls back to an issue-derived
// default instead of blocking, since this Box was cut short before that step.
func (s *Settle) adoptRelayedBranch(num string, result dispatch.Result) (string, bool) {
	branch, m := s.mediationFor(num)
	url, _, _, err := m.Open(num, branch, result, FallbackDefault)
	if err != nil {
		return "", false
	}
	return url, true
}

// tryMarkRecoverable promotes a local push-only issue to Recoverable (ADR
// 0039) when either a genuine success self-report or a signal kill (#2378)
// coincides with a bundle in the outbox, leaving the land itself to `spindrift
// recover`. It only stats the outbox, so no local issue is ever
// fast-forwarded on unauthenticated evidence alone.
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

// bundlePresent reports whether num's outbox holds a relayable bundle file. It
// stats rather than calling RelayBundle: detecting Recoverable must never
// import or land anything.
func (s *Settle) bundlePresent(num string) bool {
	if s.cfg.OutboxDir == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(s.cfg.OutboxDir(num), seambundle.FileName))
	return err == nil
}

// isSuccessSelfReport reports whether a driver self-report's Status means the
// run succeeded. Only outcome.StatusReady counts: the bare word "success" was
// never in outcome.WorkStatuses, so per #2981 it is no longer accepted here,
// deliberately narrowing the self-report side of #2223's adoption path.
func isSuccessSelfReport(status string) bool {
	return status == outcome.StatusReady
}
