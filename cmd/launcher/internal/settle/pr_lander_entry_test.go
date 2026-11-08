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
	mergedOutcome := &dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: "1", Landing: "ignored", Status: "merged", Note: "ok"},
	}}
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
		// A self-reported merged status is verified against the forge, never trusted.
		{
			name:       "a merged outcome whose PR is still open fails",
			want:       Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeUnverified), Note: "PR state is 'OPEN', expected MERGED"},
			labels:     []string{"agent-in-progress", "agent-complete"},
			result:     mergedOutcome,
			setup:      func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetPRState(testPR, forge.PROpen) },
			wantLabels: []string{"agent-failed"}, prClosed: true,
			wantStepsLike: []Step{prStep(StatusFailed, "expected MERGED")},
		},
		{
			name:       "a merged outcome whose issue lacks the complete label fails",
			want:       Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeUnverified), Note: "issue does not carry 'agent-complete'"},
			result:     mergedOutcome,
			setup:      func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetPRState(testPR, forge.PRMerged) },
			wantLabels: []string{"agent-failed"}, prClosed: true,
			wantStepsLike: []Step{prStep(StatusFailed, "does not carry")},
		},

		// With no usable outcome line, a PR on the agent branch is reported blocked
		// and never adopted, merged or relabelled; without one the run is missing.
		{
			name: "no outcome line with no PR is reported missing and fails the issue", result: &dispatch.Result{}, noPR: true,
			want:     Settlement{Verdict: ForReason(dispatchrecord.ReasonNoOutcome), Note: "no outcome in log  class=  reason="},
			prClosed: true, wantLabels: []string{"agent-failed"},
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing}},
		},
		{
			name: "a malformed outcome line with no PR is reported missing and fails the issue", result: &dispatch.Result{ParseErr: errFake}, noPR: true,
			want:     Settlement{Verdict: ForReason(dispatchrecord.ReasonNoOutcome), Note: "unparseable outcome line: fake error"},
			prClosed: true, wantLabels: []string{"agent-failed"},
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing}},
		},
		{
			name: "no outcome line with an open PR is reported blocked and never adopted", result: &dispatch.Result{},
			prClosed: true, wantNoStatus: []Status{StatusAdopted},
			wantStepsLike: []Step{prStep(StatusBlocked, "no outcome line; PR on")}, check: noChurn,
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
