package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/passmanifest"
)

// newTestMediation resolves the capability set every Mediation.Open test here
// shares: a forge with BundleRelay, DraftPRCreator and BundleCommitSubjects,
// and a tracker without LandingRecorder.
func newTestMediation(fc *forge.Fake) *Mediation {
	noLanding := fc.AsNoLandingRecorder()
	caps := forge.ResolveCapabilities(fc.AsGithubReadOnly(), noLanding, backend.Descriptor{}, backend.Descriptor{})
	return NewMediation(caps, noLanding, func(num string) string { return "/outbox/" + num }, "main")
}

// An unset or error-path source must not read as "the box's own PR-intent
// line", which never happened, so TextSourceUnknown has to be the zero value.
func TestTextSourceUnknown_IsZeroValue(t *testing.T) {
	var s TextSource
	if s != TextSourceUnknown {
		t.Errorf("zero value of TextSource = %v, want TextSourceUnknown", s)
	}
	if TextSourceUnknown == TextSourceIntent {
		t.Errorf("TextSourceUnknown must be distinct from TextSourceIntent")
	}
}

func TestMediation_Open_IntentFound_HappyPath(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := newTestMediation(fc)
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

func TestMediation_Open_FallbackReconstruct_NoIntent_CommitsAvailable(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL
	fc.CommitSubjectsResult = []string{"feat: add widget", "fix: typo"}

	m := newTestMediation(fc)
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

func TestMediation_Open_FallbackReconstruct_NoIntent_ReconstructionFails(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CommitSubjectsErr = errors.New("commit subjects: git log failed")

	m := newTestMediation(fc)
	result := dispatch.Result{PRIntentFound: false}

	_, _, source, err := m.Open(issNum, branch, result, FallbackReconstruct)
	if !errors.Is(err, ErrNoPRIntent) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNoPRIntent)", err)
	}
	if !errors.Is(err, fc.CommitSubjectsErr) {
		t.Errorf("err = %v, want errors.Is(err, %v) (the underlying reconstruction failure)", err, fc.CommitSubjectsErr)
	}
	if strings.Contains(err.Error(), "found: no usable PR-intent line found") {
		t.Errorf("err.Error() = %q, want no stutter of ErrNoPRIntent's own text", err.Error())
	}
	if source != TextSourceUnknown {
		t.Errorf("source = %v, want TextSourceUnknown on an error return", source)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls when reconstruction fails, got %+v", fc.CreateDraftPRCalls)
	}
}

// RelayBundle still runs before Open rejects the missing intent, which is
// issue #2447's always-relay behavior.
func TestMediation_Open_FallbackNone_NoIntent(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)

	m := newTestMediation(fc)
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

func TestMediation_Open_FallbackDefault_NoIntent(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL
	fc.SetIssue(forge.Issue{Number: issNum, Title: "Fix the widget"})

	m := newTestMediation(fc)
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

func TestMediation_Open_RelayBundleFailure(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.RelayBundleErr = errors.New("bundle missing")

	m := newTestMediation(fc)
	result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}

	_, _, source, err := m.Open(issNum, branch, result, FallbackNone)
	if err == nil {
		t.Fatal("expected a non-nil error when RelayBundle fails")
	}
	if !errors.Is(err, fc.RelayBundleErr) {
		t.Errorf("err = %v, want it to wrap %v", err, fc.RelayBundleErr)
	}
	if !errors.Is(err, errRelayBundle) {
		t.Errorf("err = %v, want it to wrap errRelayBundle", err)
	}
	if source != TextSourceUnknown {
		t.Errorf("source = %v, want TextSourceUnknown on an error return", source)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls when the relay fails, got %+v", fc.CreateDraftPRCalls)
	}
}

// Issue #3244: a known, non-zero delta appends its Summary() line after the
// closes-reference.
func TestMediation_Open_LandDelta_Present(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := newTestMediation(fc)
	result := dispatch.Result{
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement"},
			{Pass: 2, Kind: "land", LandDelta: &landdelta.Delta{Known: true, Files: 2, Insertions: 41, Deletions: 3}},
		},
	}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if len(fc.CreateDraftPRCalls) != 1 {
		t.Fatalf("CreateDraftPRCalls = %+v, want exactly 1", fc.CreateDraftPRCalls)
	}
	want := "Adds a widget.\n\nCloses #" + issNum + "\n\npost-approval land delta: 2 files changed, 41 insertions(+), 3 deletions(-)"
	if got := fc.CreateDraftPRCalls[0].Body; got != want {
		t.Errorf("Body = %q, want %q", got, want)
	}
}

// Issue #3244: a zero delta is stated explicitly in the appended line, never
// omitted.
func TestMediation_Open_LandDelta_Zero(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := newTestMediation(fc)
	result := dispatch.Result{
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "land", LandDelta: &landdelta.Delta{Known: true}},
		},
	}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	want := "Adds a widget.\n\nCloses #" + issNum + "\n\npost-approval land delta: none — landing did not alter the reviewed tree"
	if got := fc.CreateDraftPRCalls[0].Body; got != want {
		t.Errorf("Body = %q, want %q", got, want)
	}
}

// Issue #3244: the manifest is Box-authored, so Reason is untrusted input even
// though landdelta.Compute only ever emits fixed strings. A crafted Reason
// carrying a newline and a closing keyword must not forge a body section or an
// auto-close reference against an unrelated issue.
func TestMediation_Open_LandDelta_Unknown(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := newTestMediation(fc)
	result := dispatch.Result{
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "land", LandDelta: &landdelta.Delta{Known: false, Reason: "no reviewed-commit anchor\n\n## forged section\ncloses #999"}},
		},
	}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	body := fc.CreateDraftPRCalls[0].Body
	if strings.Contains(body, "\n\n## forged section") {
		t.Errorf("Body = %q, want the embedded newline collapsed rather than forging a new section", body)
	}
	if hasClosingReference(body, "999") {
		t.Errorf("Body = %q, want the embedded closing keyword defused, not left live against #999", body)
	}
	if !strings.Contains(body, "post-approval land delta: unknown (no reviewed-commit anchor") {
		t.Errorf("Body = %q, want it to contain the unknown-delta line", body)
	}
}

// A manifest with no land entry (an older Box, or a run that never reached
// land) appends nothing, leaving the body byte-identical to the pre-#3244 one.
func TestMediation_Open_LandDelta_NoLandEntry(t *testing.T) {
	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRURL = prURL

	m := newTestMediation(fc)
	result := dispatch.Result{
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement"},
			{Pass: 2, Kind: "review", Verdict: "APPROVE"},
		},
	}

	_, _, _, err := m.Open(issNum, branch, result, FallbackNone)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	want := "Adds a widget.\n\nCloses #" + issNum
	if got := fc.CreateDraftPRCalls[0].Body; got != want {
		t.Errorf("Body = %q, want %q (unchanged from before issue #3244)", got, want)
	}
}

func TestMediation_Open_CreateDraftPRFailure(t *testing.T) {
	const issNum = "1919"
	const branch = "agent/issue-1919"

	fc := forge.NewFake(testDispatchLabels)
	fc.CreateDraftPRErr = errors.New("create draft PR: 500")

	m := newTestMediation(fc)
	result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}

	_, _, source, err := m.Open(issNum, branch, result, FallbackNone)
	if err == nil {
		t.Fatal("expected a non-nil error when CreateDraftPR fails")
	}
	if !errors.Is(err, fc.CreateDraftPRErr) {
		t.Errorf("err = %v, want it to wrap %v", err, fc.CreateDraftPRErr)
	}
	if !errors.Is(err, errCreateDraftPR) {
		t.Errorf("err = %v, want it to wrap errCreateDraftPR", err)
	}
	if source != TextSourceUnknown {
		t.Errorf("source = %v, want TextSourceUnknown on an error return", source)
	}
}
