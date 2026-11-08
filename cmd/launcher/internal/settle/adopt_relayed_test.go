package settle

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// Issue #2224: a read-only github run whose authoritative outcome degraded to
// the synthetic status=blocked backstop (ADR 0036), but whose driver
// self-report says the work actually succeeded (issue #2223), must not be
// parked agent-failed. Settle relays the finished branch, opens a PR from the
// box's own PR-intent line, and merges to agent-complete on green.
func TestSettle_GithubReadOnly_AdoptsBackstopSyntheticSuccess(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc, branch, rec := newAdoptBackstopFixture(t, issNum, prURL, "feat: add widget\n\nAdds a widget.")

	const wantNote = "note=backstop-synthetic blocked overridden by genuine success self-report; PR opened on relayed branch"
	if got := adoptedDetail(t, rec); got != wantNote {
		t.Errorf("adopted Detail = %q, want %q", got, wantNote)
	}

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/2224", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/2224 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.\n\nCloses #" + issNum, Base: "main", Head: branch}
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

// newAdoptBackstopFixture runs Settle over a synthetic-backstop result with a
// self-reported success, using prIntent as the box's own PR-intent body. It
// returns the fake forge, the relayed branch, and the recorded progress.
func newAdoptBackstopFixture(t *testing.T, issNum, prURL, prIntent string) (*forge.Fake, string, *ProgressRecorder) {
	t.Helper()
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
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
		PRIntent:      prIntent,
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	rec := &ProgressRecorder{}
	c.Progress = rec
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)
	return fc, branch, rec
}

// adoptedDetail returns the Detail of the single status=adopted step in rec.
func adoptedDetail(t *testing.T, rec *ProgressRecorder) string {
	t.Helper()
	var details []string
	for _, st := range rec.Steps() {
		if st.Status == StatusAdopted {
			details = append(details, st.Detail)
		}
	}
	if len(details) != 1 {
		t.Fatalf("adopted steps = %q, want exactly 1", details)
	}
	return details[0]
}

// Issue #2345: adoptRelayedBranch's ensureClosesReference call must not
// duplicate a closing reference the box's own PR-intent body already carries.
// This exercises it through the full adopt-relayed flow rather than calling
// ensureClosesReference directly.
func TestSettle_GithubReadOnly_AdoptsBackstopSyntheticSuccess_DedupsExistingCloses(t *testing.T) {
	const issNum = "2345"
	const prURL = "https://github.com/owner/repo/pull/2345"

	fc, branch, _ := newAdoptBackstopFixture(t, issNum, prURL, "feat: add widget\n\nCloses #"+issNum)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Closes #" + issNum, Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
}

// Issue #2253: a read-only github run that exited with no parseable outcome
// line at all (!result.Resolved.Found), distinct from #2224's synthetic
// status=blocked backstop, but whose driver self-report says the work
// succeeded (issue #2223) must not be parked agent-failed either. Same flow as
// the backstop case above, except for the missing outcome line.
func TestSettle_GithubReadOnly_AdoptsNoOutcomeSuccess(t *testing.T) {
	const issNum = "2253"
	const prURL = "https://github.com/owner/repo/pull/2253"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	rec := &ProgressRecorder{}
	c.Progress = rec
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	const wantNote = "note=no outcome line; genuine success self-report and relayed bundle; PR opened on relayed branch"
	if got := adoptedDetail(t, rec); got != wantNote {
		t.Errorf("adopted Detail = %q, want %q", got, wantNote)
	}

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/2253", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/2253 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.\n\nCloses #" + issNum, Base: "main", Head: branch}
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

// Acceptance criterion 2: when the box's log carried no usable PR-intent line,
// adoptRelayedBranch falls back to defaultAdoptPRText (an issue-title-derived
// Title, and a body that explains the adoption's provenance and appends
// "Closes #<num>") rather than blocking the adoption entirely.
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
		PRIntentFound: false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// The landingFailed arm of the adoption's selfHeal switch: the fingerprint
// holds and the PR is opened on the relayed branch, but CI comes back red.
// Adoption must not merge on red and must leave the issue short of
// agent-complete, because the CI gate is the authoritative judge, not the
// driver's own success self-report.
func TestSettle_GithubReadOnly_AdoptedPRWithRedCIDoesNotMerge(t *testing.T) {
	const issNum = "2224"
	const prURL = "https://github.com/owner/repo/pull/2224"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateFailure})

	d := dispatch.NewFake()
	result := dispatch.Result{
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
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	s.Settle(d, issNum, 0, result)
	w.Close()
	os.Stdout = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	stdout := string(captured)

	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("adoption must still open the PR before CI judges it; CreateDraftPRCalls=%+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("an adopted PR must not merge on red CI; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the adopted PR's CI is red; labels=%v", iss.Labels)
	}
	// issue #2328: adoptAndGate's landingFailed print must carry selfHeal's
	// own classified reason, not the old hardcoded "CI or merge failed".
	if strings.Contains(stdout, "CI or merge failed") {
		t.Errorf("stdout must not contain the old hardcoded literal, got: %s", stdout)
	}
	if !strings.Contains(stdout, "ci-red: still red after exhausting 0 fix pass(es)") {
		t.Errorf("stdout must contain selfHeal's classified reason, got: %s", stdout)
	}
}

// Settle must not adopt a driver that genuinely blocked (Resolved.Provenance
// == outcome.ProvenanceGenuine), even when the self-report says success: a
// non-synthetic status=blocked is the driver's own authoritative outcome line,
// not the ADR 0036 backstop this override exists to second-guess.
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
		PRIntentFound: false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// A Box that crashed and never self-reported: the synthetic backstop fires
// (issue #2224's fingerprint's first condition) but no self-report evidence
// says the run succeeded, so adoption must not fire.
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
			SelfReportFound: false,
		},
		PRIntentFound: false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// A Box that did self-report, but self-reported blocked rather than success:
// isSuccessSelfReport must reject it, so no PR is opened at all.
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
			SelfReport:      outcome.SelfReport{Status: "blocked"},
		},
		PRIntentFound: false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// Fingerprint condition (c): a full success fingerprint (synthetic blocked
// plus success self-report) can still fail to adopt if RelayBundle errors. No
// bundle means no finished branch to open a PR on, so the PR Lander's salvage
// must bail there and let the normal blocked handling run.
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
		PRIntentFound: false,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// The fingerprint's s.readOnly condition: under a read-write Code Forge, even
// a full success fingerprint must not adopt. The override exists for a
// read-only Box that cannot push or open a PR itself (issue #1933's
// reasoning), which does not apply here.
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
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc)
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

// the PR Lander's salvage (no outcome line)'s own fingerprint (issue #2253) in
// the !result.Resolved.Found arm: a Box that crashed and never self-reported
// has no evidence the run succeeded, so adoption must not fire and settle falls
// back to settleUnresolved, which parks the issue agent-failed because no PR
// was ever opened for ResolveOpenPR to find.
func TestSettle_GithubReadOnly_NoOutcomeNoSelfReportDoesNotAdopt(t *testing.T) {
	const issNum = "2253"
	const prURL = "https://github.com/owner/repo/pull/2253"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: false,
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls; got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the driver never self-reported; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the driver never self-reported; labels=%v", iss.Labels)
	}
}

// A Box that did self-report in the !result.Resolved.Found arm (issue #2253),
// but self-reported blocked rather than success: isSuccessSelfReport must
// reject it, so no PR is opened at all and settle falls back to
// settleUnresolved's agent-failed park.
func TestSettle_GithubReadOnly_NoOutcomeSelfReportBlockedDoesNotAdopt(t *testing.T) {
	const issNum = "2253"
	const prURL = "https://github.com/owner/repo/pull/2253"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: "blocked"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(d, issNum, 0, result)

	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls; got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed when the self-report itself says blocked; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must not carry agent-complete when the self-report itself says blocked; labels=%v", iss.Labels)
	}
}

// Fingerprint condition (c) in the !result.Resolved.Found arm (issue #2253): a
// full success fingerprint (no outcome line plus success self-report) can
// still fail to adopt if RelayBundle errors, so the PR Lander's salvage (no
// outcome line) must bail there and let settleUnresolved's normal no-outcome
// handling run.
func TestSettle_GithubReadOnly_NoOutcomeBundleMissingFallsBackToBlocked(t *testing.T) {
	const issNum = "2253"
	const prURL = "https://github.com/owner/repo/pull/2253"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:           false,
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
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

// Recover's relay arm (issue #2225): with no open PR and a genuine success
// self-report on record, it relays the branch into a real PR and drives it
// through the normal merge gate. It needs neither Resolved.Provenance ==
// outcome.ProvenanceSynthetic nor a read-only Code Forge, since recover is
// operator-driven and runs read-write.
func TestRecover_RelaysSuccessSelfReportIntoMergedPR(t *testing.T) {
	e := newRecoverEnv(t, false)
	st, err := e.s.Recover(e.in("", readyReport(), nil, false))
	if err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}
	if st.State() != forge.Complete || st.PRURL != recoverPR {
		t.Errorf("Settlement = %+v, want complete naming %s", st, recoverPR)
	}
	if len(e.fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", e.fc.CreateDraftPRCalls)
	}
	call := e.fc.CreateDraftPRCalls[0]
	if call.Head != e.fc.AgentBranch(recoverNum) || call.Base != "main" {
		t.Errorf("draft PR head/base = %q/%q, want %q/main", call.Head, call.Base, e.fc.AgentBranch(recoverNum))
	}
	if !strings.Contains(call.Body, "Closes #"+recoverNum) {
		t.Errorf("draft PR body = %q, want it to contain %q", call.Body, "Closes #"+recoverNum)
	}
	if e.fc.Merged != recoverPR {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", recoverPR, e.fc.Merged)
	}
	iss, _ := e.fc.Issue(recoverNum)
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("want agent-complete and not agent-failed after a relayed-and-merged landing; labels=%v", iss.Labels)
	}
}

// A recover landing persists the PR but not the run's pass (issue #5189):
// Recover's Result carries no Passes, so the recorded pass is cleared.
func TestRecover_RelayRecordsLandingAndClearsPass(t *testing.T) {
	e := newRecoverEnv(t, false)
	e.s = newTestSettle(e.c, e.fc.AsLocalShaped(), e.fc.AsGithubReadOnly())
	if _, err := e.s.Recover(e.in("", readyReport(), implementThenLandPasses(), false)); err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}

	wantLanding := []forge.RecordLandingCall{{Num: recoverNum, Landing: recoverPR}}
	if !reflect.DeepEqual(e.fc.RecordLandingCalls, wantLanding) {
		t.Errorf("RecordLanding calls = %+v, want %+v", e.fc.RecordLandingCalls, wantLanding)
	}
	wantPass := []forge.RecordLandingPassCall{{Num: recoverNum, Pass: 0, Kind: ""}}
	if !reflect.DeepEqual(e.fc.RecordLandingPassCalls, wantPass) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", e.fc.RecordLandingPassCalls, wantPass)
	}
}

// The parked-relay recovery path (issue #4651): the issue wears only
// agent-failed (settle parked an exhausted bundle relay) and a local
// `spindrift recover` never re-claims it, so landing the preserved bundle
// must leave agent-complete without the stale agent-failed.
func TestRecover_LandsFromParkedFailedIssue(t *testing.T) {
	e := newRecoverEnv(t, true)
	e.fc.SetIssue(forge.Issue{Number: recoverNum, Labels: []string{"agent-failed"}})

	// The parked run's own outcome line, resolved the way recover does.
	logPath := filepath.Join(t.TempDir(), "issue.log")
	line := "SPINDRIFT_OUTCOME issue=" + recoverNum + " landing=" + e.fc.AgentBranch(recoverNum) + " status=ready note=ok\n"
	if err := os.WriteFile(logPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := outcome.Resolve([]outcome.PassLog{{Path: logPath}}, "")
	if err != nil {
		t.Fatalf("outcome.Resolve: %v", err)
	}

	if _, err := e.s.Recover(e.in("", resolved, nil, false)); err != nil {
		t.Fatalf("Recover err = %v, want nil", err)
	}
	if want := (forge.RelayBundleCall{OutboxDir: e.outbox, Ref: e.fc.AgentBranch(recoverNum)}); len(e.fc.RelayBundleCalls) != 1 || e.fc.RelayBundleCalls[0] != want {
		t.Fatalf("RelayBundleCalls = %+v, want exactly [%+v]", e.fc.RelayBundleCalls, want)
	}
	if e.fc.Merged != recoverPR {
		t.Errorf("expected Merge(%q) to have run; fc.Merged=%q", recoverPR, e.fc.Merged)
	}
	iss, _ := e.fc.Issue(recoverNum)
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("want agent-complete without the parked agent-failed; labels=%v", iss.Labels)
	}
}

// A self-report that isn't a genuine success must not relay, and unlike
// Settle's own failure path it must leave the issue's labels completely
// untouched: main's recover failure handling owns the operator-park decision.
func TestRecover_NonSuccessSelfReportRelaysNothing(t *testing.T) {
	e := newRecoverEnv(t, false)
	blocked := outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: "blocked"}}
	_, err := e.s.Recover(e.in("", blocked, nil, false))
	if !errors.Is(err, ErrNothingToRecover) {
		t.Fatalf("Recover err = %v, want ErrNothingToRecover", err)
	}
	e.assertSettledNothing(t)
	iss, _ := e.fc.Issue(recoverNum)
	if !containsLabel(iss.Labels, "agent-in-progress") || containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue labels must stay untouched; labels=%v", iss.Labels)
	}
}
