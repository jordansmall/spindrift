package settle

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// terminateCases are the rows where an operator's Terminate (or Reclaim) lands
// while Settle stands in the PR landing: one row per stop point, each of which
// must abandon without committing tracker state or posting a comment, since
// the stop already released the issue (ADR 0024, issue #3523).
func terminateCases() []prCase {
	flip := []string{"MarkReady:" + testPR}
	reds := []forge.RollupState{red, red, red, red}
	pending := []forge.RollupState{forge.StatePending}
	reaped := errors.New("exit status 137") // what Reclaim's SIGKILL leaves a Box with

	// stopOn marks the issue terminated as the named forge method is entered.
	stopOn := func(method string) func(*forge.Fake, *dispatch.Fake, func()) {
		return func(fc *forge.Fake, _ *dispatch.Fake, mark func()) { fc.On(method, func(int) { mark() }) }
	}
	// stopInBox marks the issue terminated as the named Box dispatch starts.
	stopInBox := func(method string) func(*forge.Fake, *dispatch.Fake, func()) {
		return func(_ *forge.Fake, d *dispatch.Fake, mark func()) { d.On(method, func(int) { mark() }) }
	}
	// stopOnSleep marks the issue terminated in the first pause through the
	// injected clock; the mark itself arrives once Settle exists.
	var stopNow func()
	stopOnSleep := func(c *Config) {
		c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { stopNow() }}
	}
	armSleep := func(_ *forge.Fake, _ *dispatch.Fake, mark func()) { stopNow = mark }
	stale := func(_ *testing.T, fc *forge.Fake, c *Config) {
		c.PreflightStaleBase = true
		fc.SetNeedsUpdate(testPR, true)
	}

	var polls int

	return []prCase{
		{
			name: "a stop before the first poll abandons", checks: pending,
			wantStatuses:  []Status{detailOnly},
			midCall:       func(_ *forge.Fake, _ *dispatch.Fake, mark func()) { mark() },
			wantAbandoned: true,
		},
		{
			// The deadline is far past the fifth poll, so a missed mark would run on.
			name: "a stop mid-poll abandons at the next checkpoint", checks: pending,
			wantStatuses: []Status{detailOnly},
			midCall: func(fc *forge.Fake, _ *dispatch.Fake, mark func()) {
				fc.On("CheckState", func(n int) {
					polls = n
					if n >= 5 {
						mark()
					}
				})
			},
			wantAbandoned: true,
			check: func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
				if polls != 5 {
					t.Errorf("CheckState polled %d times, want 5: a mid-poll stop must not run toward the deadline", polls)
				}
			},
		},
		{
			// A drain that never escalates to a stop must not shorten a settle (issue #3523).
			name: "a poll nobody stops runs the full deadline", checks: pending,
			wantStatuses: []Status{detailOnly, StatusGateTerminal, StatusFailed},
			wantPRClosed: true, want: settledAs(dispatchrecord.ReasonGateTerminal, "ci-timeout: CI-watch deadline reached after 100s"),
			wantSteps:  []Step{stepAt(testPR, StatusGateTerminal, "!! ci-timeout: CI-watch deadline reached after 100s")},
			wantLabels: []string{"agent-failed"}, wantNoLabels: []string{"agent-complete"},
			wantComments: []string{"landing failed: ci-timeout: CI-watch deadline reached after 100s"},
		},
		{
			name: "a stop on the poll that hits the deadline abandons instead of failing", checks: pending,
			wantStatuses:  []Status{detailOnly},
			setup:         func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 0 },
			midCall:       stopOn("CheckState"),
			wantAbandoned: true,
		},
		{
			name: "a stop on the red poll with no fix budget abandons instead of failing", checks: []forge.RollupState{red},
			wantStatuses:  []Status{detailOnly},
			midCall:       stopOn("CheckState"),
			wantAbandoned: true,
		},
		{
			name: "a stop during the success-confirm pause abandons before the ready flip", mergeMode: "manual",
			wantStatuses:  []Status{detailOnly},
			setup:         func(_ *testing.T, _ *forge.Fake, c *Config) { stopOnSleep(c) },
			midCall:       armSleep,
			wantAbandoned: true,
		},
		{
			// The first pause is the gate's success-confirm; the second is the
			// merge-transient backoff, which must not sleep out its wait and merge again.
			name: "a stop during a merge-transient backoff abandons instead of merging again", mergeErrs: []error{forge.ErrMergeTransient, nil},
			wantStatuses: []Status{detailOnly, StatusMergeTransientRetry},
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) {
				c.Policy = retry.Policy{Max: 3, Unit: 5 * time.Second}
				sleeps := 0
				c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {
					if sleeps++; sleeps == 2 {
						stopNow()
					}
				}}
			},
			midCall: armSleep, wantAbandoned: true, wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR},
		},
		{
			name: "a stop during the ready flip abandons before the manual hand-off", mergeMode: "manual",
			wantStatuses: []Status{detailOnly, StatusAgentComplete},
			midCall:      stopOn("MarkReady"), wantAbandoned: true, wantCalls: flip,
		},
		{
			// The mark lands ahead of the hold's own PR comment, which still fires.
			name: "a stop while the merge guard reads the PR's files abandons", mergeMode: "manual",
			wantStatuses: []Status{detailOnly, StatusMergeGuardHit},
			guard:        ".github/**", files: []string{".github/workflows/ci.yml"},
			midCall: stopOn("ListPRFiles"), wantAbandoned: true, wantCalls: flip,
			wantPRExact: guardWording(".github/workflows/ci.yml"),
		},
		{
			name:         "a stop during the ready flip keeps an immediate merge from retrying a conflict",
			wantStatuses: []Status{detailOnly},
			maxRebase:    5, mergeErrs: []error{errConflict},
			midCall: stopOn("MarkReady"), wantAbandoned: true, wantCalls: flip,
		},
		{
			// preflightStaleBase force-pushes, so it must not run for a stopped issue (issue #943).
			name: "a stop before the stale-base preflight skips its rebase", maxRebase: 3, setup: stale,
			wantStatuses: []Status{detailOnly},
			midCall:      stopOn("MarkReady"), wantAbandoned: true, wantCalls: flip,
		},
		{
			name: "a stop during the stale-base rebase abandons before the re-wait", maxRebase: 3, setup: stale,
			wantStatuses: []Status{detailOnly, StatusStaleBaseRebase, StatusPostForcePushWait},
			midCall:      stopOn("Rebase"), wantAbandoned: true, wantCalls: flip, wantRebases: 1,
		},
		{
			name:         "a stop during a plain rebase abandons the re-wait, not never-green",
			wantStatuses: []Status{detailOnly, StatusRebaseRetry, StatusPostForcePushWait},
			maxRebase:    5, mergeErrs: []error{errConflict},
			midCall: stopOn("Rebase"), wantAbandoned: true, wantRebases: 1,
			wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR},
		},
		{
			name:         "a stop during the conflict-resolve Box abandons the re-wait, not never-green",
			wantStatuses: []Status{detailOnly, StatusRebaseRetry, StatusConflictResolve},
			maxRebase:    5, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			midCall: stopInBox("ResolveConflict"), wantAbandoned: true,
			wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR}, wantResolves: 1, wantRebases: 1,
		},
		{
			name:         "a conflict-resolve Box reaped by the stop abandons, not never-green",
			wantStatuses: []Status{detailOnly, StatusRebaseRetry, StatusConflictResolve},
			maxRebase:    5, mergeErrs: []error{errConflict}, rebaseErr: errConflict, resolveErr: reaped,
			midCall: stopInBox("ResolveConflict"), wantAbandoned: true,
			wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR}, wantResolves: 1, wantRebases: 1,
		},
		{
			name: "a stale-base conflict-resolve Box reaped by the stop abandons", maxRebase: 3, setup: stale,
			wantStatuses: []Status{detailOnly, StatusStaleBaseRebase, StatusConflictResolve},
			rebaseErr:    errConflict, resolveErr: reaped,
			midCall: stopInBox("ResolveConflict"), wantAbandoned: true,
			wantCalls: []string{"MarkReady:" + testPR, "MarkDraft:" + testPR}, wantResolves: 1, wantRebases: 1,
		},
		{
			name: "a stop during a fix pass ends the retry loop", maxFix: 3, checks: reds,
			wantStatuses: []Status{detailOnly, detailOnly},
			midCall:      stopInBox("Fix"), wantAbandoned: true, wantFixes: 1,
		},
		{
			name: "a fix pass reaped by the stop abandons instead of failing", maxFix: 3, checks: []forge.RollupState{red},
			wantStatuses:     []Status{detailOnly, detailOnly},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) { d.FixResult = dispatch.Failed(dispatch.Result{}) },
			midCall:          stopInBox("Fix"), wantAbandoned: true, wantFixes: 1,
		},
		{
			name: "a stop during the fix-no-op confirm pause abandons instead of failing", maxFix: 3, checks: []forge.RollupState{red},
			wantStatuses: []Status{detailOnly, detailOnly},
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				fc.SetHeadCommitSHAs(testPR, []string{"sha-unchanged", "sha-unchanged", "sha-unchanged"})
				stopOnSleep(c)
			},
			midCall: armSleep, wantAbandoned: true, wantFixes: 1,
		},
		{
			name: "a fix pass that succeeds under a stop relays no bundle", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly},
			maxFix:       3, checks: []forge.RollupState{red},
			setup: func(_ *testing.T, _ *forge.Fake, c *Config) {
				c.OutboxDir = func(num string) string { return "/outbox/" + num }
			},
			midCall: stopInBox("Fix"), wantAbandoned: true, wantFixes: 1,
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				if len(fc.RelayBundleCalls) != 0 {
					t.Errorf("RelayBundle calls = %+v, want none for a released issue", fc.RelayBundleCalls)
				}
			},
		},
	}
}
