package settle

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/usage"
)

// fixPassStep is the progress step announcing fix pass n of of.
func fixPassStep(n, of int) Step {
	return stepAt(testPR, "", fmt.Sprintf("fix-pass=%d/%d", n, of))
}

// settledAs is the Settlement of a PR landing that failed for reason with note.
func settledAs(reason, note string) Settlement {
	s := prSettlement(reason)
	s.Note = note
	return s
}

// fixSummaries checks the CI failure summaries the fix passes were handed.
func fixSummaries(want ...string) checkFn {
	return func(t *testing.T, _ *forge.Fake, d *dispatch.Fake) {
		t.Helper()
		var got []string
		for _, call := range d.FixCalls {
			got = append(got, call.CIFailureSummary)
		}
		if !slices.Equal(got, want) {
			t.Errorf("fix passes' CI_FAILURE_SUMMARY = %q, want %q", got, want)
		}
	}
}

// fixWarnings is what the fix passes recorded to the sidecar: the initial
// settle's own record comes first and is not a fix pass's.
func fixWarnings(d *dispatch.Fake) [][]string {
	if len(d.RecordedWarnings) == 0 {
		return nil
	}
	return d.RecordedWarnings[1:]
}

// fixCases are the rows where a red CI draws fix passes: the passes a Box is
// dispatched for, what each one's outcome settles, and the bundle a read-only
// fix Box hands back through the outbox.
func fixCases() []prCase {
	failed := []string{"agent-failed"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	redGreen := []forge.RollupState{red, green, green}
	reds := []forge.RollupState{red, red, red, red}
	outbox := func(_ *testing.T, _ *forge.Fake, c *Config) {
		c.OutboxDir = func(num string) string { return "/outbox/" + num }
	}
	unmoved := []string{"sha-1", "sha-1", "sha-1", "sha-1"}
	fixFailed := settledAs(dispatchrecord.ReasonFixFailed, "fix-failed: fix pass 1 exited non-zero")
	failedComment := []string{"fix pass 1 exited non-zero — aborting self-heal"}
	noOpComment := []string{"fix pass 1 produced no new commit — aborting self-heal"}
	fixNoOp := settledAs(dispatchrecord.ReasonFixNoOp, "fix-no-op: fix pass 1 produced no new commit")
	relayFailed := settledAs(dispatchrecord.ReasonRelayFailed, "relay-failed: fix pass 1's bundle relay failed; its commits never reached the PR")
	relays := func(want ...forge.RelayBundleCall) checkFn {
		return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
			t.Helper()
			if !slices.Equal(fc.RelayBundleCalls, want) {
				t.Errorf("RelayBundle calls = %+v, want %+v", fc.RelayBundleCalls, want)
			}
		}
	}
	oneRelay := forge.RelayBundleCall{OutboxDir: "/outbox/1", Ref: handoffBranch}

	// The clock the fix-no-op confirm pause must sleep through (issue #2502).
	var sleeps *[]time.Duration
	var pollInterval time.Duration

	// budgeted caps the run's tokens and USD and scripts the cumulative usage
	// the fix-pass gate reads (issue #2001): a zero cap leaves that dimension off.
	budgeted := func(c prCase, maxTokens int, maxUSD float64, used usage.Usage) prCase {
		c.setup = func(_ *testing.T, _ *forge.Fake, cfg *Config) {
			cfg.MaxBudgetTokens, cfg.MaxBudgetUSD = maxTokens, maxUSD
		}
		c.scriptDispatcher = func(d *dispatch.Fake, _ *forge.Fake) { d.CumulativeUsageResult = used }
		return c
	}
	// overBudget is a red run whose usage met a cap: no fix pass is dispatched
	// although MAX_FIX_ATTEMPTS would still allow one.
	overBudget := func(c prCase, reason string) prCase {
		c.maxFix, c.checks = 3, []forge.RollupState{red}
		c.wantPRClosed, c.wantLabels, c.wantNoLabels = true, failed, complete
		c.want = settledAs(dispatchrecord.ReasonBudgetExhausted, "budget-exhausted: "+reason)
		c.wantStatuses = []Status{detailOnly, StatusBudgetExhausted, StatusFailed}
		c.wantStepsLike = []Step{stepAt(testPR, StatusBudgetExhausted, "!! "+reason)}
		c.wantComments = []string{"budget exhausted (" + reason + ") — stopping self-heal before another fix pass"}
		return c
	}

	return []prCase{
		{
			name: "a green first poll draws no fix pass", maxFix: 3,
			wantStatuses: []Status{detailOnly, StatusVerifiedMerged},
			want:         merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		{
			name: "a red poll hands the fix pass the forge's failure detail", maxFix: 3, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			setup:        func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetFailureDetail(testPR, "lint: FAILURE\n2 errors") },
			want:         merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{fixPassStep(1, 3)}, wantFixes: 1, check: fixSummaries("lint: FAILURE\n2 errors"),
		},
		{
			name: "a failure detail that errors still dispatches the fix pass with no summary", maxFix: 3, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFailureDetailUnavailable, StatusVerifiedMerged},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.FailureDetailErr = errors.New("gh api graphql: 403 Forbidden")
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{stepAt(testPR, StatusFailureDetailUnavailable, "!! gh api graphql: 403 Forbidden"), fixPassStep(1, 3)},
			wantFixes: 1, check: fixSummaries(""),
		},
		{
			name: "a head SHA that errors still dispatches the fix pass with its no-op check suppressed", maxFix: 3, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusHeadShaUnavailable, StatusVerifiedMerged},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.HeadCommitSHAErr = errors.New("gh api: 502 Bad Gateway")
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{stepAt(testPR, StatusHeadShaUnavailable, "!! gh api: 502 Bad Gateway"), fixPassStep(1, 3)},
			wantFixes: 1,
		},
		{
			name: "an error rollup draws a fix pass like a failure", maxFix: 1, checks: []forge.RollupState{forge.StateError, green, green},
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			want:         merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{fixPassStep(1, 1)}, wantFixes: 1,
		},
		{
			name: "a red poll with no fix budget ends ci-red", maxFix: 0, checks: []forge.RollupState{red},
			wantStatuses: []Status{detailOnly, StatusFailed},
			wantPRClosed: true, want: settledAs(dispatchrecord.ReasonCIRed, "ci-red: still red after exhausting 0 fix pass(es)"),
			wantSteps:  []Step{stepAt(testPR, StatusFailed, "!! ci-red: still red after exhausting 0 fix pass(es)")},
			wantLabels: failed, wantNoLabels: complete,
		},
		{
			name: "a red poll that outlasts the fix budget ends fix-exhausted", maxFix: 2, checks: []forge.RollupState{red, red, red},
			wantStatuses: []Status{detailOnly, detailOnly, detailOnly, StatusFixExhausted, StatusFailed},
			wantPRClosed: true, want: settledAs(dispatchrecord.ReasonFixExhausted, "ci-red: still red after exhausting 2 fix pass(es)"),
			wantSteps: []Step{
				fixPassStep(1, 2), fixPassStep(2, 2),
				stepAt(testPR, StatusFixExhausted, "!! exhausted 2 fix pass(es)"),
				stepAt(testPR, StatusFailed, "!! ci-red: still red after exhausting 2 fix pass(es)"),
			},
			wantLabels: failed, wantNoLabels: complete, wantFixes: 2,
		},
		{
			name: "a pending rollup that times out draws no fix pass", maxFix: 3, checks: []forge.RollupState{forge.StatePending},
			wantStatuses: []Status{detailOnly, StatusGateTerminal, StatusFailed},
			setup:        func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 0 },
			wantPRClosed: true, want: settledAs(dispatchrecord.ReasonGateTerminal, "ci-timeout: CI-watch deadline reached after 0s"),
			wantSteps:  []Step{stepAt(testPR, StatusGateTerminal, "!! ci-timeout: CI-watch deadline reached after 0s")},
			wantLabels: failed, wantNoLabels: complete,
			wantComments: []string{"landing failed: ci-timeout: CI-watch deadline reached after 0s"},
		},
		overBudget(budgeted(prCase{name: "exhausted tokens stop before a fix pass"}, 100, 0, usage.Usage{InputTokens: 150}),
			"150 tokens >= cap 100"),
		overBudget(budgeted(prCase{name: "exhausted USD stops before a fix pass with tokens uncapped"}, 0, 1.00, usage.Usage{TotalCostUSD: 4.44}),
			"$4.4400 >= cap $1.0000"),
		budgeted(prCase{
			name: "a run under both caps dispatches its fix pass", maxFix: 3, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			want:         merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantFixes: 1,
		}, 1000, 10.00, usage.Usage{InputTokens: 100, TotalCostUSD: 0.50}),
		budgeted(prCase{
			name: "unset caps never stop a fix pass whatever the usage", maxFix: 2, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			want:         merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantFixes: 1,
		}, 0, 0, usage.Usage{InputTokens: 999999999, TotalCostUSD: 999999.0}),
		{
			name: "a failed fix pass stops without re-polling the unchanged head", maxFix: 3, checks: reds,
			wantStatuses:     []Status{detailOnly, detailOnly, StatusFixFailed, StatusFailed},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) { d.FixResult = dispatch.Failed(dispatch.Result{}) },
			want:             fixFailed, wantPRClosed: true, wantLabels: failed, wantNoLabels: complete, wantFixes: 1, wantComments: failedComment,
			wantSteps:  []Step{stepAt(testPR, StatusFixFailed, "!! fix pass 1 exited non-zero — aborting self-heal")},
			wantStderr: []string{},
		},
		{
			name: "a failed fix pass surfaces why its Box never launched", maxFix: 3, checks: reds,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFixFailed, StatusFailed},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) {
				d.FixResult = dispatch.Failed(dispatch.Result{Err: errors.New("box never launched: registry proxy dial failed")})
			},
			want: fixFailed, wantPRClosed: true, wantLabels: failed, wantFixes: 1, wantComments: failedComment,
			wantStderr: []string{"    ?? #1: box never launched: registry proxy dial failed\n"},
		},
		{
			name: "a fix pass skipped for a live run abandons", maxFix: 3, checks: reds,
			wantStatuses:     []Status{detailOnly, detailOnly, StatusAlreadyInFlight},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) { d.FixResult = dispatch.Skipped() },
			wantAbandoned:    true, wantFixes: 1,
			wantSteps: []Step{stepAt(testPR, StatusAlreadyInFlight, "~~ fix pass 1 skipped; live run continues")},
		},
		{
			name: "a fix pass that pushes nothing ends fix-no-op through the injected clock", maxFix: 3, checks: reds,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFixNoOp, StatusFailed},
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				fc.SetHeadCommitSHAs(testPR, []string{"sha-unchanged", "sha-unchanged", "sha-unchanged"})
				sleeps, c.Clock = recordingClock()
				pollInterval = time.Duration(c.MergePollInterval) * time.Second
			},
			want: fixNoOp, wantPRClosed: true, wantLabels: failed, wantNoLabels: complete, wantFixes: 1, wantComments: noOpComment,
			wantSteps: []Step{stepAt(testPR, StatusFixNoOp, "!! fix pass 1 produced no new commit — aborting self-heal")},
			check: func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
				if want := []time.Duration{pollInterval}; !slices.Equal(*sleeps, want) {
					t.Errorf("clock sleeps = %v, want %v (no-op-confirm pause must sleep through the clock)", *sleeps, want)
				}
			},
		},
		{
			name: "a stale head read right after the fix is confirmed against a re-read", maxFix: 3, checks: redGreen,
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			setup: func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.SetHeadCommitSHAs(testPR, []string{"sha-a", "sha-a", "sha-b"})
			},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, wantFixes: 1,
		},
		{
			name: "a fix pass's warnings and rejections are recorded tagged with the pass", maxFix: 3, checks: reds,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFixFailed, StatusFailed},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) {
				d.FixResult = dispatch.Failed(dispatch.Result{
					Warnings:             []string{"pass-manifest scan: boom"},
					IssueIntentsRejected: outcome.Rejections{Malformed: 2},
				})
			},
			want: fixFailed, wantPRClosed: true, wantLabels: failed, wantFixes: 1, wantComments: failedComment,
			wantStderr: []string{"    ?? #1: fix pass 1: 2 malformed issue-intent line(s) rejected\n"},
			check: func(t *testing.T, _ *forge.Fake, d *dispatch.Fake) {
				want := [][]string{{"fix pass 1: pass-manifest scan: boom", "fix pass 1: 2 malformed issue-intent line(s) rejected"}}
				if got := fixWarnings(d); !reflect.DeepEqual(got, want) {
					t.Errorf("fix passes' recorded warnings = %q, want %q", got, want)
				}
			},
		},
		{
			// An empty batch would still delete an earlier run's sidecar when this
			// Dispatch recorded none before it (an adopted settle).
			name: "a clean fix pass records nothing", maxFix: 3, checks: reds,
			wantStatuses:     []Status{detailOnly, detailOnly, StatusFixFailed, StatusFailed},
			scriptDispatcher: func(d *dispatch.Fake, _ *forge.Fake) { d.FixResult = dispatch.Failed(dispatch.Result{}) },
			want:             fixFailed, wantPRClosed: true, wantLabels: failed, wantFixes: 1, wantComments: failedComment,
			check: func(t *testing.T, _ *forge.Fake, d *dispatch.Fake) {
				if got := fixWarnings(d); len(got) != 0 {
					t.Errorf("fix passes' recorded warnings = %q, want none", got)
				}
			},
		},

		// A read-only fix Box holds no push-capable token and bundles its work
		// instead (issue #1979): the relay must land it before CI is re-polled.
		{
			name: "a read-only fix pass relays its bundle before CI is re-polled", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusVerifiedMerged},
			maxFix:       3, checks: redGreen, setup: outbox,
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{fixPassStep(1, 3)}, wantFixes: 1, check: relays(oneRelay),
		},
		{
			// A read-only Box's direct-push visibility never moves, so the relay
			// must run ahead of the no-op check or every pass reads as a no-op.
			name: "a read-only fix pass relays before the no-op check reads the head", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFixNoOp, StatusFailed},
			maxFix:       3, checks: redGreen,
			setup: func(t *testing.T, fc *forge.Fake, c *Config) {
				outbox(t, fc, c)
				fc.SetHeadCommitSHAs(testPR, unmoved)
			},
			want: fixNoOp, wantPRClosed: true, wantLabels: failed, wantFixes: 1, check: relays(oneRelay), wantComments: noOpComment,
		},
		{
			// Nothing to recover, so it is logged and must never park the issue.
			name: "a read-only fix pass whose bundle is missing falls through to fix-no-op", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusFixRelayFailed, StatusFixNoOp, StatusFailed},
			maxFix:       1, checks: []forge.RollupState{red, red},
			setup: func(t *testing.T, fc *forge.Fake, c *Config) {
				outbox(t, fc, c)
				fc.SetHeadCommitSHAs(testPR, unmoved)
				fc.RelayBundleErr = forge.ErrBundleNotFound
			},
			want: fixNoOp, wantPRClosed: true, wantLabels: failed, wantFixes: 1, check: relays(oneRelay), wantComments: noOpComment,
			wantSteps: []Step{stepAt(testPR, StatusFixRelayFailed, "!! relay bundle failed: "+forge.ErrBundleNotFound.Error())},
		},
		{
			// The bundle is intact in the outbox but unrelayed, so the head never
			// moved: parked for recover, not misread as a fix-no-op (issue #6074).
			name: "a read-only fix pass whose relay fails parkably parks for recover", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusRelayFailed, StatusFailed},
			maxFix:       3, checks: []forge.RollupState{red, red},
			setup: func(t *testing.T, fc *forge.Fake, c *Config) {
				outbox(t, fc, c)
				fc.SetHeadCommitSHAs(testPR, unmoved)
				fc.RelayBundleErr = errors.New("ssh auth blip")
			},
			want: relayFailed, wantPRClosed: true, wantLabels: failed, wantNoLabels: complete, wantFixes: 1,
			wantSteps:    []Step{stepAt(testPR, StatusRelayFailed, "!! fix pass 1: relay bundle failed: ssh auth blip")},
			wantComments: []string{"`spindrift recover 1`"},
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				// recover re-gates the open PR and never relays this bundle.
				for _, cc := range fc.CommentCalls {
					if strings.Contains(cc.Body, "retry relaying and landing") {
						t.Errorf("comment %q promises a relay retry recover never makes", cc.Body)
					}
				}
				relays(oneRelay)(t, fc, nil)
			},
		},
		{
			// The one parkable failure whose recover hint would hit the same wall.
			name: "a read-only fix pass refused for workflow scope parks with the scope remedy", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusRelayFailed, StatusFailed},
			maxFix:       3, checks: []forge.RollupState{red, red},
			setup: func(t *testing.T, fc *forge.Fake, c *Config) {
				outbox(t, fc, c)
				fc.SetHeadCommitSHAs(testPR, unmoved)
				fc.RelayBundleErr = fmt.Errorf("push refused: %w", forge.ErrWorkflowPermissionDenied)
			},
			want: relayFailed, wantPRClosed: true, wantLabels: failed, wantNoLabels: complete, wantFixes: 1,
			wantComments: []string{forge.WorkflowPermissionRemedy("1")},
		},
		{
			// A mark landing in the relay's final attempt is not a backoff, so the
			// retry loop returns an exhausted error rather than errAbandoned (issue
			// #3523): the park must see the mark and leave the released issue alone.
			name: "a stop during a read-only fix pass's final relay attempt abandons", shape: shapeReadOnly,
			wantStatuses: []Status{detailOnly, detailOnly, StatusRelayRetry, StatusRelayRetry, StatusRelayRetry},
			maxFix:       3, checks: []forge.RollupState{red, red},
			setup: func(t *testing.T, fc *forge.Fake, c *Config) {
				outbox(t, fc, c)
				fc.SetHeadCommitSHAs(testPR, unmoved)
				fc.RelayBundleErr = errors.New("ssh auth blip")
				c.Policy = retry.Policy{Max: 3, Unit: time.Second}
				c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}}
			},
			midCall: func(fc *forge.Fake, _ *dispatch.Fake, mark func()) {
				fc.On("RelayBundle", func(attempt int) {
					if attempt == 3+1 {
						mark()
					}
				})
			},
			wantAbandoned: true, wantFixes: 1,
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				if n := len(fc.RelayBundleCalls); n != 3+1 {
					t.Errorf("RelayBundle calls = %d, want %d", n, 3+1)
				}
			},
		},
	}
}
