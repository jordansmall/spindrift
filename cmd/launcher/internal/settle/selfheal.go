package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// landPushOnly is the push-only-forge counterpart to the gateToGreen+
// applyMergeMode pair: there is no PR or CI to watch (the Box already pushed
// branch to the remote), so the issue is marked Complete immediately and
// MERGE_MODE is applied straight against the push-only forge's Merge/Rebase.
// A merge failure leaves the issue Complete with a merge-blocked note,
// matching the github adapter's post-green contract (ADR 0012) — it is never
// demoted to Failed.
func (s *Settle) landPushOnly(num, branch string) LandingResult {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, branch, nil); err != nil {
		fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("merge blocked after push: %v", err))
		return LandingManual
	}
	if s.cfg.MergeMode == "immediate" {
		return LandingMerged
	}
	return LandingManual
}

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// MaxFixAttempts times. On green it swaps agent-complete (via gateToGreen)
// then applies the merge mode; a merge failure after green leaves the issue
// agent-complete and is never demoted to agent-failed.
//
// Returns LandingFailed when CI never reached green (genuine red exhausted
// or a gate timeout — the issue is swapped to failedLabel). Otherwise CI
// reached green: LandingMerged when immediate mode completed an actual
// merge, LandingManual for every other green outcome (manual/auto mode, a
// guard hit, or a merge failure — the issue stays at agent-complete with a
// merge-blocked note).
//
// d dispatches fix passes and, when a rebase conflict arises, an
// agent-assisted conflict resolution -- both subject to dispatch's own
// in-session transient retry (issue #441).
func (s *Settle) selfHeal(d dispatch.Dispatcher, num, pr string) LandingResult {
	if s.pr == nil {
		return s.landPushOnly(num, pr)
	}
	for attempt := 0; ; attempt++ {
		switch s.gateToGreen(num, pr) {
		case GateGreen:
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				return LandingManual
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				return LandingManual
			}
			if err := s.applyMergeMode(num, pr, d); err != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				return LandingManual
			}
			if s.cfg.MergeMode == "immediate" {
				return LandingMerged
			}
			return LandingManual
		case GateTerminal:
			s.transitionState(num, forge.InProgress, forge.Failed)
			return LandingFailed
		case GateRedRetry:
			if attempt >= s.cfg.MaxFixAttempts {
				if s.cfg.MaxFixAttempts > 0 {
					fmt.Printf("    #%s  pr=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
						num, pr, s.cfg.MaxFixAttempts)
				}
				s.transitionState(num, forge.InProgress, forge.Failed)
				return LandingFailed
			}
			fmt.Printf("    #%s  pr=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
			// Best-effort: a failure to fetch the CI failure detail must
			// never block the fix pass — fall back to an empty summary.
			detail, detailErr := s.pr.FailureDetail(pr)
			if detailErr != nil {
				fmt.Printf("    #%s  pr=%s  status=failure-detail-unavailable  !! %v\n", num, pr, detailErr)
				detail = ""
			}
			if result := d.Fix(attempt+1, detail); !result.Success {
				fmt.Printf("    !! #%s fix-pass-%d exited non-zero\n", num, attempt+1)
			}
		}
	}
}
