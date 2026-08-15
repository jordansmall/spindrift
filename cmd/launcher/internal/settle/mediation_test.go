package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// TestMediation_RequiredCapabilityError_NoBundleRelay asserts the
// unconditional bundle-relay check fires first, regardless of prShaped, when
// the underlying Code Forge implements neither BundleRelay nor PRForge (the
// git push-only shape).
func TestMediation_RequiredCapabilityError_NoBundleRelay(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	m := NewMediation(fc.AsPushOnly(), fc.AsNoLandingRecorder(), nil, "main")

	err := m.RequiredCapabilityError("git", false)
	if err == nil {
		t.Fatal("expected a non-nil error when the Code Forge lacks forge.BundleRelay")
	}
	want := `BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE="git" does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off; only CODE_FORGE=local implements it today`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestMediation_RequiredCapabilityError_PRShapedNoDraftPRCreator asserts that
// when prShaped is true and BundleRelay is present but DraftPRCreator is
// not, the draft-PR-create error fires.
func TestMediation_RequiredCapabilityError_PRShapedNoDraftPRCreator(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	m := NewMediation(fc.AsLocal(), fc.AsNoLandingRecorder(), nil, "main")

	err := m.RequiredCapabilityError("local", true)
	if err == nil {
		t.Fatal("expected a non-nil error when prShaped and the Code Forge lacks forge.DraftPRCreator")
	}
	want := `BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE="local" does not implement host-side draft-PR-create (forge.DraftPRCreator); not yet available on CODE_FORGE=github`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestMediation_RequiredCapabilityError_PRShapedNoBundleCommitSubjects
// asserts the new check (issue #2501): BundleRelay and DraftPRCreator are
// both present, but BundleCommitSubjects is not -- the stale gate gap the
// issue calls out.
func TestMediation_RequiredCapabilityError_PRShapedNoBundleCommitSubjects(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	readOnly := fc.AsGithubReadOnly()
	wrapper := readOnlyForgeWithoutCommitSubjects{
		CodeForge:      readOnly,
		PRForge:        readOnly.(forge.PRForge),
		BundleRelay:    readOnly.(forge.BundleRelay),
		DraftPRCreator: readOnly.(forge.DraftPRCreator),
	}
	m := NewMediation(wrapper, fc.AsNoLandingRecorder(), nil, "main")

	err := m.RequiredCapabilityError("github", true)
	if err == nil {
		t.Fatal("expected a non-nil error when prShaped and the Code Forge lacks forge.BundleCommitSubjects")
	}
	want := `BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE="github" does not implement commit-subjects (forge.BundleCommitSubjects), the reconstructed-PR fallback the host-mediated hand-off needs when a Box leaves no usable PR-intent line`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestMediation_RequiredCapabilityError_AllPresent asserts a Code Forge
// implementing every required capability passes the gate.
func TestMediation_RequiredCapabilityError_AllPresent(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), nil, "main")

	if err := m.RequiredCapabilityError("github", true); err != nil {
		t.Errorf("expected nil error when every required capability is present, got %v", err)
	}
}

// TestMediation_RequiredCapabilityError_NotPRShapedOnlyBundleRelay asserts a
// Code Forge with no PR concept at all (local) needs only BundleRelay when
// prShaped is false -- mirroring the git/local push-only shape.
func TestMediation_RequiredCapabilityError_NotPRShapedOnlyBundleRelay(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	m := NewMediation(fc.AsLocal(), fc.AsNoLandingRecorder(), nil, "main")

	if err := m.RequiredCapabilityError("local", false); err != nil {
		t.Errorf("expected nil error for a non-PR-shaped forge with only BundleRelay, got %v", err)
	}
}

// TestMediation_Open_IntentFound_HappyPath asserts the ordinary case: a
// found PR-intent line relays the branch, creates the PR with the intent's
// title/body plus a closes-reference, and reports TextSourceIntent.
func TestMediation_Open_IntentFound_HappyPath(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}

	url, created, source, err := m.Open(issNum, branch, result, FallbackNone)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if url != prURL {
		t.Errorf("url = %q, want %q", url, prURL)
	}
	if !created {
		t.Errorf("created = false, want true")
	}
	if source != TextSourceIntent {
		t.Errorf("source = %v, want TextSourceIntent", source)
	}
	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1919", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1919 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := forge.CreateDraftPRCall{Title: "feat: add widget", Body: "Adds a widget.\n\nCloses #" + issNum, Base: "main", Head: branch}
	if fc.CreateDraftPRCalls[0] != want {
		t.Errorf("CreateDraftPRCalls[0] = %+v, want %+v", fc.CreateDraftPRCalls[0], want)
	}
}

// TestMediation_Open_FallbackReconstruct_NoIntent_CommitsAvailable asserts
// FallbackReconstruct derives title/body from the relayed branch's own
// commits when no PR-intent line was found, reporting TextSourceReconstructed.
func TestMediation_Open_FallbackReconstruct_NoIntent_CommitsAvailable(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntentFound: false}

	url, created, source, err := m.Open(issNum, branch, result, FallbackReconstruct)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if url != prURL || !created {
		t.Errorf("url=%q created=%v, want %q true", url, created, prURL)
	}
	if source != TextSourceReconstructed {
		t.Errorf("source = %v, want TextSourceReconstructed", source)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if call.Title != "feat: add widget" {
		t.Errorf("Title = %q, want first commit subject %q", call.Title, "feat: add widget")
	}
	if !strings.Contains(call.Body, "Reconstructed host-side") {
		t.Errorf("Body = %q, want it to contain the reconstructed-host-side explanation", call.Body)
	}
}

// TestMediation_Open_FallbackReconstruct_NoIntent_ReconstructionFails asserts
// that when both the PR-intent line and the commit-based reconstruction fail,
// Open returns an error satisfying errors.Is(err, ErrNoPRIntent), and never
// calls CreateDraftPR.
func TestMediation_Open_FallbackReconstruct_NoIntent_ReconstructionFails(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CommitSubjectsErr = errors.New("commit subjects: git log failed")

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntentFound: false}

	_, _, _, err := m.Open(issNum, branch, result, FallbackReconstruct)
	if !errors.Is(err, ErrNoPRIntent) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNoPRIntent)", err)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls when reconstruction fails, got %+v", fc.CreateDraftPRCalls)
	}
}

// TestMediation_Open_FallbackNone_NoIntent asserts that with FallbackNone and
// no PR-intent line, Open returns errors.Is(err, ErrNoPRIntent) and never
// calls CreateDraftPR -- RelayBundle still runs first (issue #2447's
// always-relay behavior).
func TestMediation_Open_FallbackNone_NoIntent(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntentFound: false}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if !errors.Is(err, ErrNoPRIntent) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNoPRIntent)", err)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("RelayBundle must still be attempted, got %+v", fc.RelayBundleCalls)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls, got %+v", fc.CreateDraftPRCalls)
	}
}

// TestMediation_Open_FallbackDefault_NoIntent asserts FallbackDefault never
// fails: it derives an issue-title-derived default title/body, reports
// TextSourceDefault, and still creates the PR.
func TestMediation_Open_FallbackDefault_NoIntent(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL
	fc.SetIssue(forge.Issue{Number: issNum, Title: "Fix the widget"})

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntentFound: false}

	url, created, source, err := m.Open(issNum, branch, result, FallbackDefault)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if url != prURL || !created {
		t.Errorf("url=%q created=%v, want %q true", url, created, prURL)
	}
	if source != TextSourceDefault {
		t.Errorf("source = %v, want TextSourceDefault", source)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	call := fc.CreateDraftPRCalls[0]
	if call.Title != "Fix the widget" {
		t.Errorf("Title = %q, want issue-derived default %q", call.Title, "Fix the widget")
	}
	if !strings.Contains(call.Body, "Closes #"+issNum) {
		t.Errorf("Body = %q, want it to contain %q", call.Body, "Closes #"+issNum)
	}
}

// TestMediation_Open_RelayBundleFailure asserts a RelayBundle failure is
// wrapped and returned, with no CreateDraftPR call made.
func TestMediation_Open_RelayBundleFailure(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.RelayBundleErr = errors.New("bundle missing")

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err == nil {
		t.Fatal("expected a non-nil error when RelayBundle fails")
	}
	if !errors.Is(err, fc.RelayBundleErr) {
		t.Errorf("err = %v, want it to wrap %v", err, fc.RelayBundleErr)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls when the relay fails, got %+v", fc.CreateDraftPRCalls)
	}
}

// TestMediation_Open_CreateDraftPRFailure asserts a CreateDraftPR failure is
// wrapped and returned.
func TestMediation_Open_CreateDraftPRFailure(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRErr = errors.New("create draft PR: 500")

	m := NewMediation(fc.AsGithubReadOnly(), fc.AsNoLandingRecorder(), func(num string) string { return "/outbox/" + num }, "main")
	result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err == nil {
		t.Fatal("expected a non-nil error when CreateDraftPR fails")
	}
	if !errors.Is(err, fc.CreateDraftPRErr) {
		t.Errorf("err = %v, want it to wrap %v", err, fc.CreateDraftPRErr)
	}
}
