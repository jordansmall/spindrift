package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

// ciStateCases are the rows where the CI rollup sequence the gate polls is the
// subject: which states it confirms green, which draw a fix pass, and which
// end the landing without one (issue #757: the gate swaps no label itself, so
// every row also pins the labels the settled landing leaves).
func ciStateCases() []prCase {
	merged := prSettlement(dispatchrecord.ReasonMerged)
	complete := []string{"agent-complete"}
	failed := []string{"agent-failed"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	pending := forge.StatePending
	timedOut := "landing failed: ci-timeout: CI-watch deadline reached after 0s"
	errForbidden := errors.New("gh api graphql: 403 Forbidden")
	pollTimesOut := func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = 0 }
	withTimeout := func(n int) func(*testing.T, *forge.Fake, *Config) {
		return func(_ *testing.T, _ *forge.Fake, c *Config) { c.MergePollTimeout = n }
	}
	checkErrs := func(errs ...error) func(*testing.T, *forge.Fake, *Config) {
		return func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetCheckStateErrors(testPR, errs) }
	}
	// An API error must not be silently dropped as StateNone.
	checkErrored := func(c prCase) prCase {
		c.want = settledAs(dispatchrecord.ReasonGateTerminal, "ci-check-error: "+errForbidden.Error())
		c.prClosed, c.wantLabels, c.wantNoLabels = true, failed, complete
		c.wantComments = []string{"landing failed: ci-check-error: " + errForbidden.Error()}
		c.wantSteps = []Step{
			prStep(StatusCheckStateError, "!! "+errForbidden.Error()),
			prStep(StatusGateTerminal, "!! ci-check-error: "+errForbidden.Error()),
		}
		return c
	}
	// adoptedMerges is an adopted PR the registration guard lets through.
	adoptedMerges := func(c prCase) prCase {
		c.entry = entryAdopted
		c.want, c.wantLabels, c.wantNoLabels, c.wantMerged, c.wantCalls = merged, complete, failed, testPR, flipThenMerge
		return c
	}

	return []prCase{
		{
			name: "a rollup that never reports a check times out like a pending one", checks: []forge.RollupState{forge.StateNone},
			setup: pollTimesOut, prClosed: true, want: settledAs(dispatchrecord.ReasonGateTerminal, "ci-timeout: CI-watch deadline reached after 0s"),
			wantSteps:  []Step{prStep(StatusGateTerminal, "!! ci-timeout: CI-watch deadline reached after 0s")},
			wantLabels: failed, wantNoLabels: complete, wantComments: []string{timedOut},
		},
		{
			// A partial check snapshot can briefly show SUCCESS before every job
			// registers, so the gate confirms with a second poll.
			name: "a success that turns failure in the confirmation poll draws a fix pass", maxFix: 1, checks: []forge.RollupState{green, red, green, green},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
			wantSteps: []Step{fixPassStep(1, 1)}, wantFixes: 1,
		},
		{
			// A pending confirmation means another check registered but has not
			// settled, so the gate keeps waiting instead of calling it green.
			name: "a success that turns pending in the confirmation poll keeps waiting", checks: []forge.RollupState{green, pending, green, green},
			want: merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		checkErrored(prCase{
			name: "a check-state error on the first poll ends the landing", setup: checkErrs(errForbidden),
		}),
		checkErrored(prCase{
			name: "a check-state error on the confirmation poll ends the landing", setup: checkErrs(nil, errForbidden),
		}),

		// issue #1652: an unchanged head SHA can carry a terminal SUCCESS
		// inherited from an earlier run, so an adopted PR is not trusted until a
		// non-terminal state proves this run's checks are alive.
		adoptedMerges(prCase{
			name:   "adopted: a stale success is waited out until a fresh registration appears",
			checks: []forge.RollupState{green, green, pending, green, green},
		}),
		// issue #2475: the window is a bounded poll count (3 intervals), not a
		// side effect of a tight deadline, and is clamped to a smaller one.
		adoptedMerges(prCase{
			name:   "adopted: a settled success is taken when the deadline equals the registration window",
			checks: []forge.RollupState{green, green, green, green, green}, setup: withTimeout(3),
		}),
		adoptedMerges(prCase{
			name:   "adopted: a settled success is taken when the deadline is shorter than the registration window",
			checks: []forge.RollupState{green, green, green, green, green}, setup: withTimeout(2),
		}),
		// issue #2476: the generic reason when a real poll, not the window,
		// cleared the guard.
		{
			name: "adopted: a timeout after a genuine pending poll carries the generic reason", entry: entryAdopted,
			checks: []forge.RollupState{pending}, setup: pollTimesOut,
			prClosed: true, want: settledAs(dispatchrecord.ReasonGateTerminal, "ci-timeout: CI-watch deadline reached after 0s"),
			wantLabels: failed, wantNoLabels: complete, wantComments: []string{timedOut},
		},
	}
}
