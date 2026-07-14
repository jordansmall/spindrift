package settle

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// SettleAdopted runs the merge gate (selfHeal → verifyMerged) on an
// already-discovered open non-draft PR for num. Prints "status=adopted"
// before running the gate. Called by the reconcile/recover entry points and
// by Settle itself when a Box exits with no outcome line.
func (s *Settle) SettleAdopted(d dispatch.Dispatcher, num, prURL string) {
	branch := s.cf.AgentBranch(num)
	fmt.Printf("    #%s  pr=%s  status=adopted  note=no outcome line; PR discovered on %s\n", num, prURL, branch)
	switch s.selfHeal(d, num, prURL) {
	case LandingMerged:
		s.verifyMerged(num, prURL)
	case LandingFailed:
		fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", num, prURL)
	}
}

// verifyMerged is the tripwire: it confirms a PR reported merged actually
// carries a MERGED state and CompleteLabel, demoting the issue to Failed
// otherwise (evidence of a merge outside the gate).
func (s *Settle) verifyMerged(num, pr string) {
	prState, _ := s.pr.PRState(pr)
	iss, _ := s.it.Issue(num)
	if prState == forge.PRMerged && containsLabel(iss.Labels, s.cfg.CompleteLabel) {
		fmt.Printf("    #%s  pr=%s  status=verified-merged\n", num, pr)
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
	fmt.Printf("    #%s  pr=%s  status=failed  !! %s\n", num, pr, reason)
	s.transitionState(num, forge.InProgress, forge.Failed)
}

// postUsageComment posts d's aggregate usage-statistics comment to the
// issue. Errors posting the comment are logged but do not abort the caller.
func (s *Settle) postUsageComment(num string, d dispatch.Dispatcher) {
	if commentErr := s.it.Comment(num, d.UsageReport()); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: post usage comment: %v\n", num, commentErr)
	}
}
