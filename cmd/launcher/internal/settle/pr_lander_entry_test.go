package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
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
			name:         "a blocked outcome's step names its landing",
			wantStatuses: []Status{detailOnly, StatusBlocked},
			result: &dispatch.Result{Resolved: outcome.Resolved{
				Found:   true,
				Outcome: outcome.Outcome{Issue: "1", Landing: testPR, Status: "blocked", Note: "tests failing; expr=1 mismatch"},
			}},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), PRURL: testPR, Note: "tests failing; expr=1 mismatch"},
			wantPRClosed: true,
			wantLabels:   []string{"agent-failed"},
			wantSteps:    []Step{stepAt(testPR, StatusBlocked, "!! tests failing; expr=1 mismatch")},
			wantComments: []string{"tests failing; expr=1 mismatch"},
		},
		// A box that mangled its outcome line but opened a PR is reported blocked,
		// not dropped as malformed with no further trace (issue #1898).
		{
			name:          "a malformed outcome line with an open PR is reported blocked and leaves the labels alone",
			wantStatuses:  []Status{StatusBlocked},
			result:        &dispatch.Result{ParseErr: errFake},
			wantPRClosed:  true,
			wantStepsLike: []Step{stepAt(testPR, StatusBlocked, "no outcome line; PR on")},
			check:         noChurn,
		},
		// A self-reported merged status is verified against the forge, never trusted.
		{
			name:         "a merged outcome whose PR is still open fails",
			wantStatuses: []Status{detailOnly, StatusFailed},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeUnverified), Note: "PR state is 'OPEN', expected MERGED"},
			labels:       []string{"agent-in-progress", "agent-complete"},
			result:       mergedOutcome,
			setup:        func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetPRState(testPR, forge.PROpen) },
			wantLabels:   []string{"agent-failed"}, wantPRClosed: true,
			wantStepsLike: []Step{stepAt(testPR, StatusFailed, "expected MERGED")},
		},
		{
			name:         "a merged outcome whose issue lacks the complete label fails",
			wantStatuses: []Status{detailOnly, StatusFailed},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeUnverified), Note: "issue does not carry 'agent-complete'"},
			result:       mergedOutcome,
			setup:        func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.SetPRState(testPR, forge.PRMerged) },
			wantLabels:   []string{"agent-failed"}, wantPRClosed: true,
			wantStepsLike: []Step{stepAt(testPR, StatusFailed, "does not carry")},
		},

		// With no usable outcome line, a PR on the agent branch is reported blocked
		// and never adopted, merged or relabelled; without one the run is missing.
		{
			name: "no outcome line with no PR is reported missing and fails the issue", result: &dispatch.Result{}, noPR: true,
			wantStatuses: []Status{StatusMissing},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonNoOutcome), Note: "no outcome in log  class=  reason="},
			wantPRClosed: true, wantLabels: []string{"agent-failed"},
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing}},
		},
		{
			name: "a malformed outcome line with no PR is reported missing and fails the issue", result: &dispatch.Result{ParseErr: errFake}, noPR: true,
			wantStatuses: []Status{StatusMissing},
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonNoOutcome), Note: "unparseable outcome line: fake error"},
			wantPRClosed: true, wantLabels: []string{"agent-failed"},
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing}},
		},
		{
			name: "no outcome line with an open PR is reported blocked and never adopted", result: &dispatch.Result{},
			wantStatuses:  []Status{StatusBlocked},
			wantPRClosed:  true,
			wantStepsLike: []Step{stepAt(testPR, StatusBlocked, "no outcome line; PR on")}, check: noChurn,
		},
		// A lookup failure leaves doubt whether a mergeable PR exists, so it is
		// reported with the log's classification and no demotion (issue #1605).
		{
			name:          "a PR lookup error on a missing outcome keeps the classification and leaves the labels alone",
			wantStatuses:  []Status{StatusMissing},
			result:        &dispatch.Result{Classification: driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}},
			noPR:          true,
			setup:         func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.OpenPRForBranchErr = errFake },
			wantPRClosed:  true,
			wantStepsLike: []Step{{Key: "1", Status: StatusMissing, Detail: "class=terminal  reason=taskFailed"}},
			check:         noChurn,
		},
	}
}

// shapeMatrixCases run the adopted entry and the other MERGE_MODEs on the
// forgejo- and local-shaped PR forges, whose trackers lack the optional
// capabilities the github shape carries: each shape x entry pair reaches a
// merged and an unmerged outcome. A local-shaped Settle already merges and
// holds in the tuning rows, and a forgejo-shaped one in the tuning-marker and
// merge-guard rows.
func shapeMatrixCases() []prCase {
	failed := []string{"agent-failed"}
	registered := []forge.RollupState{forge.StatePending, green, green}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	flipOnly := []string{"MarkReady:" + testPR}

	var cases []prCase
	for _, tc := range []struct {
		shape   prShape
		tracker backend.Descriptor
	}{{shapeForgejo, backend.Forgejo}, {shapeLocalPR, backend.Local}} {
		on := " on " + tc.tracker.Name
		cases = append(cases,
			prCase{
				name: "adopted: a green PR merges" + on, shape: tc.shape, tracker: tc.tracker, entry: entryAdopted, checks: registered,
				wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
				want:         prSettlement(dispatchrecord.ReasonMerged), wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: flipThenMerge,
			},
			prCase{
				name: "adopted: manual mode leaves a green PR unmerged" + on, shape: tc.shape, tracker: tc.tracker, entry: entryAdopted, checks: registered,
				mergeMode:    "manual",
				wantStatuses: []Status{StatusAdopted, StatusAgentComplete},
				want:         prSettlement(dispatchrecord.ReasonManual), wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly,
			},
			prCase{
				name: "adopted: a red PR fails the issue" + on, shape: tc.shape, tracker: tc.tracker, entry: entryAdopted, checks: []forge.RollupState{red},
				wantStatuses: []Status{StatusAdopted, StatusFailed},
				want:         settledAs(dispatchrecord.ReasonCIRed, "ci-red: still red after exhausting 0 fix pass(es)"), wantPRClosed: true,
				wantLabels: failed, wantNoLabels: complete,
			},
			prCase{
				name: "auto mode enqueues the auto-merge" + on, shape: tc.shape, tracker: tc.tracker, mergeMode: "auto",
				wantStatuses: []Status{detailOnly, StatusAutoMergeEnqueued},
				want:         prSettlement(dispatchrecord.ReasonAutoMergeEnqueued), wantLabels: complete, wantNoLabels: failed,
				wantCalls: []string{"MarkReady:" + testPR, "EnqueueAutoMerge:" + testPR},
			},
			prCase{
				name: "manual mode leaves a green PR unmerged" + on, shape: tc.shape, tracker: tc.tracker, mergeMode: "manual",
				wantStatuses: []Status{detailOnly, StatusAgentComplete},
				want:         prSettlement(dispatchrecord.ReasonManual), wantLabels: complete, wantNoLabels: failed, wantCalls: flipOnly,
			},
		)
	}
	return cases
}
