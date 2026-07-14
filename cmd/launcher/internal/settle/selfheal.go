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
func (s *Settle) landPushOnly(num, branch string) (ok bool, merged bool) {
	s.transitionState(num, forge.InProgress, forge.Complete)
	if err := s.applyMergeMode(num, branch, nil); err != nil {
		fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, branch, err)
		s.it.Comment(num, fmt.Sprintf("merge blocked after push: %v", err))
		return true, false
	}
	return true, s.cfg.MergeMode == "immediate"
}

// selfHeal polls the merge gate, dispatching fix boxes on genuine red up to
// MaxFixAttempts times. On green it swaps agent-complete (via gateToGreen)
// then applies the merge mode; a merge failure after green leaves the issue
// agent-complete and is never demoted to agent-failed.
//
// Returns (ok, merged): ok is true when CI reached green; merged is true only
// when immediate mode completed an actual merge. A merge failure keeps the
// issue at agent-complete (merge-blocked note) and returns (true, false).
//
// d dispatches fix passes and, when a rebase conflict arises, an
// agent-assisted conflict resolution -- both subject to dispatch's own
// in-session transient retry (issue #441).
func (s *Settle) selfHeal(d dispatch.Dispatcher, num, pr string) (ok bool, merged bool) {
	if s.pr == nil {
		return s.landPushOnly(num, pr)
	}
	for attempt := 0; ; attempt++ {
		gate := s.gateToGreen(num, pr)
		green := gate == GateGreen
		genuineRed := gate == GateRedRetry
		if green {
			matched, guardErr := s.mergeGuardHit(pr)
			if guardErr != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-check-error  !! %v\n", num, pr, guardErr)
				s.it.Comment(num, fmt.Sprintf("merge guard: could not list changed files (%v) — downgrading to manual as a precaution; review and merge by hand", guardErr))
				return true, false
			}
			if len(matched) > 0 {
				fmt.Printf("    #%s  pr=%s  status=merge-guard-hit  paths=%v\n", num, pr, matched)
				s.it.Comment(num, mergeGuardComment(matched))
				return true, false
			}
			if err := s.applyMergeMode(num, pr, d); err != nil {
				fmt.Printf("    #%s  pr=%s  status=merge-blocked  !! %v\n", num, pr, err)
				s.it.Comment(num, fmt.Sprintf("merge blocked after green CI: %v", err))
				return true, false
			}
			return true, s.cfg.MergeMode == "immediate"
		}
		if !genuineRed || attempt >= s.cfg.MaxFixAttempts {
			if genuineRed && s.cfg.MaxFixAttempts > 0 {
				fmt.Printf("    #%s  pr=%s  status=fix-exhausted  !! exhausted %d fix pass(es)\n",
					num, pr, s.cfg.MaxFixAttempts)
			}
			s.transitionState(num, forge.InProgress, forge.Failed)
			return false, false
		}
		fmt.Printf("    #%s  pr=%s  fix-pass=%d/%d\n", num, pr, attempt+1, s.cfg.MaxFixAttempts)
		// Best-effort: a failure to fetch the CI failure detail must never
		// block the fix pass — fall back to an empty summary.
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
