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
	merged := prSettlement(dispatchrecord.ReasonMerged)
	complete := []string{"agent-complete"}
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
	bundle := func(kept bool) func(*testing.T, *forge.Fake, *dispatch.Fake) {
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
	retry := func(n int, err string) Step {
		return prStep(StatusMergeUnverifiedReadRetry, fmt.Sprintf("attempt=%d/2  !! %s", n, err))
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

	var cases []prCase
	for _, r := range reads {
		cases = append(cases,
			// A transient read error must retry, not demote a genuinely merged issue.
			prCase{
				name:  "adopted: a transient " + r.name + " read error after the merge retries, then verifies",
				entry: entryAdopted, checks: registered, setup: bundled, afterMerge: r.transient,
				want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR},
				wantSteps: []Step{retry(1, "blip"), prStep(StatusVerifiedMerged, "")}, check: bundle(false),
			},
			// The issue keeps the Complete that completeLanding latched and the
			// bundle stays, rather than be demoted or closed.
			prCase{
				name:  "adopted: a persistent " + r.name + " read error after the merge keeps the issue complete",
				entry: entryAdopted, checks: registered, setup: bundled, afterMerge: r.persistent,
				want: merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: []string{"MarkReady:" + testPR, "Merge:" + testPR},
				wantSteps:   []Step{retry(1, "api down"), retry(2, "api down"), prStep(StatusMergeUnverifiedReadError, "!! api down")},
				wantNoSteps: []Step{prStep(StatusVerifiedMerged, "")}, check: bundle(true),
			},
			// The off-script status=merged arm verifies a PR whose completeLanding
			// never ran, so the issue is still agent-in-progress: a read that keeps
			// failing must leave it there, neither demoted nor closed.
			prCase{
				name:  "a merged self-report whose " + r.name + " read keeps failing leaves the issue in progress",
				shape: shapeReadOnly, labels: []string{"agent-in-progress", "agent-complete"},
				result: &dispatch.Result{Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: "1", Landing: "ignored", Status: "merged", Note: "ok"},
				}},
				setup: func(_ *testing.T, fc *forge.Fake, c *Config) {
					c.ReadOnly = true
					c.Policy.Max = 2
					fc.SetPRState(testPR, forge.PRMerged)
					r.persistent(fc)
				},
				wantLabels: []string{"agent-in-progress"}, wantNoLabels: failed, wantCalls: []string{}, prClosed: true, // the self-report names a merged PR; there is no settlement to leave it open
				wantSteps: []Step{retry(1, "api down"), retry(2, "api down"), prStep(StatusMergeUnverifiedReadError, "!! api down")},
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
	return cases
}

func noFailedTransition(t *testing.T, fc *forge.Fake) {
	t.Helper()
	if slices.ContainsFunc(fc.TransitionStateCalls, func(c forge.TransitionStateCall) bool { return c.To == forge.Failed }) {
		t.Errorf("TransitionStateCalls = %v, want no transition to failed", fc.TransitionStateCalls)
	}
}
