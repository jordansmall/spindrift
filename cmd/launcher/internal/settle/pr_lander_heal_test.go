package settle

import (
	"errors"
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

const (
	green = forge.StateSuccess
	red   = forge.StateFailure
)

var errConflict = forge.ErrMergeConflict

// prStep is a progress step of the PR landing of issue 1.
func prStep(status Status, detail string) Step {
	return Step{Key: "1", Landing: testPR, Status: status, Detail: detail}
}

// failedLanding is the Settlement of a landing whose force-pushed head never
// went green, for the cause why.
func failedLanding(why string) Settlement {
	s := prSettlement(dispatchrecord.ReasonLandingFailed)
	s.Note = "settle: force-pushed head never went green: " + why
	return s
}

// healCases are the CI-state, conflict and fix-pass-on-rewait rows of
// TestSettle_PRLander: what a merge conflict or a stale base sets in motion
// between a green PR and its merge.
func healCases() []prCase {
	merged := prSettlement(dispatchrecord.ReasonMerged)
	complete := []string{"agent-complete"}
	failed := []string{"agent-failed"}
	pollTimesOut := func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 0 }
	wentRed := "CI went red after force-push on " + testPR
	conflicted := []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR}
	mergedAfterFix := append(append([]string(nil), conflicted...), "MarkReady:"+testPR, "Merge:"+testPR)
	fixPass := func(n, of int) Step { return prStep("", fmt.Sprintf("fix-pass=%d/%d", n, of)) }

	// Snapshot the labels as the conflict-resolve Box starts: the swap to
	// agent-complete waits for the landing path to settle (issue #757).
	var midResolve []string
	snapshotLabels := func(d *dispatch.Fake, fc *forge.Fake) {
		d.On("ResolveConflict", func(int) {
			iss, _ := fc.Issue("1")
			midResolve = append([]string(nil), iss.Labels...)
		})
	}

	return []prCase{
		{
			name:      "a failed conflict-resolve Box ends failed without a fix pass",
			maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			resolveErr: errors.New("conflict-resolve box exited 1"),
			want:       failedLanding("conflict-resolve dispatch failed: conflict-resolve box exited 1"),
			prClosed:   true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantSteps:    []Step{prStep(StatusConflictResolveFailed, "!! conflict-resolve box exited 1")},
			wantComments: []string{"conflict-resolve box exited 1 — no green PR exists at the current head"},
			wantResolves: 1, wantRebases: 1,
		},
		{
			name:      "a conflict-resolve skipped for a live run abandons",
			maxRebase: 3, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			resolveErr: fmt.Errorf("resolve conflict: %w", runner.ErrAlreadyRunning),
			abandoned:  true, wantCalls: conflicted, wantResolves: 1, wantRebases: 1,
			wantSteps: []Step{prStep(StatusAlreadyInFlight, "~~ conflict-resolve skipped; live run continues")},
		},
		{
			name:      "a plain rebase whose re-wait ends red with no fix budget ends failed",
			maxRebase: 3, mergeErrs: []error{errConflict},
			checks: []forge.RollupState{green, green, red},
			want:   failedLanding(wentRed), prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantComments: []string{"no fix passes allowed (MAX_FIX_ATTEMPTS=0)"}, wantRebases: 1,
		},
		{
			name:      "a conflict-resolve whose re-wait times out ends failed",
			maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict}, rebaseErr: errConflict, setup: pollTimesOut,
			want:     failedLanding("CI did not reach green after force-push on " + testPR + " (ci-timeout: CI-watch deadline reached after 0s)"),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantComments: []string{"(ci-timeout: CI-watch deadline reached after 0s) — no green PR exists at the current head"},
			wantResolves: 1, wantRebases: 1,
		},
		{
			name:      "a red re-wait draws a fix pass and the fixed head merges",
			maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict},
			checks: []forge.RollupState{green, green, red, green, green},
			want:   merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: mergedAfterFix,
			wantSteps: []Step{prStep(StatusPostForcePushRed, ""), fixPass(1, 2)},
			wantFixes: 1, wantRebases: 1,
		},
		{
			name:      "a red re-wait after a conflict-resolve draws a fix pass and merges",
			maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			checks: []forge.RollupState{green, green, red, green, green},
			want:   merged, wantLabels: complete, wantMerged: testPR, wantCalls: mergedAfterFix,
			wantSteps:    []Step{prStep(StatusConflictResolve, ""), prStep(StatusPostForcePushRed, ""), fixPass(1, 2)},
			wantResolves: 1, wantFixes: 1, wantRebases: 1,
		},
		{
			name:      "the re-wait fix pass shares the budget the pre-merge loop spent",
			maxRebase: 3, maxFix: 1, mergeErrs: []error{errConflict},
			checks: []forge.RollupState{red, green, green, red},
			want:   failedLanding(wentRed), prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantComments: []string{"1 of 1 fix pass(es) used, 0 on the force-pushed head"},
			wantFixes:    1, wantRebases: 1,
		},
		{
			name:      "a fix that leaves the force-pushed head red ends landing-failed",
			maxRebase: 3, maxFix: 1, mergeErrs: []error{errConflict},
			checks: []forge.RollupState{green, green, red, red},
			want:   failedLanding(wentRed), prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantComments: []string{"fix budget exhausted (1 of 1 fix pass(es) used, 1 on the force-pushed head) — no green PR"},
			wantFixes:    1, wantRebases: 1,
		},
		{
			name:      "each force-push episode counts only its own fix passes",
			maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict, errConflict},
			checks: []forge.RollupState{green, green, red, green, green, red, red},
			want:   failedLanding(wentRed), prClosed: true, wantLabels: failed, wantNoLabels: complete,
			wantCalls:    append(append([]string(nil), mergedAfterFix...), "MarkDraft:"+testPR),
			wantComments: []string{"2 of 2 fix pass(es) used, 1 on the force-pushed head"},
			wantFixes:    2, wantRebases: 2,
		},
		{
			name:      "a stale-base preflight whose re-wait is red draws a fix pass and merges",
			maxRebase: 3, maxFix: 2,
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				c.PreflightStaleBase = true
				fc.SetNeedsUpdate(testPR, true)
			},
			checks: []forge.RollupState{green, green, red, green, green, green, green},
			want:   merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR,
			wantCalls: []string{"MarkReady:" + testPR, "MarkReady:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR},
			wantSteps: []Step{prStep(StatusStaleBaseRebase, "attempt=1/3"), prStep(StatusPostForcePushRed, ""), fixPass(1, 2)},
			wantFixes: 1, wantRebases: 2,
		},
		{
			name:  "a failed relay after a conflict-resolve ends failed without a fix pass",
			shape: shapeReadOnly, maxRebase: 3, maxFix: 2, mergeErrs: []error{errConflict}, rebaseErr: errConflict,
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				c.OutboxDir = func(num string) string { return "/outbox/" + num }
				fc.RelayBundleErr = errors.New("bundle missing")
			},
			want:     failedLanding("relay after conflict-resolve failed: relay bundle failed: bundle missing"),
			prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: conflicted,
			wantComments: []string{"relay bundle failed: bundle missing — no green PR exists at the current head"},
			wantResolves: 1, wantRebases: 1,
		},
		{
			name:      "a conflict that exhausts the rebase budget before any force-push keeps the issue complete",
			mergeErrs: []error{errConflict},
			want:      prSettlement(dispatchrecord.ReasonMergeBlocked), wantLabels: complete, wantNoLabels: failed, wantCalls: conflicted,
			wantComments: []string{"merge blocked after green CI: merge conflict"},
		},
		{
			name:      "the issue stays in progress through a conflict-resolve",
			maxRebase: 3, mergeErrs: []error{errConflict, nil}, rebaseErr: errConflict,
			checks: []forge.RollupState{green, green, green, green},
			want:   merged, wantLabels: complete, wantMerged: testPR, wantCalls: mergedAfterFix,
			wantResolves: 1, wantRebases: 1, dsetup: snapshotLabels,
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				if !containsLabel(midResolve, "agent-in-progress") || containsLabel(midResolve, "agent-complete") {
					t.Errorf("labels as the conflict-resolve Box started = %v, want agent-in-progress and not agent-complete", midResolve)
				}
				swaps := 0
				for _, call := range fc.TransitionStateCalls {
					if call.To == forge.Complete {
						swaps++
					}
				}
				if swaps != 1 {
					t.Errorf("InProgress->Complete swaps = %d, want 1: %+v", swaps, fc.TransitionStateCalls)
				}
			},
		},
	}
}

// handOffExit is one of the four exits that take a green PR away from the merge
// mode and hand it to a human, with the reason it settles.
type handOffExit struct {
	name, reason string
	guard        string
	files        []string
	labels       []string
	filesErr     error
	issueErr     error
}

var handOffExits = []handOffExit{
	{name: "guard-hit", reason: dispatchrecord.ReasonMergeGuardHit, guard: ".github/**", files: []string{".github/workflows/ci.yml"}},
	{name: "guard-check-error", reason: dispatchrecord.ReasonMergeGuardCheckError, guard: ".github/**", filesErr: errors.New("gh api pulls files: 403 Forbidden")},
	{name: "merge-by-hand", reason: dispatchrecord.ReasonMergeByHand, labels: []string{"agent-in-progress", "agent-tuning-finding"}},
	{name: "merge-by-hand-check-error", reason: dispatchrecord.ReasonMergeGuardCheckError, issueErr: errors.New("gh issue view: 502")},
}

// row turns tc into a green PR held at the exit, under immediate unless tc says
// otherwise, with the forge reporting tc.mergeable.
func (e handOffExit) row(name string, tc prCase) prCase {
	tc.name = e.name + "/" + name
	tc.tracker = backend.GitHub
	tc.guard, tc.files, tc.labels = e.guard, e.files, e.labels
	tc.maxRebase, tc.maxFix = 3, 1
	if tc.checks == nil {
		tc.checks = []forge.RollupState{green, green, green, green}
	}
	if tc.want == (Settlement{}) && !tc.abandoned {
		tc.want = prSettlement(e.reason)
	}
	inner := tc.setup
	tc.setup = func(t *testing.T, fc *forge.Fake, c *Config) {
		fc.PRFilesErr, fc.IssueErr = e.filesErr, e.issueErr
		if inner != nil {
			inner(t, fc, c)
		}
	}
	return tc
}

func mergeableReads(n int) func(*testing.T, *forge.Fake, *dispatch.Fake) {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if fc.MergeableCalls != n {
			t.Errorf("Mergeable read %d times, want %d", fc.MergeableCalls, n)
		}
	}
}

// handOffCases are the rows where a hold hands a green PR to a human: under
// immediate it first makes sure the PR still merges into its base (issue #6368).
func handOffCases() []prCase {
	flipOnly := []string{"MarkReady:" + testPR}
	respond := []string{"MarkReady:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR}
	blocked := []string{"MarkReady:" + testPR, "MarkDraft:" + testPR}
	complete := []string{"agent-complete"}
	failed := []string{"agent-failed"}
	holdNote := []string{"review and merge by hand"}

	var cases []prCase
	for _, e := range handOffExits {
		for _, tc := range []struct {
			name      string
			rebaseErr error
			resolves  int
		}{{"conflict/clean-rebase", nil, 0}, {"conflict/rebase-conflict", errConflict, 1}} {
			cases = append(cases, e.row(tc.name, prCase{
				mergeable: forge.MergeableConflicting, rebaseErr: tc.rebaseErr,
				wantLabels: complete, wantNoLabels: failed, wantCalls: respond, wantPRComments: holdNote,
				wantResolves: tc.resolves, wantRebases: 1, check: mergeableReads(2),
			}))
		}

		for _, tc := range []struct {
			name      string
			mergeable forge.MergeableState
			err       error
			step      []Step
			reads     int
		}{
			{"mergeable", forge.MergeableMergeable, nil, nil, 1},
			{"unknown", forge.MergeableUnknown, nil, []Step{{Key: "1", Landing: testPR, Status: StatusMergeableUnknown, Detail: "~~ handing off without a conflict check"}}, mergeableUnknownPolls},
			{"error", forge.MergeableUnknown, errors.New("gh api pulls: 502"), []Step{{Key: "1", Landing: testPR, Status: StatusMergeableCheckError, Detail: "!! gh api pulls: 502"}}, 1},
		} {
			cases = append(cases, e.row("not-conflicting/"+tc.name, prCase{
				mergeable: tc.mergeable, wantSteps: tc.step,
				setup:      func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.MergeableErr = tc.err },
				wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly, wantPRComments: holdNote,
				check: mergeableReads(tc.reads),
			}))
		}

		wentRed := "CI went red after force-push on " + testPR
		for _, tc := range []struct {
			name        string
			setup       func(fc *forge.Fake, c *Config)
			resolveErr  error
			wantNote    string
			wantComment string
			resolves    int
			rebases     int
			fixes       int
		}{
			{
				name:       "resolve Box fails",
				setup:      func(fc *forge.Fake, _ *Config) { fc.RebaseErr = errConflict },
				resolveErr: errors.New("box exited 1"),
				wantNote:   "conflict-resolve dispatch failed: box exited 1", wantComment: "box exited 1 — no green PR exists at the current head",
				resolves: 1, rebases: 1,
			},
			{
				name: "resolved head red with no fix budget",
				setup: func(fc *forge.Fake, c *Config) {
					c.MaxFixAttempts = 0
					fc.SetCheckStates(testPR, []forge.RollupState{green, green, red})
				},
				wantNote: wentRed, wantComment: "no fix passes allowed (MAX_FIX_ATTEMPTS=0)", rebases: 1,
			},
			{
				name: "resolved head red after its fix pass",
				setup: func(fc *forge.Fake, _ *Config) {
					fc.RebaseErr = errConflict
					fc.SetCheckStates(testPR, []forge.RollupState{green, green, red, red, red, red})
				},
				wantNote: wentRed, wantComment: "fix budget exhausted (1 of 1 fix pass(es) used, 1 on the force-pushed head)",
				resolves: 1, rebases: 1, fixes: 1,
			},
		} {
			cases = append(cases, e.row("conflict/failed-response/"+tc.name, prCase{
				mergeable: forge.MergeableConflicting, resolveErr: tc.resolveErr,
				want: failedLanding(tc.wantNote), prClosed: true, wantLabels: failed, wantNoLabels: complete, wantCalls: blocked,
				setup:        func(_ *testing.T, fc *forge.Fake, c *Config) { tc.setup(fc, c) },
				wantComments: []string{tc.wantComment}, wantResolves: tc.resolves, wantRebases: tc.rebases, wantFixes: tc.fixes,
			}))
		}

		for _, tc := range []struct {
			name       string
			setup      func(*forge.Fake, *Config)
			wantRebase int
		}{
			{"no rebase attempts", func(_ *forge.Fake, c *Config) { c.MaxRebaseAttempts = 0 }, 0},
			{"rebase fails without a conflict", func(fc *forge.Fake, _ *Config) { fc.RebaseErr = errors.New("non-fast-forward") }, 1},
		} {
			cases = append(cases, e.row("conflict/unresolvable/"+tc.name, prCase{
				mergeable: forge.MergeableConflicting, want: prSettlement(dispatchrecord.ReasonMergeBlocked),
				setup:      func(_ *testing.T, fc *forge.Fake, c *Config) { tc.setup(fc, c) },
				wantLabels: complete, wantNoLabels: failed, wantCalls: blocked, wantPRComments: holdNote,
				wantComments: []string{"merge blocked after green CI"}, wantRebases: tc.wantRebase,
			}))
		}

		cases = append(cases, e.row("conflict/resolve-already-running", prCase{
			mergeable: forge.MergeableConflicting, rebaseErr: errConflict,
			resolveErr: fmt.Errorf("resolve conflict: %w", runner.ErrAlreadyRunning),
			abandoned:  true, wantCalls: blocked, wantResolves: 1, wantRebases: 1,
		}))

		for _, mode := range []string{"manual", "auto"} {
			cases = append(cases, e.row(mode+"/conflicting-verdict-unread", prCase{
				mergeMode: mode, mergeable: forge.MergeableConflicting,
				wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly, wantPRComments: holdNote,
				check: mergeableReads(0),
			}))
		}

		cases = append(cases, e.row("draft-unmergeable-forge", prCase{
			mergeable:  forge.MergeableMergeable,
			setup:      func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.DraftUnmergeable = true; fc.SetDraft(testPR, true) },
			wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly, wantPRComments: holdNote,
		}))

		for _, tc := range []struct {
			name   string
			steady forge.MergeableState
			after  int
			err    error
			step   Step
			reads  int
		}{
			{"mergeable", forge.MergeableMergeable, 0, nil, Step{Key: "1", Landing: testPR, Status: StatusConflictUnconfirmed, Detail: "~~ handing off without a conflict response"}, 2},
			{"unknown", forge.MergeableUnknown, 0, nil, Step{Key: "1", Landing: testPR, Status: StatusMergeableUnknown, Detail: "~~ handing off without a conflict response"}, 1 + mergeableUnknownPolls},
			{"error", forge.MergeableMergeable, 1, errors.New("gh api pulls: 502"), Step{Key: "1", Landing: testPR, Status: StatusMergeableCheckError, Detail: "!! gh api pulls: 502"}, 2},
		} {
			cases = append(cases, e.row("conflict/unrepeated/"+tc.name, prCase{
				mergeable: tc.steady, wantSteps: []Step{tc.step},
				setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
					fc.SetMergeableStates(testPR, []forge.MergeableState{forge.MergeableConflicting})
					fc.MergeableErr, fc.MergeableErrAfter = tc.err, tc.after
				},
				wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly, wantPRComments: holdNote,
				check: mergeableReads(tc.reads),
			}))
		}
	}

	guardHit := handOffExits[0]
	cases = append(cases,
		guardHit.row("conflict/red-resolved-head-draws-a-fix-pass", prCase{
			mergeable:  forge.MergeableConflicting,
			checks:     []forge.RollupState{green, green, red, green, green, green, green},
			wantLabels: complete, wantNoLabels: failed, wantFixes: 1, wantRebases: 2, wantPRComments: holdNote,
			wantCalls: []string{"MarkReady:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR},
		}),
		guardHit.row("conflict/confirmed-after-unknown", prCase{
			mergeable: forge.MergeableMergeable,
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.SetMergeableStates(testPR, []forge.MergeableState{
					forge.MergeableConflicting, forge.MergeableUnknown, forge.MergeableConflicting,
				})
			},
			wantLabels: complete, wantNoLabels: failed, wantRebases: 1, wantPRComments: holdNote, wantCalls: respond,
		}),
	)
	return cases
}
