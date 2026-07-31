package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSettle_GithubReadOnly_AdoptsBackstopSyntheticSuccess is the positive
// case for issue #2224's auto-adoption: a read-only github run whose
// authoritative outcome degraded to the synthetic status=blocked backstop
// (ADR 0036) but whose driver self-report says the work actually succeeded
// (issue #2223) must not be parked agent-failed. Instead settle relays the
// finished branch, opens a PR from the box's own PR-intent line (mirroring
// hostMediateDraftPR's "ready" hand-off), and drives the normal merge
// lifecycle through to agent-complete on green.
func TestSettle_GithubReadOnly_AdoptsBackstopSyntheticSuccess(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "success"},
		PRIntent:        "feat: add widget\n\nAdds a widget.",
		PRIntentFound:   true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/2224", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/2224 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.", Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", prURL, fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after an adopted-and-merged landing; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must not carry agent-failed after an adopted-and-merged landing; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_AdoptsWithDefaultPRBodyWhenNoIntent covers
// acceptance criterion 2: when the box's log carried no usable PR-intent
// line, adoptRelayedBranch falls back to defaultAdoptPRText — an
// issue-title-derived Title and a body that explains the adoption's
// provenance and appends "Closes #<num>" — rather than blocking the
// adoption entirely.
func TestSettle_GithubReadOnly_AdoptsWithDefaultPRBodyWhenNoIntent(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Title: "Fix the widget", Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "success"},
		PRIntentFound:   false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if call.Title != "Fix the widget" {
		t.Errorf("CreateDraftPRCalls[0].Title = %q, want issue-derived default %q", call.Title, "Fix the widget")
	}
	if !strings.Contains(call.Body, "Closes #"+issNum) {
		t.Errorf("CreateDraftPRCalls[0].Body = %q, want it to contain %q", call.Body, "Closes #"+issNum)
	}
	if call.Base != "main" {
		t.Errorf("CreateDraftPRCalls[0].Base = %q, want %q", call.Base, "main")
	}
	if call.Head != branch {
		t.Errorf("CreateDraftPRCalls[0].Head = %q, want %q", call.Head, branch)
	}
	if fc.Merged != prURL {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", prURL, fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after an adopted-and-merged landing; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must not carry agent-failed after an adopted-and-merged landing; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_NonSyntheticBlockedDoesNotAdopt covers a driver
// that genuinely blocked (Outcome.Synthetic=false) — even with a self-report
// that says success, adoption must not fire: a non-synthetic status=blocked
// is the driver's own authoritative outcome line, not the ADR 0036 backstop
// this override exists to second-guess.
func TestSettle_GithubReadOnly_NonSyntheticBlockedDoesNotAdopt(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: false,
			Note:      "driver reported blocked",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "success"},
		PRIntentFound:   false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a genuine blocked outcome; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete after a genuine blocked outcome; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadOnly_SyntheticBlockedNoSelfReportDoesNotAdopt covers
// a Box that crashed and never self-reported at all — the synthetic backstop
// fires (issue #2224's fingerprint's first condition) but there is no
// self-report evidence at all that the run succeeded, so adoption must not
// fire.
func TestSettle_GithubReadOnly_SyntheticBlockedNoSelfReportDoesNotAdopt(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: false,
		PRIntentFound:   false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the driver never self-reported; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the driver never self-reported; labels=%v", iss.Labels)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
}

// TestSettle_GithubReadOnly_SyntheticBlockedSelfReportBlockedDoesNotAdopt
// covers a Box that did self-report, but self-reported blocked rather than
// success — isSuccessSelfReport must reject it, so no PR is opened at all.
func TestSettle_GithubReadOnly_SyntheticBlockedSelfReportBlockedDoesNotAdopt(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "blocked"},
		PRIntentFound:   false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the self-report itself says blocked; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the self-report itself says blocked; labels=%v", iss.Labels)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls; got %+v", fc.CreateDraftPRCalls)
	}
}

// TestSettle_GithubReadOnly_AdoptionFingerprintButBundleMissingFallsBackToBlocked
// covers the fingerprint condition (c): a full success fingerprint (synthetic
// blocked + success self-report) can still fail to actually adopt if
// RelayBundle itself errors — no bundle means no finished branch to open a PR
// on, so tryAdoptRelayedBranch must bail there and let the normal blocked
// handling run.
func TestSettle_GithubReadOnly_AdoptionFingerprintButBundleMissingFallsBackToBlocked(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "success"},
		PRIntentFound:   false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls when the bundle relay fails; got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the relay bundle is missing; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the relay bundle is missing; labels=%v", iss.Labels)
	}
}

// TestSettle_GithubReadWrite_SyntheticSuccessDoesNotAdopt covers the
// fingerprint's s.readOnly condition: under a read-write Code Forge, even a
// full success fingerprint (synthetic blocked + success self-report) must
// not adopt — the override exists for a read-only Box that cannot push or
// open a PR itself (issue #1933's reasoning), which does not apply here.
func TestSettle_GithubReadWrite_SyntheticSuccessDoesNotAdopt(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome: outcome.Outcome{
			Issue:     issNum,
			Landing:   branch,
			Status:    "blocked",
			Synthetic: true,
			Note:      "driver exited without emitting an outcome",
		},
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: "success"},
	}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := New(c, fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls under read-write; got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed under read-write; labels=%v", iss.Labels)
	}
}
