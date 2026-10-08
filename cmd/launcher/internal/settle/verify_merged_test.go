package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
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

// readErrFixture is a settle that ran to a real merge and completeLanding
// before its post-merge reads failed.
type readErrFixture struct {
	fc          *forge.Fake
	outbox, out string
	readRecords func() []report.Record
}

// settleWithReadErrAfterMerge drives a green PR through SettleAdopted to a
// real merge and completeLanding, then arms a read failure for verifyMerged.
func settleWithReadErrAfterMerge(t *testing.T, arm func(*forge.Fake)) readErrFixture {
	t.Helper()
	readRecords := testutil.InstallPipeReporter(t)
	outbox := t.TempDir()
	writeBundle(t, outbox)
	c, fc := bundleFixture(outbox)
	c.MergeMode = "immediate"
	c.Policy.Max = 2
	s := newTestSettle(c, fc, readErrAfterMerge{fc, arm})

	out := captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })
	return readErrFixture{fc, outbox, out, readRecords}
}

// A transient read error must retry, not demote a genuinely merged issue.
func TestSettleAdopted_TransientReadErrorAfterMergeRetriesThenVerifies(t *testing.T) {
	for name, arm := range map[string]func(*forge.Fake){
		"PRState": func(fc *forge.Fake) { fc.PRStateErrs = []error{errors.New("blip")} },
		"Issue":   func(fc *forge.Fake) { fc.IssueErrs = []error{errors.New("blip")} },
	} {
		t.Run(name, func(t *testing.T) {
			r := settleWithReadErrAfterMerge(t, arm)
			fc, outbox, out, readRecords := r.fc, r.outbox, r.out, r.readRecords

			for _, want := range []string{"status=merge-unverified-read-retry  attempt=1/2", "status=verified-merged"} {
				if !strings.Contains(out, want) {
					t.Errorf("output must contain %q; got: %q", want, out)
				}
			}
			if len(fc.CloseMergedIssueCalls) != 1 {
				t.Errorf("CloseMergedIssueCalls = %v, want one close", fc.CloseMergedIssueCalls)
			}
			if bundleExists(t, outbox) {
				t.Errorf("a verified merge must drop the bundle")
			}
			if recs := readRecords(); len(recs) != 1 || recs[0].State != "complete" {
				t.Errorf("records = %+v, want exactly one state=complete", recs)
			}
		})
	}
}

// A read that keeps failing proves nothing about the merge: the issue must
// keep the Complete that completeLanding latched, emit no second settled
// record, and keep its bundle, rather than be demoted or closed.
func TestSettleAdopted_PersistentReadErrorAfterMergeKeepsComplete(t *testing.T) {
	for name, arm := range map[string]func(*forge.Fake){
		"PRState": func(fc *forge.Fake) { fc.PRStateErr = errors.New("api down") },
		"Issue":   func(fc *forge.Fake) { fc.IssueErr = errors.New("api down") },
	} {
		t.Run(name, func(t *testing.T) {
			r := settleWithReadErrAfterMerge(t, arm)
			fc, outbox, out, readRecords := r.fc, r.outbox, r.out, r.readRecords

			if !strings.Contains(out, "attempt=2/2") || !strings.Contains(out, "status=merge-unverified-read-error") {
				t.Errorf("want two retries then a read-error line; got: %q", out)
			}
			if strings.Contains(out, "status=failed") || strings.Contains(out, "status=verified-merged") {
				t.Errorf("an unverifiable merge must neither fail nor verify; got: %q", out)
			}
			for _, call := range fc.TransitionStateCalls {
				if call.To == forge.Failed {
					t.Errorf("TransitionStateCalls = %v, want no transition to failed", fc.TransitionStateCalls)
					break
				}
			}
			fc.IssueErr = nil // the label read below must reach the fake
			iss, _ := fc.Issue("1")
			if containsLabel(iss.Labels, "agent-failed") || !containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("labels = %v, want agent-complete kept and no agent-failed", iss.Labels)
			}
			if len(fc.CloseMergedIssueCalls) != 0 {
				t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
			}
			if !bundleExists(t, outbox) {
				t.Errorf("an unverified merge must keep the bundle")
			}
			recs := readRecords()
			if len(recs) != 1 {
				t.Fatalf("records: got %d, want exactly 1: %+v", len(recs), recs)
			}
			if recs[0].Event != "settled" || recs[0].Key != dispatchkey.Issue("1") || recs[0].State != "complete" {
				t.Errorf("record = %+v, want event=settled issue=1 state=complete", recs[0])
			}
		})
	}
}

// The off-script status=merged arm verifies a PR whose completeLanding never
// ran, so the issue is still agent-in-progress: a read that keeps failing must
// leave it there, neither demoted to Failed nor closed.
func TestSettle_MergedStatus_PersistentReadErrorLeavesIssueInProgress(t *testing.T) {
	const issNum = "1955"
	const prURL = "https://github.com/owner/repo/pull/1955"

	for name, arm := range map[string]func(*forge.Fake){
		"PRState": func(fc *forge.Fake) { fc.PRStateErr = errors.New("api down") },
		"Issue":   func(fc *forge.Fake) { fc.IssueErr = errors.New("api down") },
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress", "agent-complete"}})
			fc.SetPR(fc.AgentBranch(issNum), forge.PR{URL: prURL})
			fc.SetPRState(prURL, forge.PRMerged)
			arm(fc)

			c := baseConfig()
			c.ReadOnly = true
			c.Policy.Max = 2
			s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
			result := dispatch.Result{Resolved: outcome.Resolved{
				Found:   true,
				Outcome: outcome.Outcome{Issue: issNum, Landing: "ignored", Status: "merged", Note: "ok"},
			}}
			out := captureStdout(t, func() { s.Settle(dispatch.NewFake(), issNum, 0, result) })

			if !strings.Contains(out, "status=merge-unverified-read-error") {
				t.Errorf("want a read-error line; got: %q", out)
			}
			for _, call := range fc.TransitionStateCalls {
				if call.To == forge.Failed {
					t.Errorf("TransitionStateCalls = %v, want no transition to failed", fc.TransitionStateCalls)
					break
				}
			}
			fc.IssueErr = nil // the label read below must reach the fake
			iss, _ := fc.Issue(issNum)
			if containsLabel(iss.Labels, "agent-failed") || !containsLabel(iss.Labels, "agent-in-progress") {
				t.Errorf("labels = %v, want agent-in-progress kept and no agent-failed", iss.Labels)
			}
			if len(fc.CloseMergedIssueCalls) != 0 {
				t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
			}
		})
	}
}
