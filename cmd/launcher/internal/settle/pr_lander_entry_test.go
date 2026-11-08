package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// entryCases are the rows where the Box's outcome is not a plain ready: how a
// blocked, malformed or missing outcome line reads in the progress steps and
// what it leaves on the issue.
func entryCases() []prCase {
	noChurn := func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		if len(fc.TransitionStateCalls) != 0 {
			t.Errorf("label transitions = %v, want none", fc.TransitionStateCalls)
		}
	}

	return []prCase{
		// The step names the landing, not a stale pr= field: the value may not be a PR at all (issue #655).
		{
			name: "a blocked outcome's step names its landing",
			result: &dispatch.Result{Resolved: outcome.Resolved{
				Found:   true,
				Outcome: outcome.Outcome{Issue: "1", Landing: testPR, Status: "blocked", Note: "tests failing; expr=1 mismatch"},
			}},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), PRURL: testPR, Note: "tests failing; expr=1 mismatch"},
			prClosed:     true,
			wantLabels:   []string{"agent-failed"},
			wantSteps:    []Step{prStep(StatusBlocked, "!! tests failing; expr=1 mismatch")},
			wantComments: []string{"tests failing; expr=1 mismatch"},
		},
		// A box that mangled its outcome line but opened a PR is reported blocked,
		// not dropped as malformed with no further trace (issue #1898).
		{
			name:          "a malformed outcome line with an open PR is reported blocked and leaves the labels alone",
			result:        &dispatch.Result{ParseErr: errFake},
			prClosed:      true,
			wantStepsLike: []Step{prStep(StatusBlocked, "no outcome line; PR on")},
			check:         noChurn,
		},
		// A lookup failure leaves doubt whether a mergeable PR exists, so it is
		// reported with the log's classification and no demotion (issue #1605).
		{
			name:          "a PR lookup error on a missing outcome keeps the classification and leaves the labels alone",
			result:        &dispatch.Result{Classification: driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}},
			noPR:          true,
			setup:         func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.OpenPRForBranchErr = errFake },
			prClosed:      true,
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing, Detail: "class=terminal  reason=taskFailed"}},
			check:         noChurn,
		},
	}
}
