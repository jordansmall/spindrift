package settle

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// readErrAfterMerge arms arm on the fake right after a successful Merge, so
// the read failure lands exactly on verifyMerged's reads, as a forge that
// merges and then drops the next API calls would.
type readErrAfterMerge struct {
	*forge.Fake
	arm func(*forge.Fake)
}

func (r readErrAfterMerge) Merge(url string) error {
	if err := r.Fake.Merge(url); err != nil {
		return err
	}
	r.arm(r.Fake)
	return nil
}

// verifyCases are the rows where the merge has happened and verifyMerged reads
// the PR and the issue back: a read that recovers verifies the merge, one that
// keeps failing proves nothing and leaves the issue as it stands.
func verifyCases() []prCase {
	failed := []string{"agent-failed"}

	var outbox string
	bundled := func(t *testing.T, _ *forge.Fake, c *Config) {
		outbox = t.TempDir()
		writeBundle(t, outbox)
		c.OutboxDir = func(string) string { return outbox }
		c.Policy.Max = 2
	}
	// The leading Pending proves the checks registered, which Recover's open-PR arm waits for.
	registered := []forge.RollupState{forge.StatePending, green, green}
	bundle := func(kept bool) checkFn {
		return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
			t.Helper()
			if got := bundleExists(t, outbox); got != kept {
				t.Errorf("bundle kept = %v, want %v", got, kept)
			}
			wantCloses := 1
			if kept {
				wantCloses = 0
			}
			if len(fc.CloseMergedIssueCalls) != wantCloses {
				t.Errorf("CloseMergedIssueCalls = %v, want %d", fc.CloseMergedIssueCalls, wantCloses)
			}
			noFailedTransition(t, fc)
		}
	}
	retry := func(landing string, n int, err string) Step {
		return stepAt(landing, StatusMergeUnverifiedReadRetry, fmt.Sprintf("attempt=%d/2  !! %s", n, err))
	}
	blip := errors.New("blip")
	down := errors.New("api down")
	reads := []struct {
		name       string
		transient  func(*forge.Fake)
		persistent func(*forge.Fake)
	}{
		{"PRState",
			func(fc *forge.Fake) { fc.PRStateErrs = []error{blip} },
			func(fc *forge.Fake) { fc.PRStateErr = down }},
		{"Issue",
			func(fc *forge.Fake) { fc.IssueErrs = []error{blip} },
			func(fc *forge.Fake) { fc.IssueErr = down }},
	}

	mergedReport := &dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: "1", Landing: "ignored", Status: "merged", Note: "ok"},
	}}

	var cases []prCase
	for _, r := range reads {
		cases = append(cases,
			// A transient read error must retry, not demote a genuinely merged issue.
			prCase{
				name:  "adopted: a transient " + r.name + " read error after the merge retries, then verifies",
				entry: entryAdopted, checks: registered, setup: bundled, afterMerge: r.transient,
				want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR},
				wantStatuses: []Status{StatusAdopted, StatusMergeUnverifiedReadRetry, StatusVerifiedMerged},
				wantSteps:    []Step{retry(testPR, 1, "blip"), stepAt(testPR, StatusVerifiedMerged, "")}, check: bundle(false),
			},
			// The issue keeps the Complete that completeLanding latched and the
			// bundle stays, rather than be demoted or closed.
			prCase{
				name:  "adopted: a persistent " + r.name + " read error after the merge keeps the issue complete",
				entry: entryAdopted, checks: registered, setup: bundled, afterMerge: r.persistent,
				want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR},
				wantStatuses: []Status{StatusAdopted, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadError},
				wantSteps:    []Step{retry(testPR, 1, "api down"), retry(testPR, 2, "api down"), stepAt(testPR, StatusMergeUnverifiedReadError, "!! api down")},
				check:        bundle(true),
			},
			// The off-script status=merged arm verifies a PR whose completeLanding
			// never ran, so the issue is still agent-in-progress: a read that keeps
			// failing must leave it there, neither demoted nor closed.
			prCase{
				name:  "a merged self-report whose " + r.name + " read keeps failing leaves the issue in progress",
				shape: shapeReadOnly, labels: []string{"agent-in-progress", "agent-complete"},
				result: mergedReport,
				setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
					c.ReadOnly = true
					c.Policy.Max = 2
					fc.SetPRState(testPR, forge.PRMerged)
					r.persistent(fc)
				},
				wantLabels: []string{"agent-in-progress"}, wantNoLabels: failed, wantCalls: []string{}, wantPRClosed: true, // the self-report names a merged PR; there is no settlement to leave it open
				wantStatuses: []Status{detailOnly, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadError},
				wantSteps:    []Step{retry(testPR, 1, "api down"), retry(testPR, 2, "api down"), stepAt(testPR, StatusMergeUnverifiedReadError, "!! api down")},
				check: func(t *testing.T, fc *forge.Fake, d *dispatch.Fake) {
					t.Helper()
					if len(fc.CloseMergedIssueCalls) != 0 {
						t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
					}
					noFailedTransition(t, fc)
				},
			},
		)
	}

	// The branch lookup that finds the PR to verify is itself a read: an error
	// from it proves nothing about whether a PR exists, so it must not demote.
	branch := forge.AgentBranchName(testBranchPrefix, "1")
	cases = append(cases,
		prCase{
			name:  "a merged self-report whose PRForBranch lookup keeps failing leaves the issue in progress",
			shape: shapeReadOnly, labels: []string{"agent-in-progress", "agent-complete"},
			result: mergedReport,
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				c.ReadOnly = true
				c.Policy.Max = 2
				fc.PRForBranchErr = down
			},
			wantLabels: []string{"agent-in-progress"}, wantNoLabels: failed, wantCalls: []string{}, wantPRClosed: true,
			wantStatuses: []Status{detailOnly, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadRetry, StatusMergeUnverifiedReadError},
			wantSteps: []Step{
				retry(branch, 1, "api down"),
				retry(branch, 2, "api down"),
				stepAt(branch, StatusMergeUnverifiedReadError, "!! api down"),
			},
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				t.Helper()
				if len(fc.TransitionStateCalls) != 0 || len(fc.CloseMergedIssueCalls) != 0 {
					t.Errorf("TransitionStateCalls = %v, CloseMergedIssueCalls = %v, want neither", fc.TransitionStateCalls, fc.CloseMergedIssueCalls)
				}
			},
		},
		prCase{
			name:  "a merged self-report whose PRForBranch lookup fails once retries, then verifies",
			shape: shapeReadOnly, labels: []string{"agent-in-progress", "agent-complete"},
			result: mergedReport,
			setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
				c.ReadOnly = true
				c.Policy.Max = 2
				fc.SetPRState(testPR, forge.PRMerged)
				fc.PRForBranchErrs = []error{blip}
			},
			wantLabels: []string{"agent-in-progress"}, wantNoLabels: failed, wantCalls: []string{}, wantPRClosed: true,
			wantStatuses: []Status{detailOnly, StatusMergeUnverifiedReadRetry, StatusVerifiedMerged},
			wantSteps:    []Step{retry(branch, 1, "blip"), stepAt(testPR, StatusVerifiedMerged, "")},
			check: func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				t.Helper()
				if len(fc.CloseMergedIssueCalls) != 1 {
					t.Errorf("CloseMergedIssueCalls = %v, want 1", fc.CloseMergedIssueCalls)
				}
				noFailedTransition(t, fc)
			},
		},
		// A lookup that succeeds and finds nothing is the one case that demotes.
		prCase{
			name:  "a merged self-report with no PR on the branch demotes to agent-failed",
			shape: shapeReadOnly, labels: []string{"agent-in-progress", "agent-complete"},
			result: mergedReport, noPR: true,
			want:       Settlement{Verdict: ForReason(dispatchrecord.ReasonNoPR), Note: "no PR found on branch to verify merge"},
			setup:      func(_ *testing.T, _ *forge.Fake, c *Config) { c.ReadOnly = true },
			wantLabels: []string{"agent-failed"}, wantCalls: []string{}, wantPRClosed: true,
			wantStatuses: []Status{detailOnly, StatusFailed},
			wantSteps:    []Step{stepAt(branch, StatusFailed, "!! no PR found on branch to verify merge")},
		},
	)
	return cases
}

func noFailedTransition(t *testing.T, fc *forge.Fake) {
	t.Helper()
	if slices.ContainsFunc(fc.TransitionStateCalls, func(c forge.TransitionStateCall) bool { return c.To == forge.Failed }) {
		t.Errorf("TransitionStateCalls = %v, want no transition to failed", fc.TransitionStateCalls)
	}
}
