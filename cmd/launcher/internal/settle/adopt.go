package settle

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// SettleAdopted runs the merge gate (selfHealAdopted → verifyMerged) on an
// already-discovered open non-draft PR for num. Prints "status=adopted"
// before running the gate. Called only by the reconcile/recover entry
// points — an operator's explicit agent-recover label, never Settle's own
// no-outcome path (issue #1654), which reports status=blocked instead of
// adopting off draft-ness. Unlike Settle's own "ready" path, this PR's head
// SHA was not necessarily pushed by this process, so the gate cannot trust
// an immediately-green rollup without first seeing evidence it registered
// (issue #1652) — see selfHealAdopted.
func (s *Settle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	branch := s.cf.AgentBranch(num)
	fmt.Printf("    #%s  landing=%s  status=adopted  note=no outcome line; PR discovered on %s\n", num, prURL, branch)
	switch s.selfHealAdopted(d, num, gen, prURL) {
	case landingMerged:
		// verifyMerged reads PR state, which a push-only Code Forge does not
		// have (mirrors gate.go's "ready" case guard: silent skip, no
		// logging when s.pr is nil).
		if s.pr != nil {
			s.verifyMerged(num, prURL)
		}
	case landingFailed:
		fmt.Printf("    #%s  landing=%s  status=failed  !! CI or merge failed\n", num, prURL)
	case landingAbandoned:
		// Terminate already recorded its own comment and log line.
	}
}

// verifyMerged is the tripwire: it confirms a PR reported merged actually
// carries a MERGED state and CompleteLabel, demoting the issue to Failed
// otherwise (evidence of a merge outside the gate).
func (s *Settle) verifyMerged(num, pr string) {
	prState, _ := s.pr.PRState(pr)
	iss, _ := s.it.Issue(num)
	if prState == forge.PRMerged && containsLabel(iss.Labels, s.cfg.CompleteLabel) {
		fmt.Printf("    #%s  landing=%s  status=verified-merged\n", num, pr)
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
	s.transitionState(num, forge.InProgress, forge.Failed)
}

// postUsageComment posts d's aggregate usage-statistics comment to the
// issue. Errors posting the comment are logged but do not abort the caller.
func (s *Settle) postUsageComment(num string, d dispatch.Dispatcher) {
	// Audited (issue #1233, extending #831): d.UsageReport() (dispatch/usage.go)
	// never returns an error to its caller — on an ExtractUsage failure it
	// substitutes a static "Usage data unavailable" string. Its only external
	// input is the MODEL env var (a model name, not a credential), so there is
	// no Box-internal output for it to leak. commentErr below is the
	// forge-comment-post failure itself, not a UsageReport error, and it never
	// leaves the process (stderr only) — nothing here needs redaction.
	if commentErr := s.it.Comment(num, d.UsageReport()); commentErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: post usage comment: %v\n", num, commentErr)
	}
}
