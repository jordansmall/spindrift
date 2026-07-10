package settle

import (
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// Settle interprets result (a Dispatcher's Run outcome) and drives num to its
// terminal label: routing to SettleAdopted or the self-heal merge gate as
// needed, then posting the usage comment. Called immediately after a Box
// exits so each issue reaches CompleteLabel or its failed label independently
// of its wave siblings.
func (s *Settle) Settle(d dispatch.Dispatcher, num string, result dispatch.Result) {
	if result.ParseErr != nil {
		fmt.Printf("    #%s  status=malformed  note=unparseable outcome line\n", num)
		return
	}
	if !result.OutcomeFound {
		branch := s.fc.AgentBranch(num)
		pr, prFound, prErr := s.fc.OpenPRForBranch(branch)
		if prErr != nil || !prFound {
			clsNote := ""
			if result.ClassifyErr != nil {
				fmt.Fprintf(os.Stderr, "    ?? #%s: classify: %v\n", num, result.ClassifyErr)
			} else {
				clsNote = fmt.Sprintf("  class=%s  reason=%s", result.Classification.Class, result.Classification.Reason)
				if result.Classification.ResetAt != nil {
					clsNote += "  resetsAt=" + result.Classification.ResetAt.UTC().Format(time.RFC3339)
				}
			}
			fmt.Printf("    #%s  status=missing%s  note=no outcome in log\n", num, clsNote)
			return
		}
		if pr.IsDraft {
			fmt.Printf("    #%s  pr=%s  status=blocked  note=draft PR on %s; no outcome line\n", num, pr.URL, branch)
			return
		}
		s.SettleAdopted(d, num, pr.URL)
		return
	}

	o := result.Outcome
	switch o.Status {
	case "blocked":
		fmt.Printf("    #%s  pr=%s  status=%s  !! %s\n", num, o.PR, o.Status, o.Note)
		s.postUsageComment(num, d)
	case "ready":
		ok, merged := s.selfHeal(d, num, o.PR)
		if ok {
			// verifyMerged reads PR state, which a push-only Code Forge does
			// not have — landPushOnly's own fc.Merge success already
			// confirms the push landed, so there is nothing left to verify.
			if merged && !s.fc.PushOnly() {
				s.verifyMerged(num, o.PR)
			}
		} else {
			fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", num, o.PR)
		}
		s.postUsageComment(num, d)
	case "merged":
		// verifyMerged reads PR state, which a push-only Code Forge does not
		// have (mirrors the "ready" case's same guard above).
		if !s.fc.PushOnly() {
			s.verifyMerged(num, o.PR)
		} else {
			fmt.Printf("    #%s  pr=%s  status=%s\n", num, o.PR, o.Status)
		}
		s.postUsageComment(num, d)
	default:
		fmt.Printf("    #%s  pr=%s  status=%s\n", num, o.PR, o.Status)
		s.postUsageComment(num, d)
	}
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the launcher's original behaviour.
func (s *Settle) transitionState(num string, from, to forge.DispatchState) {
	if err := s.fc.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}

// gateToGreen polls CheckState on the PR's head commit until the state
// reaches confirmed SUCCESS, a terminal failure, or MergePollTimeout seconds
// elapse. On confirmed green, agent-complete is swapped unconditionally.
//
// Returns (green, genuineRed):
//   - (true, false)  — CI confirmed green; issue swapped to CompleteLabel.
//   - (false, true)  — CI red (FAILURE or ERROR); caller decides whether to
//     dispatch a fix box. No label swap performed.
//   - (false, false) — non-retriable outcome (timeout, API error); no label
//     swap. Caller must swap to failedLabel.
func (s *Settle) gateToGreen(num, pr string) (bool, bool) {
	pollIv := s.cfg.MergePollInterval
	deadline := s.cfg.MergePollTimeout
	// actualIv is used for elapsed tracking; floor to 1 so we don't
	// hot-spin. When pollIv is 0 (test mode) the sleep duration is also 0,
	// so elapsed still advances and the loop terminates.
	actualIv := pollIv
	if actualIv <= 0 {
		actualIv = 1
	}
	elapsed := 0

	for {
		state, stateErr := s.fc.CheckState(pr)
		if stateErr != nil {
			fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, stateErr)
			return false, false
		}

		switch state {
		case forge.StateSuccess:
			// Pause before confirming — back-to-back GraphQL calls return the
			// same snapshot, so a late-registered job would not yet appear.
			time.Sleep(time.Duration(pollIv) * time.Second)
			// Re-poll to confirm the snapshot is stable. A partial check
			// registration can briefly show SUCCESS before all jobs appear.
			confirm, confirmErr := s.fc.CheckState(pr)
			if confirmErr != nil {
				fmt.Printf("    #%s  pr=%s  status=check-state-error  !! %v\n", num, pr, confirmErr)
				return false, false
			}
			if confirm != forge.StateSuccess {
				if confirm == forge.StateFailure || confirm == forge.StateError {
					return false, true
				}
				// PENDING/EXPECTED/NONE — keep waiting for checks to settle.
				break
			}
			// Confirmed green: mark complete regardless of merge outcome.
			s.transitionState(num, forge.InProgress, forge.Complete)
			return true, false
		case forge.StateFailure, forge.StateError:
			// Genuine red — signal caller so it can dispatch a fix pass.
			return false, true
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		time.Sleep(time.Duration(pollIv) * time.Second)
		elapsed += actualIv
	}
	return false, false
}
