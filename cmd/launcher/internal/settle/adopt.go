package settle

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// SettleAdopted runs the merge gate on an already-discovered open PR for num,
// draft or not (issue #2408). Only reconcile/recover call it, never Settle's
// own no-outcome path, which reports status=blocked (issue #1654). The head
// SHA may not come from this process, so the gate waits for evidence the
// rollup registered (issue #1652), bounded by registrationWindowPolls (#2475).
func (s *Settle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	defer s.flushSettled(num)
	// Host-discovered, so it names the record even if the gate never gets a
	// CheckState answer.
	s.latchPR(num, prURL)
	branch := s.cf.AgentBranch(num)
	note := fmt.Sprintf("no outcome line; PR discovered on %s", branch)
	if s.cfg.Unclaimed {
		note = "host-opened patch PR"
	}
	fmt.Printf("    #%s  landing=%s  status=adopted  note=%s\n", num, prURL, note)
	landing, reason := s.selfHealAdopted(d, num, gen, prURL)
	switch landing {
	case landingMerged:
		// A push-only Code Forge has no PR state for verifyMerged to read.
		if s.pr != nil {
			s.verifyMerged(num, prURL)
		}
	case landingFailed:
		fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, prURL, reason)
	case landingAbandoned:
		// See landingAbandoned: another actor owns the issue's comment and state.
	}
}

// verifyMerged confirms a PR reported merged carries both a MERGED state and
// the CompleteLabel, and demotes the issue to Failed otherwise, since a gap
// there means something merged outside the gate. A verified merge also drops
// the landed bundle; a demoted settle keeps it.
func (s *Settle) verifyMerged(num, pr string) {
	prState, _ := s.pr.PRState(pr)
	iss, _ := s.it.Issue(num)
	if prState == forge.PRMerged && containsLabel(iss.Labels, s.cfg.CompleteLabel) {
		fmt.Printf("    #%s  landing=%s  status=verified-merged\n", num, pr)
		s.removeLandedBundle(num)
		s.closeIssue(num)
		return
	}
	var reason string
	if prState != forge.PRMerged {
		if prState == "" {
			reason = "PR state is 'unknown', expected MERGED"
		} else {
			reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
		}
	} else {
		reason = fmt.Sprintf("issue does not carry '%s'", s.cfg.CompleteLabel)
	}
	fmt.Printf("    #%s  landing=%s  status=failed  !! %s\n", num, pr, reason)
	s.transitionState(num, forge.InProgress, forge.Failed, reason)
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
