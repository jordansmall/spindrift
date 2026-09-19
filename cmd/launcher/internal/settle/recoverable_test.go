package settle

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

// writeBundle writes a seam.bundle file into dir. The content does not matter
// because settle only stats it; the fixed name is what relayBundle and
// bundlePresent key off (internal/seambundle.FileName).
func writeBundle(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, seambundle.FileName), []byte("bundle"), 0o644); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
}

// Slice B's positive case for ADR 0039's CODE_FORGE=local push-only
// counterpart to tryAdoptRelayedBranchNoOutcome: a local run with no parseable
// outcome line, a genuine success self-report, and a relayable bundle in the
// outbox must not be parked agent-failed. Local has no PR-shaped adopt path
// (s.pr is always nil), so it promotes the issue to Recoverable instead.
func TestSettle_LocalPushOnly_NoOutcomeBundlePresentMarksRecoverable(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			found = true
		}
		if call.Num == issNum && call.To == forge.Failed {
			t.Errorf("issue must not transition to Failed; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
	if !found {
		t.Errorf("expected a TransitionState(..., Recoverable) call; TransitionStateCalls=%+v", fc.TransitionStateCalls)
	}
}

// The same promotion from gate.go's "blocked" arm, the local counterpart to
// tryAdoptRelayedBranch's synthetic-blocked override: when the authoritative
// outcome degraded to the ADR 0036 synthetic status=blocked backstop but the
// driver self-reported success and the bundle is in the outbox, settle
// promotes the issue to Recoverable rather than parking it agent-failed.
func TestSettle_LocalPushOnly_SyntheticBlockedBundlePresentMarksRecoverable(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:      true,
			Provenance: outcome.ProvenanceSynthetic,
			Outcome: outcome.Outcome{
				Issue:     issNum,
				Landing:   branch,
				Status:    "blocked",
				Synthetic: true,
				Note:      "driver exited without emitting an outcome",
			},
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			found = true
		}
		if call.Num == issNum && call.To == forge.Failed {
			t.Errorf("issue must not transition to Failed; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
	if !found {
		t.Errorf("expected a TransitionState(..., Recoverable) call; TransitionStateCalls=%+v", fc.TransitionStateCalls)
	}
}

// Proves the Synthetic guard matters: a genuine (non-synthetic) status=blocked
// is the driver's own authoritative outcome line, not the ADR 0036 backstop
// this override exists to second-guess. Even with a success self-report and a
// bundle present, the issue must still be parked agent-failed.
func TestSettle_LocalPushOnly_GenuineBlockedDoesNotMarkRecoverable(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:      true,
			Provenance: outcome.ProvenanceGenuine,
			Outcome: outcome.Outcome{
				Issue:     issNum,
				Landing:   branch,
				Status:    "blocked",
				Synthetic: false,
				Note:      "driver reported blocked",
			},
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a genuine blocked outcome; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable on a genuine blocked outcome; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}

// A genuine success self-report alone is not enough. No bundle in the outbox
// means there is nothing for `spindrift recover` to land, so the issue falls
// through to the normal no-outcome handling (settleUnresolved), which parks it
// agent-failed with no open PR to find.
func TestSettle_LocalPushOnly_NoOutcomeBundleMissingFallsBackToFailed(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir() // no bundle written

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when no bundle is present in the outbox; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable when no bundle is present; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}

// The fallback for a Box that crashed and never self-reported: with no
// evidence that the run succeeded, a bundle in the outbox is not enough on its
// own, so the issue still falls through to settleUnresolved's agent-failed
// park.
func TestSettle_LocalPushOnly_NoSelfReportFallsBackToFailed(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: false,
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the driver never self-reported; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable when the driver never self-reported; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}

// Slice 3's positive case for the signal-kill evidence leg (issue #2378): a
// local run killed by an external signal before it printed an outcome or
// self-report line has no self-report evidence, but a bundle in the outbox is
// still real work worth recovering, so the issue must be promoted to
// Recoverable rather than parked agent-failed.
func TestSettle_LocalPushOnly_KilledBySignalBundlePresentMarksRecoverable(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:        false,
		KilledBySignal: true,
		Resolved: outcome.Resolved{
			Found: false,
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			found = true
		}
		if call.Num == issNum && call.To == forge.Failed {
			t.Errorf("issue must not transition to Failed; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
	if !found {
		t.Errorf("expected a TransitionState(..., Recoverable) call; TransitionStateCalls=%+v", fc.TransitionStateCalls)
	}
}

// A signal-killed run alone is not enough. No bundle in the outbox means there
// is nothing for `spindrift recover` to land, so the issue falls through to
// the normal no-outcome handling (settleUnresolved), which parks it
// agent-failed.
func TestSettle_LocalPushOnly_KilledBySignalBundleMissingFallsBackToFailed(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir() // no bundle written

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:        false,
		KilledBySignal: true,
		Resolved: outcome.Resolved{
			Found: false,
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when no bundle is present in the outbox; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable when no bundle is present; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}

// Pins AC3 (issue #2378): a clean, non-signal exit with a bundle present but
// no self-report is unchanged by the signal-kill evidence leg added alongside
// it. KilledBySignal false and no self-report together still park the issue
// agent-failed, the same as before this leg existed.
func TestSettle_LocalPushOnly_CleanFailureBundlePresentFallsBackToFailed(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:        false,
		KilledBySignal: false,
		Resolved: outcome.Resolved{
			Found: false,
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed on a clean non-signal exit with no self-report; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable on a clean non-signal exit with no self-report; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}

// Slice C's positive case for ADR 0039's `spindrift recover` local push-only
// landing arm: a Recoverable issue's relayed branch (bundle in the outbox,
// genuine success self-report) must land via RelayBundle plus merge in
// landRelayedBranchPushOnly/landPushOnly, rather than fall through to
// adoptAndGate's PR-shaped path, which always fails for local.
func TestSettle_SettleRelayedBranch_LocalPushOnlyLandsRelayedBranch(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-recoverable"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())

	sit := s.situationFor(issNum, false, result)
	got := s.SettleRelayedBranch(d, issNum, 0, sit, result)
	if !got {
		t.Fatalf("SettleRelayedBranch = false, want true")
	}
	if fc.Merged != branch {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", branch, fc.Merged)
	}

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Complete {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a TransitionState(..., Complete) call; TransitionStateCalls=%+v", fc.TransitionStateCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a landed relayed branch; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must not carry agent-failed after a landed relayed branch; labels=%v", iss.Labels)
	}
}

// Slice C's regression check that a plain git push-only forge (s.pr == nil,
// but not a forge.BundleRelay) still falls through to adoptAndGate unchanged:
// adoptRelayedBranch's DraftPRCreator assertion still fails it, so
// SettleRelayedBranch must return false rather than route it into the
// local-shaped landing arm.
func TestSettle_SettleRelayedBranch_GitPushOnlyStillReturnsFalse(t *testing.T) {
	const issNum = "1"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return t.TempDir() }
	s := newTestSettle(c, fc, fc.AsPushOnly())

	sit := s.situationFor(issNum, false, result)
	got := s.SettleRelayedBranch(d, issNum, 0, sit, result)
	if got {
		t.Fatalf("SettleRelayedBranch = true, want false for a git-shaped push-only forge")
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
}

// Slice 4's positive case for the recover-time bundle-alone leniency (issue
// #2378): recover is a separate, later process with no access to the original
// run's in-memory KilledBySignal bit, so a signal-killed Box leaves it no
// self-report evidence on disk. A bundle in the outbox is the precondition
// tryMarkRecoverable already required, so it alone must land the relayed branch.
func TestSettle_SettleRelayedBranch_LocalPushOnlyBundleAloneLandsRelayedBranch(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-recoverable"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: false,
		Resolved: outcome.Resolved{
			SelfReportFound: false,
		},
	}

	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())

	sit := s.situationFor(issNum, false, result)
	got := s.SettleRelayedBranch(d, issNum, 0, sit, result)
	if !got {
		t.Fatalf("SettleRelayedBranch = false, want true")
	}
	if fc.Merged != branch {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", branch, fc.Merged)
	}

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Complete {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a TransitionState(..., Complete) call; TransitionStateCalls=%+v", fc.TransitionStateCalls)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a landed relayed branch; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must not carry agent-failed after a landed relayed branch; labels=%v", iss.Labels)
	}
}

// Slice 4's true-negative regression: local push-only with neither a bundle in
// the outbox nor a self-report has no evidence to recover from, so
// SettleRelayedBranch must return false and no merge may run.
func TestSettle_SettleRelayedBranch_LocalPushOnlyNoBundleNoSelfReportReturnsFalse(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir() // no bundle written

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: false,
		Resolved: outcome.Resolved{
			SelfReportFound: false,
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())

	sit := s.situationFor(issNum, false, result)
	got := s.SettleRelayedBranch(d, issNum, 0, sit, result)
	if got {
		t.Fatalf("SettleRelayedBranch = true, want false with neither a bundle nor a self-report")
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
}

// The fallback for a Box that self-reported blocked rather than success:
// isSuccessSelfReport must reject it the same way it does for
// tryAdoptRelayedBranchNoOutcome, so the bundle's presence alone is not enough
// and the issue still falls through to settleUnresolved's agent-failed park.
func TestSettle_LocalPushOnly_SelfReportBlockedFallsBackToFailed(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: "blocked"},
		},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the self-report itself says blocked; labels=%v", iss.Labels)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == issNum && call.To == forge.Recoverable {
			t.Errorf("issue must not transition to Recoverable when the self-report itself says blocked; TransitionStateCalls=%+v", fc.TransitionStateCalls)
		}
	}
}
