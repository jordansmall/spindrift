package settle

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
)

// mergeCases are the rows where the immediate merge itself is the subject: how
// a refusal (conflict, transient, blocked by checks) or a stale base is
// answered before the PR lands, and what each answer leaves on the forge.
func mergeCases() []prCase {
	merged := prSettlement(dispatchrecord.ReasonMerged)
	blocked := prSettlement(dispatchrecord.ReasonMergeBlocked)
	complete := []string{"agent-complete"}
	failed := []string{"agent-failed"}
	ready := "MarkReady:" + testPR
	draft := "MarkDraft:" + testPR
	merge := "Merge:" + testPR
	conflicted := []string{ready, merge, draft}
	rebased := []string{ready, merge, draft, ready, merge}
	errTransient := forge.ErrTransientPushFailure
	errRepoGone := errors.New("git push --force-with-lease: exit status 128: remote: Repository not found.")
	errDenied := errors.New("gh pr ready --undo: permission denied")
	fourGreen := []forge.RollupState{green, green, green, green} // the initial gate reads a pair, and so does each re-wait after a force-push
	pollTimesOut := func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 0 }
	stale := func(_ *testing.T, fc *forge.Fake, c *Config) {
		c.PreflightStaleBase = true
		fc.SetNeedsUpdate(testPR, true)
	}
	with := func(fns ...func(*testing.T, *forge.Fake, *Config)) func(*testing.T, *forge.Fake, *Config) {
		return func(t *testing.T, fc *forge.Fake, c *Config) {
			for _, fn := range fns {
				fn(t, fc, c)
			}
		}
	}
	withRebaseErrs := func(errs ...error) func(*testing.T, *forge.Fake, *Config) {
		return func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.RebaseErrs = errs }
	}
	noDraft := func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(fc.MarkDraftCalls) != 0 {
			t.Errorf("MarkDraft calls = %v, want none: this refusal is not a content conflict", fc.MarkDraftCalls)
		}
	}

	pollSleep := time.Second // baseConfig's MergePollInterval; the initial gate's confirm-poll sleeps through the same clock, ahead of the retry
	// backoff is the policy a rebase-push or merge retry sleeps out: retry N
	// waits Unit*N + Jitter in total.
	backoff := func(max int) func(*testing.T, *forge.Fake, *Config) {
		return func(_ *testing.T, _ *forge.Fake, c *Config) {
			c.Policy = retry.Policy{Max: max, Unit: 2 * time.Second, Jitter: time.Second}
		}
	}
	var retrySleeps, blockedSleeps *[]time.Duration
	sleepsInto := func(dst **[]time.Duration) func(*testing.T, *forge.Fake, *Config) {
		return func(_ *testing.T, _ *forge.Fake, c *Config) {
			var clock dispatch.Clock
			*dst, clock = recordingClock()
			c.Clock = clock
		}
	}
	sleptThrough := func(dst **[]time.Duration, want ...time.Duration) func(*testing.T, *forge.Fake, *dispatch.Fake) {
		return func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
			t.Helper()
			if !slices.Equal(**dst, want) {
				t.Errorf("clock sleeps = %v, want %v: the retry must sleep through the injected clock", **dst, want)
			}
		}
	}

	return []prCase{
		{
			name:      "a clean rebase after a conflict re-waits and merges without a further rebase",
			checks:    fourGreen,
			maxRebase: 1, mergeErrs: []error{errConflict},
			want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: rebased,
			wantSteps: []Step{prStep(StatusRebaseRetry, "attempt=1/1"), prStep(StatusPostForcePushWait, "")}, wantRebases: 1,
		},
		{
			name:      "a rebase that fails without a conflict leaves the merge blocked",
			maxRebase: 3, mergeErrs: []error{errConflict}, rebaseErr: errors.New("rebase failed: dirty worktree"),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: conflicted,
			wantSteps:    []Step{prStep(StatusRebaseFailed, "!! rebase failed: dirty worktree")},
			wantComments: []string{"merge blocked after green CI: rebase failed: dirty worktree"}, wantRebases: 1,
		},
		{
			name:      "a conflict that outlasts the rebase budget leaves the merge blocked",
			checks:    fourGreen,
			maxRebase: 1, mergeErrs: []error{errConflict, errConflict},
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: append(append([]string(nil), rebased...), draft),
			wantComments: []string{"merge blocked after green CI: merge conflict"}, wantRebases: 1,
		},
		{
			name:      "a stale conflict after a conflict-resolve is retried, not rebased again or re-demoted",
			checks:    fourGreen,
			maxRebase: 3, mergeErrs: []error{errConflict, errConflict}, rebaseErr: errConflict,
			setup: sleepsInto(&retrySleeps),
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: append(append([]string(nil), rebased...), merge),
			wantSteps:    []Step{prStep(StatusMergeRetrySettle, "")},
			wantResolves: 1, wantRebases: 1,
			check: func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
				if len(fc.MarkDraftCalls) != 1 {
					t.Errorf("MarkDraft calls = %v, want 1: the stale-snapshot retry must not re-demote", fc.MarkDraftCalls)
				}
				sleptThrough(&retrySleeps, pollSleep, pollSleep, pollSleep)(t, fc, d)
			},
		},
		{
			name:   "a conflict-resolve Box in a read-only Box relays its bundle before the merge retries",
			checks: fourGreen,
			shape:  shapeReadOnly, maxRebase: 3, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) {
				c.OutboxDir = func(num string) string { return "/outbox/" + num }
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: rebased, wantResolves: 1, wantRebases: 1,
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				want := []forge.RelayBundleCall{{OutboxDir: "/outbox/1", Ref: fc.AgentBranch("1")}}
				if !slices.Equal(fc.RelayBundleCalls, want) {
					t.Errorf("RelayBundle calls = %+v, want %+v", fc.RelayBundleCalls, want)
				}
			},
		},
		{
			name:      "a failed demotion to draft is logged and the merge goes on",
			checks:    fourGreen,
			maxRebase: 3, mergeErrs: []error{errConflict},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.MarkDraftErr = errDenied },
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: rebased,
			wantSteps: []Step{prStep(StatusMarkDraftFailed, "!! "+errDenied.Error())}, wantRebases: 1,
		},
		{
			name:      "a failed restore to ready is logged and the merge goes on",
			checks:    fourGreen,
			maxRebase: 3, mergeErrs: []error{errConflict},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.MarkReadyErr = errors.New("gh pr ready: permission denied")
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: rebased,
			wantSteps: []Step{prStep(StatusMarkReadyFailed, "!! gh pr ready: permission denied")}, wantRebases: 1,
		},
		{
			name:         "a plain rebase whose re-wait times out never retries the merge",
			wantComments: []string{"(ci-timeout: CI-watch deadline reached after 0s) — no green PR exists at the current head"},
			maxRebase:    3, mergeErrs: []error{errConflict}, setup: pollTimesOut,
			want:     failedLanding("CI did not reach green after force-push on " + testPR + " (ci-timeout: CI-watch deadline reached after 0s)"),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantSteps: []Step{prStep(StatusPostForcePushWait, "")}, wantRebases: 1,
		},

		{
			name:      "a transient merge failure is retried with a growing backoff and the merge lands",
			maxRebase: 3, mergeErrs: []error{forge.ErrMergeTransient, forge.ErrMergeTransient},
			setup: with(backoff(3), sleepsInto(&retrySleeps)),
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, merge, merge, merge},
			check: func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
				noDraft(t, fc, d)
				// The confirm poll, then retries 1 and 2 waiting 3s and 5s in 1s slices.
				sleptThrough(&retrySleeps, slices.Repeat([]time.Duration{pollSleep}, 1+3+5)...)(t, fc, d)
			},
		},
		{
			name:      "a transient merge failure that persists leaves the merge blocked once the retries are spent",
			maxRebase: 3, mergeErrs: slices.Repeat([]error{forge.ErrMergeTransient}, 4),
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) { c.Policy.Max = 3 },
			want:  blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready, merge, merge, merge, merge},
			wantComments: []string{"merge blocked after green CI"},
		},
		{
			// TRANSIENT_RETRY_MAX, not MAX_REBASE_ATTEMPTS, bounds the transient
			// retry loop (issue #2325).
			name:      "the transient retry bound is the policy's, not the rebase budget's",
			maxRebase: 10, mergeErrs: slices.Repeat([]error{forge.ErrMergeTransient}, 2),
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) { c.Policy.Max = 1 },
			want:  blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready, merge, merge},
			wantComments: []string{"merge blocked after green CI"},
		},
		{
			// A checks-blocked wait between two transients does not refill the
			// budget: the fourth Merge result is never consumed.
			name:      "the transient retry budget spans the whole merge, checks-blocked waits included",
			maxRebase: 2, mergeErrs: []error{forge.ErrMergeTransient, forge.ErrMergeBlockedByChecks, forge.ErrMergeTransient, nil},
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) { c.Policy = retry.Policy{Max: 1, Unit: time.Second} },
			want:  blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready, merge, merge, merge},
			wantStepsLike: []Step{prStep(StatusMergeTransientRetry, "attempt=1/1 ")},
			wantComments:  []string{"merge blocked after green CI"},
		},
		{
			name:      "a transient push failure on the rebase is retried and the merge lands",
			checks:    fourGreen,
			maxRebase: 3, mergeErrs: []error{errConflict}, setup: withRebaseErrs(errTransient, nil),
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: rebased, wantRebases: 2,
			wantSteps: []Step{prStep(StatusRebasePushRetry, fmt.Sprintf("attempt=1/3  !! %v", errTransient))},
		},
		{
			name:         "a transient push failure that persists exhausts its retries and leaves the merge blocked",
			wantComments: []string{"merge blocked after green CI: transient push failure"},
			maxRebase:    2, mergeErrs: []error{errConflict}, setup: with(backoff(0), sleepsInto(&retrySleeps), withRebaseErrs(errTransient, errTransient, errTransient)),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: conflicted, wantRebases: 3,
			check:     sleptThrough(&retrySleeps, pollSleep, 3*time.Second, 5*time.Second),
			wantSteps: []Step{prStep(StatusRebasePushRetriesExhausted, fmt.Sprintf("attempts=2  !! %v", errTransient))},
		},
		{
			name:         "a permanent push failure on the rebase is not retried",
			wantComments: []string{"merge blocked after green CI: git push --force-with-lease"},
			maxRebase:    3, mergeErrs: []error{errConflict}, setup: with(backoff(0), sleepsInto(&retrySleeps), withRebaseErrs(errRepoGone, nil)),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: conflicted, wantRebases: 1,
			wantSteps: []Step{prStep(StatusRebaseFailed, "!! "+errRepoGone.Error())},
			check:     sleptThrough(&retrySleeps, pollSleep), // the backoff fires between retries, never before the first attempt
		},
		{
			name:      "no rebase attempts leaves a conflict blocked without a push retry",
			maxRebase: 0, mergeErrs: []error{errConflict}, setup: with(backoff(0), sleepsInto(&retrySleeps), withRebaseErrs(errTransient)),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: conflicted,
			wantComments: []string{"merge blocked after green CI"},
			check:        sleptThrough(&retrySleeps, pollSleep),
		},

		{
			// The retry sleeps through the injected clock, not a bare time.Sleep
			// (issue #2502).
			name:      "a merge blocked by checks is waited out without a rebase, a conflict-resolve or a demotion",
			maxRebase: 3, mergeErrs: []error{forge.ErrMergeBlockedByChecks}, setup: sleepsInto(&blockedSleeps),
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, merge, merge},
			wantSteps:   []Step{prStep(StatusMergeBlockedByChecks, "attempt=1/3")},
			wantNoSteps: []Step{prStep(StatusRebaseRetry, "attempt=1/3"), prStep(StatusMergeRetrySettle, "")},
			check: func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
				noDraft(t, fc, d)
				sleptThrough(&blockedSleeps, pollSleep, pollSleep)(t, fc, d)
			},
		},
		{
			name:      "a merge permanently blocked by checks stops polling and never rebases",
			maxRebase: 2, setup: func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.MergeErr = forge.ErrMergeBlockedByChecks },
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready, merge, merge, merge},
			wantComments: []string{"merge blocked after green CI"}, check: noDraft,
		},

		{
			name:      "a stale base is rebased and re-confirmed green before the merge",
			checks:    fourGreen,
			maxRebase: 3, setup: stale,
			want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: []string{ready, ready, merge},
			wantSteps: []Step{prStep(StatusStaleBaseRebase, "attempt=1/3")}, wantRebases: 1,
		},
		{
			// The #670 / #672 collision (issue #936): green on its own stale base,
			// red against the base that now holds its sibling.
			name:      "a stale base whose combined tree fails CI is never merged",
			maxRebase: 3, setup: stale, checks: []forge.RollupState{green, green, red},
			want:     failedLanding("CI went red after force-push on " + testPR),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: []string{ready},
			wantSteps: []Step{prStep(StatusStaleBaseRebase, "attempt=1/3")}, wantRebases: 1,
			wantComments: []string{"no fix passes allowed (MAX_FIX_ATTEMPTS=0)"},
		},
		{
			name:      "a failed staleness check falls through to the merge",
			checks:    fourGreen,
			maxRebase: 3, setup: with(stale, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.NeedsUpdateErr = errors.New("gh api graphql: rate limited")
			}),
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, merge},
			wantSteps: []Step{prStep(StatusNeedsUpdateCheckError, "!! gh api graphql: rate limited")},
		},
		{
			name:      "a stale-base rebase whose transient push failures never recover blocks the merge",
			maxRebase: 2, setup: with(stale, withRebaseErrs(errTransient, errTransient, errTransient)),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready}, wantRebases: 3,
			wantSteps:    []Step{prStep(StatusStaleBaseRebaseFailed, fmt.Sprintf("!! %v", errTransient))},
			wantComments: []string{"merge blocked after green CI"},
		},
		{
			name:         "a permanent push failure on the stale-base rebase is not retried",
			wantComments: []string{"merge blocked after green CI: git push --force-with-lease"},
			maxRebase:    3, setup: with(stale, withRebaseErrs(errRepoGone, nil)),
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready}, wantRebases: 1,
			wantSteps: []Step{prStep(StatusStaleBaseRebaseFailed, "!! "+errRepoGone.Error())}, check: noDraft,
		},
		{
			name:      "a transient push failure on the stale-base rebase retries without demoting",
			checks:    fourGreen,
			maxRebase: 3, setup: with(stale, backoff(0), sleepsInto(&retrySleeps), withRebaseErrs(errTransient, errTransient, nil)),
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, ready, merge}, wantRebases: 3,
			wantSteps: []Step{prStep(StatusRebasePushRetry, fmt.Sprintf("attempt=1/3  !! %v", errTransient))},
			check: func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
				noDraft(t, fc, d)
				// The confirm polls around the retries sleep through the same clock.
				sleptThrough(&retrySleeps, pollSleep, 3*time.Second, 5*time.Second, pollSleep)(t, fc, d)
			},
		},
		{
			name:      "a stale-base rebase that conflicts is resolved by a Box, demoted to draft meanwhile",
			checks:    fourGreen,
			maxRebase: 3, rebaseErr: errConflict, setup: stale,
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, draft, ready, merge},
			wantSteps:    []Step{prStep(StatusConflictResolve, ""), prStep(StatusPostForcePushWait, "")},
			wantResolves: 1, wantRebases: 1,
		},
		{
			name:      "a stale-base conflict-resolve skipped for a live run abandons",
			maxRebase: 3, rebaseErr: errConflict, setup: stale,
			resolveErr: fmt.Errorf("resolve conflict: %w", runner.ErrAlreadyRunning),
			abandoned:  true, wantCalls: []string{ready, draft}, wantResolves: 1, wantRebases: 1,
		},
		{
			name:      "a failed demotion to draft at the stale-base conflict is logged and the resolve goes on",
			checks:    fourGreen,
			maxRebase: 3, rebaseErr: errConflict,
			setup: with(stale, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.MarkDraftErr = errDenied }),
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, draft, ready, merge},
			wantSteps: []Step{prStep(StatusMarkDraftFailed, "!! "+errDenied.Error())}, wantResolves: 1, wantRebases: 1,
		},
		{
			name:         "a failed conflict-resolve at the stale-base conflict blocks the merge",
			wantComments: []string{"agent could not resolve conflict — no green PR exists at the current head"},
			maxRebase:    3, rebaseErr: errConflict, setup: stale, resolveErr: errors.New("agent could not resolve conflict"),
			want:     failedLanding("conflict-resolve dispatch failed: agent could not resolve conflict"),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: []string{ready, draft},
			wantSteps: []Step{prStep(StatusConflictResolveFailed, "!! agent could not resolve conflict")}, wantResolves: 1, wantRebases: 1,
		},
		{
			name:         "a stale-base conflict-resolve whose re-wait never confirms green blocks the merge",
			wantComments: []string{"(ci-timeout: CI-watch deadline reached after 0s) — no green PR exists at the current head"},
			maxRebase:    3, rebaseErr: errConflict, setup: with(stale, pollTimesOut),
			want:     failedLanding("CI did not reach green after force-push on " + testPR + " (ci-timeout: CI-watch deadline reached after 0s)"),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: []string{ready, draft},
			wantSteps: []Step{prStep(StatusPostForcePushWait, "")}, wantResolves: 1, wantRebases: 1,
		},
		{
			name:  "no rebase attempts disables the stale-base preflight",
			setup: stale,
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, merge},
		},
		{
			// A NeedsUpdate call would fault here; the preflight must not make one.
			name:      "the preflight is off by default and a stale-but-green PR merges as-is",
			maxRebase: 3,
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.SetNeedsUpdate(testPR, true)
				fc.NeedsUpdateErr = errors.New("NeedsUpdate must not be called when the preflight is off")
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: []string{ready, merge},
			wantNoSteps: []Step{prStep(StatusNeedsUpdateCheckError, "!! NeedsUpdate must not be called when the preflight is off")},
		},

		{
			name: "an auto-merge that fails to enqueue leaves the merge blocked with a comment", mergeMode: "auto",
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.EnqueueAutoMergeErr = errors.New("gh pr merge --auto: permission denied")
			},
			want: blocked, wantLabels: complete, wantNoLabels: failed, wantCalls: []string{ready, "EnqueueAutoMerge:" + testPR},
			wantSteps:    []Step{prStep(StatusAutoMergeEnqueueFailed, "!! gh pr merge --auto: permission denied")},
			wantComments: []string{"auto-merge enqueue failed: gh pr merge --auto: permission denied — PR is green; approve and merge manually"},
		},
	}
}

// Settle always passes a Dispatcher, so only the lander seam can reach the
// d != nil guards: a rebase conflict with nothing to dispatch a conflict-resolve
// Box stays terminal instead of attempting ResolveConflict (issue #1319).
func TestInternalSeam_RebaseConflictWithoutDispatcherIsTerminal(t *testing.T) {
	for _, preflight := range []bool{false, true} {
		t.Run(fmt.Sprintf("preflight=%v", preflight), func(t *testing.T) {
			c := baseConfig()
			c.MaxRebaseAttempts = 3
			c.PreflightStaleBase = preflight
			fc := forge.NewFake()
			fc.SetNeedsUpdate(testPR, true)
			fc.RebaseErr = errConflict
			fc.MergeErrs = []error{errConflict}
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
			s := newTestSettle(c, fc, fc)

			err := prLander{s}.mergeImmediate("1", 0, testPR, nil)

			if err == nil {
				t.Fatal("mergeImmediate: want an error when no Dispatcher can resolve the conflict, got nil")
			}
			if fc.Merged != "" {
				t.Errorf("Merge must not succeed; fc.Merged=%q", fc.Merged)
			}
			if len(fc.RebasedURLs) != 1 {
				t.Errorf("Rebase called %d times, want 1", len(fc.RebasedURLs))
			}
		})
	}
}
