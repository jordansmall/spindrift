package settle

import (
	"errors"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// applyMergeMode performs the mode-specific action after CI reaches green.
// agent-complete is already set; a merge failure is returned as an error but
// does not revert the label.
//
// d, when non-nil, resolves rebase conflicts (via d.ResolveConflict) that
// arise while mergeImmediate retries. When nil, a rebase conflict is
// immediately non-retriable.
func (s *Settle) applyMergeMode(num, pr string, d dispatch.Dispatcher) error {
	switch s.cfg.MergeMode {
	case "immediate":
		return s.mergeImmediate(num, pr, d)
	case "auto":
		if err := s.fc.EnqueueAutoMerge(pr); err != nil {
			fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueue-failed  !! %v\n", num, pr, err)
			s.fc.Comment(num, fmt.Sprintf("auto-merge enqueue failed: %v — PR is green; approve and merge manually", err))
			return nil
		}
		fmt.Printf("    #%s  pr=%s  status=auto-merge-enqueued\n", num, pr)
		return nil
	case "manual":
		fmt.Printf("    #%s  pr=%s  status=agent-complete  merge-mode=%s\n", num, pr, s.cfg.MergeMode)
		return nil
	default:
		return fmt.Errorf("unrecognised MERGE_MODE: %q", s.cfg.MergeMode)
	}
}

// mergeImmediate attempts to merge the green PR with rebase retry on conflict.
// It embodies the existing rebase-retry and agent conflict-resolve behaviors.
//
// A successful conflict-resolve already rebased and force-pushed the branch,
// so the next Merge conflict is retried directly (after a brief settle wait
// for the forge's mergeability snapshot to catch up) instead of invoking
// Rebase a second time.
//
// A Rebase force-push failure that forge.ErrTransientPushFailure wraps (an
// infra or network fault, not a genuine stale-lease rejection) is retried up
// to MaxRebaseAttempts times before it's treated as terminal.
func (s *Settle) mergeImmediate(num, pr string, d dispatch.Dispatcher) error {
	rebaseAttempts := 0
	pushRetries := 0
	skipRebase := false
	for {
		err := s.fc.Merge(pr)
		if err == nil {
			return nil
		}
		if !errors.Is(err, forge.ErrMergeConflict) {
			return err
		}
		if skipRebase {
			skipRebase = false
			fmt.Printf("    #%s  pr=%s  status=merge-retry-settle\n", num, pr)
			time.Sleep(time.Duration(s.cfg.MergePollInterval) * time.Second)
			continue
		}
		if rebaseAttempts >= s.cfg.MaxRebaseAttempts {
			return err
		}
		rebaseAttempts++
		fmt.Printf("    #%s  pr=%s  status=rebase-retry  attempt=%d/%d\n",
			num, pr, rebaseAttempts, s.cfg.MaxRebaseAttempts)
		rbErr := s.fc.Rebase(pr)
		for rbErr != nil && errors.Is(rbErr, forge.ErrTransientPushFailure) && pushRetries < s.cfg.MaxRebaseAttempts {
			pushRetries++
			fmt.Printf("    #%s  pr=%s  status=rebase-push-retry  attempt=%d/%d  !! %v\n",
				num, pr, pushRetries, s.cfg.MaxRebaseAttempts, rbErr)
			rbErr = s.fc.Rebase(pr)
		}
		if rbErr != nil {
			if errors.Is(rbErr, forge.ErrTransientPushFailure) {
				fmt.Printf("    #%s  pr=%s  status=rebase-push-retries-exhausted  attempts=%d  !! %v\n",
					num, pr, pushRetries, rbErr)
				return rbErr
			}
			if errors.Is(rbErr, forge.ErrMergeConflict) && d != nil {
				fmt.Printf("    #%s  pr=%s  status=conflict-resolve\n", num, pr)
				if crErr := d.ResolveConflict(pr); crErr != nil {
					fmt.Printf("    #%s  pr=%s  status=conflict-resolve-failed  !! %v\n", num, pr, crErr)
					return crErr
				}
				skipRebase = true
			} else {
				fmt.Printf("    #%s  pr=%s  status=rebase-failed  !! %v\n", num, pr, rbErr)
				return rbErr
			}
		}
	}
}
