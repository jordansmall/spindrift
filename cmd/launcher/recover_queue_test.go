package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/seambundle"
)

// queueRecoverFixture is a github-shaped tracker plus a working dir whose
// outbox and logs recoverQueueOne reads. The working dir is also the process's
// cwd, because the settler resolves the outbox through os.Getwd.
type queueRecoverFixture struct {
	c  config
	fc *forge.Fake
	// tracker is what recoverQueueOne drives: fc itself by default, or fc in
	// another adapter shape.
	tracker forge.IssueTracker
	cf      forge.CodeForge
	dir     string
}

func newQueueRecoverFixture(t *testing.T) *queueRecoverFixture {
	t.Helper()
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.CreateDraftPRURL = testReconcilePR
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	dir := tempLogDir(t)
	t.Chdir(dir)
	return &queueRecoverFixture{c: c, fc: fc, tracker: fc, cf: fc.AsGithubReadOnly(), dir: dir}
}

// addFailed seeds an agent-failed issue with a bundle and a self-report line.
// An empty report writes no log at all.
func (x *queueRecoverFixture) addFailed(t *testing.T, num, report string) {
	t.Helper()
	x.fc.SetIssue(forge.Issue{Number: num, Labels: []string{x.c.failedLabel}})
	outbox := dispatch.OutboxDirFor(x.dir, num)
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, seambundle.FileName), []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report == "" {
		return
	}
	log := filepath.Join(dispatch.HostLogDirFor(x.dir), "issue-"+num+".log")
	if err := os.WriteFile(log, []byte("SPINDRIFT_OUTCOME: issue="+num+" landing="+x.fc.AgentBranch(num)+" status="+report+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (x *queueRecoverFixture) run(t *testing.T) (int, string) {
	t.Helper()
	return x.runWith(t, capsFor(x.tracker, x.cf))
}

func (x *queueRecoverFixture) runWith(t *testing.T, caps forge.Capabilities) (int, string) {
	t.Helper()
	var out strings.Builder
	err := recoverQueueOne(x.c, x.tracker, x.cf, caps, x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), &out, io.Discard)
	return exitCodeFor(err), out.String()
}

func (x *queueRecoverFixture) labels(t *testing.T, num string) []string {
	t.Helper()
	iss, err := x.fc.Issue(num)
	if err != nil {
		t.Fatalf("issue %s: %v", num, err)
	}
	return iss.Labels
}

func (x *queueRecoverFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if len(x.fc.TransitionStateCalls) != 0 || len(x.fc.CommentCalls) != 0 {
		t.Errorf("tracker written: transitions=%v comments=%v", x.fc.TransitionStateCalls, x.fc.CommentCalls)
	}
}

func TestRecoverQueueOne_ReadyBundleNoPRMergesAndCompletes(t *testing.T) {
	shapes := []struct {
		name    string
		tracker func(*forge.Fake) forge.IssueTracker
	}{
		{"github", func(fc *forge.Fake) forge.IssueTracker { return fc }},
		{"forgejo", func(fc *forge.Fake) forge.IssueTracker { return fc.AsForgejoShaped() }},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			x.tracker = shape.tracker(x.fc)
			x.addFailed(t, "42", "ready")

			code, _ := x.run(t)

			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if x.fc.Merged != testReconcilePR {
				t.Errorf("Merged = %q, want %q", x.fc.Merged, testReconcilePR)
			}
			got := x.labels(t, "42")
			if !slices.Contains(got, x.c.completeLabel) || slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) {
				t.Errorf("labels = %v, want %q only of the lifecycle labels", got, x.c.completeLabel)
			}
			if len(x.fc.TransitionStateCalls) == 0 {
				t.Fatalf("no tracker transitions recorded, want a 42 Failed->InProgress claim first")
			}
			first := x.fc.TransitionStateCalls[0]
			if first.Num != "42" || first.From != forge.Failed || first.To != forge.InProgress {
				t.Errorf("first transition = %+v, want 42 Failed->InProgress", first)
			}
		})
	}
}

func TestRecoverQueueOne_IneligibleIssuesAreLeftUntouched(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, x *queueRecoverFixture)
	}{
		{"blocked self-report", func(t *testing.T, x *queueRecoverFixture) { x.addFailed(t, "42", "blocked") }},
		{"no self-report", func(t *testing.T, x *queueRecoverFixture) { x.addFailed(t, "42", "") }},
		{"open PR", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			x.fc.SetPR(x.fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})
		}},
		{"no bundle", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			if err := os.Remove(filepath.Join(dispatch.OutboxDirFor(x.dir, "42"), seambundle.FileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"not labelled agent-failed", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			x.fc.SetIssue(forge.Issue{Number: "42", Labels: []string{x.c.completeLabel}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			tc.setup(t, x)

			code, _ := x.run(t)

			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			x.assertUntouched(t)
			if x.fc.Merged != "" {
				t.Errorf("Merged = %q, want none", x.fc.Merged)
			}
		})
	}
}

func TestRecoverQueueOne_HeldHostClaimIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	release, err := dispatch.ClaimIssue(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "status=skipped") {
		t.Errorf("output = %q, want a skipped line", out)
	}
	x.assertUntouched(t)
}

func TestRecoverQueueOne_RelayFailureEndsAgentFailed(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0 (an issue was attempted)", code)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) || slices.Contains(got, x.c.completeLabel) {
		t.Errorf("labels = %v, want %q only of the lifecycle labels", got, x.c.failedLabel)
	}
}

func TestRecoverQueueOne_StopSignalExitsSevenWithoutParking(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	withClosedStopSignal(t)

	code, _ := x.run(t)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	x.assertUntouched(t)
	if got := x.labels(t, "42"); !slices.Contains(got, x.c.failedLabel) {
		t.Errorf("labels = %v, want agent-failed kept", got)
	}
}

func TestRecoverQueueOne_AttemptsOnlyOneEligibleIssue(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "41", "ready")
	x.addFailed(t, "42", "ready")

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	var started []string
	for _, call := range x.fc.TransitionStateCalls {
		if call.From == forge.Failed && call.To == forge.InProgress {
			started = append(started, call.Num)
		}
	}
	if len(started) != 1 {
		t.Errorf("issues claimed = %v, want exactly one", started)
	}
}

func TestRecoverQueueOne_PRLessForgeRefuses(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	local := x.fc.AsLocal()

	code, _ := x.runWith(t, capsFor(x.fc, local))

	if code != 1 {
		t.Errorf("exit = %d, want 1 (a refusal, not an empty queue)", code)
	}
	x.assertUntouched(t)
}

// withControlledStop stubs the signal install with a stop channel the test
// closes mid-run, so a stop can land at an exact seam rather than before the
// first checkpoint. The returned func is safe to call more than once.
func withControlledStop(t *testing.T) (stop func()) {
	t.Helper()
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	orig := installStopSignal
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })
	var once sync.Once
	return func() { once.Do(func() { close(stopCh) }) }
}

// stopOnPRLookup stops the run from inside the PR lookup. It embeds PRForge as
// well because forge.ResolveOpenPR finds it by type assertion on the CodeForge,
// which a bare embedded CodeForge would not satisfy.
type stopOnPRLookup struct {
	forge.CodeForge
	forge.PRForge
	stop func()
}

func (w stopOnPRLookup) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	w.stop()
	return w.PRForge.OpenPRForBranch(branch)
}

// stopAfterClaim stops the run right after a Failed->InProgress transition.
type stopAfterClaim struct {
	forge.IssueTracker
	stop func()
}

func (w stopAfterClaim) TransitionState(num string, from, to forge.DispatchState) error {
	err := w.IssueTracker.TransitionState(num, from, to)
	if from == forge.Failed && to == forge.InProgress {
		w.stop()
	}
	return err
}

func (x *queueRecoverFixture) runOn(t *testing.T, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities) int {
	t.Helper()
	err := recoverQueueOne(x.c, it, cf, caps, x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), io.Discard, io.Discard)
	return exitCodeFor(err)
}

// A stop landing during the eligibility reads is only seen by the one handler
// installed ahead of them, so the run must still end in a stop, not a landing.
func TestRecoverQueueOne_StopDuringEligibilityReadsExitsSeven(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)
	caps := capsFor(x.tracker, x.cf)

	code := x.runOn(t, x.fc, stopOnPRLookup{CodeForge: x.cf, PRForge: x.cf.(forge.PRForge), stop: stop}, caps)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	x.assertUntouched(t)
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
	if got := x.labels(t, "42"); !slices.Contains(got, x.c.failedLabel) {
		t.Errorf("labels = %v, want agent-failed kept", got)
	}
}

func TestRecoverQueueOne_StopAfterClaimRestoresAgentFailed(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)

	code := x.runOn(t, stopAfterClaim{IssueTracker: x.fc, stop: stop}, x.cf, capsFor(x.tracker, x.cf))

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
	if len(x.fc.CommentCalls) != 0 {
		t.Errorf("comments = %v, want none", x.fc.CommentCalls)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) {
		t.Errorf("labels = %v, want agent-failed and not agent-in-progress", got)
	}
}

func TestRecoverQueueOne_LostClaimRaceIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.TransitionStateErr = forge.ErrAlreadyClaimed

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "status=skipped") {
		t.Errorf("output = %q, want a skipped line", out)
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
}

// A lookup outage must surface, not read as an empty queue. Queue mode has
// claimed nothing before these reads, so a stale prior Complete state must not
// let the failure path write the tracker or mark the issue complete.
func TestRecoverQueueOne_PreClaimErrorFailsWithoutTransition(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*testing.T, *queueRecoverFixture)
	}{
		{"issue lookup", func(_ *testing.T, x *queueRecoverFixture) { x.fc.IssueErr = errors.New("forge: boom") }},
		{"PR resolve", func(_ *testing.T, x *queueRecoverFixture) { x.fc.OpenPRForBranchErr = errors.New("forge: boom") }},
		{"host claim", func(t *testing.T, x *queueRecoverFixture) {
			// A directory at the lock path makes ClaimIssue's OpenFile fail with
			// EISDIR, an error other than ErrIssueClaimed.
			if err := os.MkdirAll(filepath.Join(dispatch.HostLogDirFor(x.dir), "issue-42.lock"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			x.addFailed(t, "42", "ready")
			x.fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Complete}
			tc.inject(t, x)

			code, _ := x.run(t)

			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			x.assertUntouched(t)
			x.fc.IssueErr = nil // labels() Fatalf's on a lookup error; clear the issue-lookup case's injected error
			got := x.labels(t, "42")
			if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.completeLabel) {
				t.Errorf("labels = %v, want agent-failed and not agent-complete", got)
			}
		})
	}
}

func queueLaunchContext(t *testing.T, x *queueRecoverFixture, cleaned *bool) *launchContext {
	t.Helper()
	return &launchContext{
		config:       x.c,
		pwd:          x.dir,
		issueTracker: x.tracker,
		codeForge:    x.cf,
		capabilities: capsFor(x.tracker, x.cf),
		factory:      testFactory(t, x.dir, nil),
		settle:       testNewSettle(x.c, x.tracker, testWired(x.tracker), x.cf),
		cleanup:      func() { *cleaned = true },
	}
}

// cmdRecoverQueue maps recoverQueueOne's verdicts onto exit codes (0
// attempted, 2 none eligible, 1 refusal) and runs cleanup on each; an empty
// queue is a verdict, so it prints nothing to stderr.
func TestCmdRecoverQueue_ExitCodesAndCleanup(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*testing.T, *queueRecoverFixture) *launchContext
		want       int
		wantStderr string
	}{
		{"attempted", func(t *testing.T, x *queueRecoverFixture) *launchContext {
			x.addFailed(t, "42", "ready")
			return nil
		}, 0, ""},
		{"nothing eligible", func(t *testing.T, x *queueRecoverFixture) *launchContext { return nil }, 2, ""},
		{"no PR forge", func(t *testing.T, x *queueRecoverFixture) *launchContext {
			x.addFailed(t, "42", "ready")
			lc := queueLaunchContext(t, x, new(bool))
			lc.capabilities = forge.Capabilities{}
			return lc
		}, 1, "PR-shaped Code Forge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			var cleaned bool
			lc := tc.setup(t, x)
			if lc == nil {
				lc = queueLaunchContext(t, x, &cleaned)
			} else {
				lc.cleanup = func() { cleaned = true }
			}
			var stdout, stderr strings.Builder
			if got := cmdRecoverQueue(lc, &stdout, &stderr); got != tc.want {
				t.Errorf("cmdRecoverQueue = %d, want %d (stderr %q)", got, tc.want, stderr.String())
			}
			if tc.wantStderr == "" {
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
			if !cleaned {
				t.Error("cmdRecoverQueue did not run lc.cleanup()")
			}
		})
	}
}
