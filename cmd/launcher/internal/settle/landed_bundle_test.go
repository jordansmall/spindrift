package settle

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

func bundleExists(t *testing.T, outbox string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(outbox, seambundle.FileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat bundle: %v", err)
	}
	return err == nil
}

// bundleFixture is a green PR on an in-progress issue "1" whose outbox is
// outbox; the leading Pending proves the checks registered. Callers tweak the returned config or fake before newTestSettle.
func bundleFixture(outbox string) (Config, *forge.Fake) {
	c := baseConfig()
	c.OutboxDir = func(string) string { return outbox }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	return c, fc
}

// openAfterMerge reports the PR as still OPEN after a nil Merge, as a forge
// whose merge is queued rather than applied would.
type openAfterMerge struct{ *forge.Fake }

func (o openAfterMerge) Merge(url string) error {
	if err := o.Fake.Merge(url); err != nil {
		return err
	}
	o.SetPRState(url, forge.PROpen)
	return nil
}

// Issue #4653: a finished issue must not leave a stale seam.bundle behind, but
// only a settle that verified the merge may drop it; every other outcome keeps
// the bundle as the evidence a human or `spindrift recover` works from.
func TestSettleAdopted_BundleRemovedOnlyAfterVerifiedMerge(t *testing.T) {
	green := []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess}
	cases := []struct {
		name        string
		mergeMode   string
		guard       string
		mergeErr    error
		checkStates []forge.RollupState
		wantBundle  bool
	}{
		{name: "merged", mergeMode: "immediate", checkStates: green},
		{name: "gate terminal", mergeMode: "immediate", checkStates: []forge.RollupState{forge.StateFailure}, wantBundle: true},
		{name: "manual", mergeMode: "manual", checkStates: green, wantBundle: true},
		{name: "auto-merge queued", mergeMode: "auto", checkStates: green, wantBundle: true},
		{name: "merge guard hit", mergeMode: "immediate", guard: ".github/**", checkStates: green, wantBundle: true},
		{name: "merge blocked", mergeMode: "immediate", mergeErr: errors.New("required review missing"), checkStates: green, wantBundle: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := t.TempDir()
			writeBundle(t, outbox)
			c, fc := bundleFixture(outbox)
			c.MergeMode = tc.mergeMode
			c.MergeGuardPaths = tc.guard
			fc.SetCheckStates(testPR, tc.checkStates)
			fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})
			fc.MergeErr = tc.mergeErr
			s := newTestSettle(c, fc, fc)

			captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })

			if got := bundleExists(t, outbox); got != tc.wantBundle {
				t.Errorf("bundle present = %v, want %v", got, tc.wantBundle)
			}
		})
	}
}

// The merge gate can go green and Merge return nil while the forge still
// reports the PR unmerged; verifyMerged then fails the issue, and a failed
// settle keeps its bundle.
func TestSettleAdopted_MergeNotVerifiedKeepsBundle(t *testing.T) {
	outbox := t.TempDir()
	writeBundle(t, outbox)
	c, fc := bundleFixture(outbox)
	s := newTestSettle(c, fc, openAfterMerge{fc})

	out := captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })

	if !strings.Contains(out, "status=failed") {
		t.Fatalf("want a failed settle, got %q", out)
	}
	if !bundleExists(t, outbox) {
		t.Errorf("a settle demoted to Failed must keep its bundle")
	}
}

// The outbox also holds the pass manifest, so only the bundle file goes.
func TestSettleAdopted_MergedLandingLeavesOtherOutboxFiles(t *testing.T) {
	outbox := t.TempDir()
	writeBundle(t, outbox)
	other := filepath.Join(outbox, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, fc := bundleFixture(outbox)
	s := newTestSettle(c, fc, fc)

	out := captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })

	if !strings.Contains(out, "status=verified-merged") {
		t.Fatalf("want a verified merge, got %q", out)
	}
	if bundleExists(t, outbox) {
		t.Errorf("bundle must be removed after a verified merge")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated outbox file must survive: %v", err)
	}
}

// A missing bundle (e.g. a Box that wrote none) is a silent no-op, and an
// unset OutboxDir never panics.
func TestSettleAdopted_MergedLandingWithoutBundleOrOutboxIsSilent(t *testing.T) {
	for name, outboxDir := range map[string]func(string) string{
		"missing bundle": func(string) string { return t.TempDir() },
		"nil OutboxDir":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			c, fc := bundleFixture("")
			c.OutboxDir = outboxDir
			s := newTestSettle(c, fc, fc)

			out := captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })

			if !strings.Contains(out, "status=verified-merged") {
				t.Errorf("want a verified merge, got %q", out)
			}
			if strings.Contains(out, "status=bundle-remove-failed") {
				t.Errorf("want no bundle-remove-failed line, got %q", out)
			}
		})
	}
}

// A failed removal is logged and must never fail the landing.
func TestSettleAdopted_BundleRemovalFailureIsLoggedAndLandingStillMerges(t *testing.T) {
	outbox := t.TempDir()
	// A non-empty directory in the bundle's place makes os.Remove fail.
	stuck := filepath.Join(outbox, seambundle.FileName)
	if err := os.MkdirAll(filepath.Join(stuck, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, fc := bundleFixture(outbox)
	s := newTestSettle(c, fc, fc)

	out := captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR) })

	if !strings.Contains(out, "status=verified-merged") {
		t.Errorf("landing must still verify as merged despite the removal failure, got %q", out)
	}
	if !strings.Contains(out, "status=bundle-remove-failed") || !strings.Contains(out, "!!") {
		t.Errorf("want a logged bundle-remove-failed line, got %q", out)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must still be agent-complete; labels=%v", iss.Labels)
	}
}

// The push-only (CODE_FORGE=local) landing relays the bundle itself, then
// drops it once the merge lands; a manual hand-off keeps it.
func TestSelfHeal_LocalPushOnly_BundleRemovedOnlyWhenMerged(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mergeMode  string
		want       landingResult
		wantBundle bool
	}{
		{"immediate", "immediate", landingMerged, false},
		{"manual", "manual", landingManual, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outbox := t.TempDir()
			writeBundle(t, outbox)
			c := baseConfig()
			c.MergeMode = tc.mergeMode
			c.OutboxDir = func(string) string { return outbox }
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			s := newTestSettle(c, fc, fc.AsLocal())

			landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, fc.AgentBranch("1"))

			if landing != tc.want {
				t.Fatalf("selfHeal = %v, want %v", landing, tc.want)
			}
			if got := bundleExists(t, outbox); got != tc.wantBundle {
				t.Errorf("bundle present = %v, want %v", got, tc.wantBundle)
			}
		})
	}
}

// A blocked hand-off never reaches the landing, so its bundle stays.
func TestSettle_BlockedHandOffKeepsBundle(t *testing.T) {
	const issNum = "1933"
	outbox := t.TempDir()
	writeBundle(t, outbox)
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = "https://github.com/owner/repo/pull/1933"
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(string) string { return outbox }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if !bundleExists(t, outbox) {
		t.Errorf("a blocked hand-off must keep its bundle")
	}
}

// Recover's adopt arm lands through the same gate and drops the bundle too.
func TestSettle_SettleRelayedBranch_RemovesBundleAfterMerge(t *testing.T) {
	const issNum = "2225"
	const prURL = "https://github.com/owner/repo/pull/2225"
	outbox := t.TempDir()
	writeBundle(t, outbox)
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	result := dispatch.Result{Resolved: outcome.Resolved{
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
	}}
	c := baseConfig()
	c.OutboxDir = func(string) string { return outbox }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	sit := s.situationFor(issNum, false, result)
	if got, err := s.SettleRelayedBranch(dispatch.NewFake(), issNum, 0, sit, result); !got || err != nil {
		t.Fatalf("SettleRelayedBranch = %v, %v; want true, nil", got, err)
	}
	if fc.Merged != prURL {
		t.Fatalf("fc.Merged = %q, want %q", fc.Merged, prURL)
	}
	if bundleExists(t, outbox) {
		t.Errorf("recover's landed issue must not keep its bundle")
	}
}

// Recover's hand-run push-only arm (CODE_FORGE=local, no PR) lands through
// selfHeal and drops the bundle on an immediate merge.
func TestSettle_SettleRelayedBranch_LocalPushOnly_RemovesBundleAfterMerge(t *testing.T) {
	const issNum = "1"
	outbox := t.TempDir()
	writeBundle(t, outbox)
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	c := baseConfig()
	c.OutboxDir = func(string) string { return outbox }
	s := newTestSettle(c, fc, fc.AsLocal())
	result := dispatch.Result{}

	sit := s.situationFor(issNum, false, result)
	if !sit.BundlePresent {
		t.Fatalf("fixture: want BundlePresent")
	}
	if got, err := s.SettleRelayedBranch(dispatch.NewFake(), issNum, 0, sit, result); !got || err != nil {
		t.Fatalf("SettleRelayedBranch = %v, %v; want true, nil", got, err)
	}
	if bundleExists(t, outbox) {
		t.Errorf("recover's landed push-only issue must not keep its bundle")
	}
}
