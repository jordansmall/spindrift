package settle

import (
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
)

// settleAdopted runs the merge gate on an already-discovered open PR for num,
// draft or not (issue #2408). It is the shared tail of Recover's open-PR arm
// and SettlePatch, the butler patch gate (a Settle built with Unclaimed, ADR
// 0057), never Settle's own no-outcome path, which reports status=blocked
// (issue #1654). The head SHA may not come from this process, so the gate
// waits for evidence the rollup registered (issue #1652), bounded by
// registrationWindowPolls (#2475).
func (s *Settle) settleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) (st Settlement) {
	defer func() { st = s.flushSettled(d, num) }()
	// Host-discovered, so it names the record even if the gate never gets a
	// CheckState answer.
	s.latchPR(num, prURL)
	branch := s.agentBranch(num)
	note := fmt.Sprintf("no outcome line; PR discovered on %s", branch)
	if s.cfg.Unclaimed {
		note = "host-opened patch PR"
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: prURL, Status: StatusAdopted, Detail: "note=" + note})
	landing, reason, err := s.selfHealAdopted(d, num, gen, prURL)
	if errors.Is(err, errAbandoned) {
		// Another actor owns the issue's comment and state.
		return
	}
	if landing == landingFailed {
		s.cfg.Progress.Step(Step{Key: num, Landing: prURL, Status: StatusFailed, Detail: "!! " + reason})
	}
	return
}

// SettlePatch is the butler patch gate's entry (ADR 0057). It reports under
// owner, the dispatching Chore's key, since the daemon parses a butler child's
// records as chore-keyed (issue #4966). The owner is latched per num and
// cleared by settleAdopted's deferred flushSettled, so a num must not be gated
// twice concurrently; adoptLandings gates each finding issue once, in turn.
func (s *Settle) SettlePatch(owner dispatchkey.Key, num, prURL string) Settlement {
	s.settledMu.Lock()
	if s.reportKey == nil {
		s.reportKey = make(map[string]dispatchkey.Key)
	}
	s.reportKey[num] = owner
	s.settledMu.Unlock()
	return s.settleAdopted(nil, num, 0, prURL)
}

// postUsageComment posts d's aggregate usage-statistics comment to the issue.
// It logs a post failure instead of returning one, so the caller continues.
func (s *Settle) postUsageComment(num string, d dispatch.Dispatcher) {
	// Audited for leaks (issue #1233, extending #831): UsageReport's only
	// external input is the MODEL env var, so it carries no Box-internal
	// output, and commentErr is a post failure that reaches stderr only.
	// Nothing here needs redaction.
	if commentErr := s.it.Comment(num, d.UsageReport()); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: post usage comment: %v\n", num, commentErr)
	}
}
