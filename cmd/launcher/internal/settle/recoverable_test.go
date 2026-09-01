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

// writeBundle writes an (empty, content doesn't matter — settle only stats
// it) seam.bundle file into dir, the fixed name relayBundle/bundlePresent
// both key off (internal/seambundle.FileName).
func writeBundle(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, seambundle.FileName), []byte("bundle"), 0o644); err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
}

// TestSettle_LocalPushOnly_NoOutcomeBundlePresentMarksRecoverable covers ADR
// 0039's CODE_FORGE=local push-only counterpart to
// tryAdoptRelayedBranchNoOutcome: local has no PR-shaped adopt path (s.pr is
// always nil for it), so given a genuine success self-report and a bundle
// actually relayable in the outbox it promotes the issue to Recoverable,
// leaving `spindrift recover` to land it.
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

// TestSettle_LocalPushOnly_SyntheticBlockedBundlePresentMarksRecoverable
// covers the same promotion from gate.go's "blocked" arm: an authoritative
// outcome degraded to the ADR 0036 synthetic status=blocked backstop, with a
// success self-report and a bundle in the outbox — the local counterpart to
// tryAdoptRelayedBranch's own synthetic-blocked override.
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

// TestSettle_LocalPushOnly_GenuineBlockedDoesNotMarkRecoverable proves the
// Synthetic guard actually matters: a genuine (non-synthetic) status=blocked
// is the driver's own authoritative outcome line, not the ADR 0036 backstop
// this override exists to second-guess — even with a success self-report and
// a bundle present, the issue must still be parked agent-failed.
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

// TestSettle_LocalPushOnly_NoOutcomeBundleMissingFallsBackToFailed pins that a
// genuine success self-report alone is not enough — no bundle actually sitting
// in the outbox means there is nothing for `spindrift recover` to land, so the
// issue falls through to settleUnresolved's agent-failed park.
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

// TestSettle_LocalPushOnly_NoSelfReportFallsBackToFailed covers a Box that
// crashed and never self-reported: with no evidence at all that the run
// succeeded, a bundle in the outbox is not enough on its own.
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

// TestSettle_LocalPushOnly_KilledBySignalBundlePresentMarksRecoverable covers
// the signal-kill evidence leg (issue #2378): a run killed before it ever
// printed an outcome or self-report line has no self-report evidence at all,
// but a bundle actually sitting in the outbox is still real work worth
// recovering.
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

// TestSettle_LocalPushOnly_KilledBySignalBundleMissingFallsBackToFailed
// pins that a signal-killed run alone is not enough — no bundle actually
// sitting in the outbox means there is nothing for `spindrift recover` to
// land.
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

// TestSettle_LocalPushOnly_CleanFailureBundlePresentFallsBackToFailed pins
// AC3 (issue #2378): KilledBySignal false plus no self-report still parks the
// issue agent-failed, unchanged by the signal-kill evidence leg added
// alongside it.
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

// TestSettle_SettleRelayedBranch_LocalPushOnlyLandsRelayedBranch covers ADR
// 0039's `spindrift recover` local push-only landing arm: a Recoverable
// issue's relayed branch must actually land — RelayBundle+merge via
// landRelayedBranchPushOnly/landPushOnly — rather than fall through to
// adoptAndGate's PR-shaped path, which always fails for local (no
// DraftPRCreator).
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

// TestSettle_SettleRelayedBranch_GitPushOnlyStillReturnsFalse pins that a
// plain git push-only forge (s.pr == nil, but does NOT implement
// forge.BundleRelay) still falls through to adoptAndGate unchanged — where
// adoptRelayedBranch's own DraftPRCreator assertion fails it — rather than
// being routed into the local-shaped landing arm.
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

// TestSettle_SettleRelayedBranch_LocalPushOnlyBundleAloneLandsRelayedBranch
// covers recover-time bundle-alone leniency (issue #2378): a signal-killed Box
// never gets the chance to print an outcome or self-report line at all, so
// recover — a separate, later process with no access to the original run's
// in-memory KilledBySignal bit — has no self-report evidence to consult from
// disk. A bundle actually sitting in the outbox is the same hard physical
// precondition tryMarkRecoverable already required before promoting the issue
// to Recoverable, so for local push-only it must suffice on its own here too.
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

// TestSettle_SettleRelayedBranch_LocalPushOnlyNoBundleNoSelfReportReturnsFalse
// is the true-negative: local push-only with neither a bundle in the outbox
// nor a self-report has no evidence at all to recover from.
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

// TestSettle_LocalPushOnly_SelfReportBlockedFallsBackToFailed pins that
// isSuccessSelfReport rejects a blocked self-report the same way it does for
// tryAdoptRelayedBranchNoOutcome, so the bundle's presence alone is not
// enough.
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
