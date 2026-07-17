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
func (s *Settle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	if result.ParseErr != nil {
		fmt.Printf("    #%s  status=malformed  note=unparseable outcome line\n", num)
		return
	}
	if !result.OutcomeFound {
		branch := s.cf.AgentBranch(num)
		res, prErr := forge.ResolveOpenPR(s.cf, num)
		if prErr != nil || !res.Found {
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
		if res.IsDraft {
			fmt.Printf("    #%s  landing=%s  status=blocked  note=draft PR on %s; no outcome line\n", num, res.URL, branch)
			return
		}
		s.SettleAdopted(d, num, gen, res.URL)
		return
	}

	o := result.Outcome
	switch o.Status {
	case "blocked":
		fmt.Printf("    #%s  landing=%s  status=%s  !! %s\n", num, o.Landing, o.Status, o.Note)
		s.postUsageComment(num, d)
	case "ready":
		switch s.selfHeal(d, num, gen, o.Landing) {
		case landingMerged:
			// verifyMerged reads PR state, which a push-only Code Forge
			// does not have — landPushOnly's own cf.Merge success already
			// confirms the push landed, so there is nothing left to verify.
			if s.pr != nil {
				s.verifyMerged(num, o.Landing)
			}
		case landingFailed:
			fmt.Printf("    #%s  landing=%s  status=failed  !! CI or merge failed\n", num, o.Landing)
		case landingAbandoned:
			// Terminate already recorded its own comment and log line; a
			// usage comment here would be noise on an issue it reclaimed.
			return
		}
		s.postUsageComment(num, d)
	case "merged":
		// verifyMerged reads PR state, which a push-only Code Forge does
		// not have — unlike the "ready" case above, this branch logs a
		// status line via the else when s.pr is nil.
		if s.pr != nil {
			s.verifyMerged(num, o.Landing)
		} else {
			fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		}
		s.postUsageComment(num, d)
	default:
		fmt.Printf("    #%s  landing=%s  status=%s\n", num, o.Landing, o.Status)
		s.postUsageComment(num, d)
	}
}

// transitionState is a best-effort dispatch-state transition that logs but
// does not propagate errors, matching the launcher's original behaviour.
func (s *Settle) transitionState(num string, from, to forge.DispatchState) {
	if err := s.it.TransitionState(num, from, to); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not transition to state %d\n", num, to)
	}
}
