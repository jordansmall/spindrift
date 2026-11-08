package settle

import (
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// SettleAdopted runs the merge gate on an already-discovered open PR for num,
// draft or not (issue #2408). Only recover's adopt path and the butler patch
// gate (a Settle built with Unclaimed, ADR 0057) call it, never Settle's own
// no-outcome path, which reports status=blocked (issue #1654). The head SHA
// may not come from this process, so the gate waits for evidence the rollup
// registered (issue #1652), bounded by registrationWindowPolls (#2475).
func (s *Settle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) (st Settlement) {
	defer func() { st = s.flushSettled(d, num) }()
	// Host-discovered, so it names the record even if the gate never gets a
	// CheckState answer.
	s.latchPR(num, prURL)
	branch := s.cf.AgentBranch(num)
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
// cleared by SettleAdopted's deferred flushSettled, so a num must not be gated
// twice concurrently; adoptLandings gates each finding issue once, in turn.
func (s *Settle) SettlePatch(owner dispatchkey.Key, num, prURL string) Settlement {
	s.settledMu.Lock()
	if s.reportKey == nil {
		s.reportKey = make(map[string]dispatchkey.Key)
	}
	s.reportKey[num] = owner
	s.settledMu.Unlock()
	return s.SettleAdopted(nil, num, 0, prURL)
}

// verifyMerged confirms a PR reported merged carries both a MERGED state and
// the CompleteLabel, and demotes the issue to Failed otherwise, since a gap
// there means something merged outside the gate. A read that still fails after
// its retries proves nothing, so it leaves the issue as it stands rather than
// demote it. A verified merge also drops the landed bundle; a demoted settle
// keeps it.
func (s *Settle) verifyMerged(num, pr string) {
	var prState forge.PRState
	var iss forge.Issue
	err := s.retryVerifyRead(num, pr, func() error {
		var err error
		prState, err = s.pr.PRState(pr)
		return err
	})
	if err == nil {
		err = s.retryVerifyRead(num, pr, func() error {
			var err error
			iss, err = s.it.Issue(num)
			return err
		})
	}
	if err != nil {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeUnverifiedReadError, Detail: fmt.Sprintf("!! %v", err)})
		return
	}
	if prState == forge.PRMerged && containsLabel(iss.Labels, s.cfg.CompleteLabel) {
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusVerifiedMerged})
		s.removeLandedBundle(num)
		s.closeIssue(num)
		return
	}
	var reason string
	if prState != forge.PRMerged {
		reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
	} else {
		reason = fmt.Sprintf("issue does not carry '%s'", s.cfg.CompleteLabel)
	}
	s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusFailed, Detail: "!! " + reason})
	s.transitionState(num, ForReason(dispatchrecord.ReasonMergeUnverified), reason)
}

// retryVerifyRead runs read, retrying a failure up to Policy.Max times with
// backoff.
func (s *Settle) retryVerifyRead(num, pr string, read func() error) error {
	attempts := 0
	for {
		err := read()
		if err == nil {
			return nil
		}
		if attempts >= s.cfg.Policy.Max {
			return err
		}
		attempts++
		s.cfg.Progress.Step(Step{Key: num, Landing: pr, Status: StatusMergeUnverifiedReadRetry,
			Detail: fmt.Sprintf("attempt=%d/%d  !! %v", attempts, s.cfg.Policy.Max, err)})
		s.transientBackoff().Do(attempts)
	}
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
