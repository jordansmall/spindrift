package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestFileIssueIntents_FilesEachIntentWithHostDerivedLabels verifies the
// 1-to-many host-mediated issue-filing relay channel (issue #2018): every
// decoded SPINDRIFT_ISSUE_INTENT payload in Result.IssueIntents is filed via
// the tracker's HostPostedIssueFiler with the launcher's own fixed label
// set — never the payload's own "labels" field (issue #1949's
// do-not-trust-the-agent-target invariant, extended from destination repo to
// labels).
func TestFileIssueIntents_FilesEachIntentWithHostDerivedLabels(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body","labels":["evil-label"]}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	c := baseConfig()
	s := New(c, fc.AsIssueFiler(), fc.AsPushOnly())
	urls := s.fileIssueIntents("1", result)

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
	want0 := forge.PostIssueCall{Title: "first bug", Body: "first body", Labels: issueIntentLabels}
	if fc.PostIssueCalls[0].Title != want0.Title || fc.PostIssueCalls[0].Body != want0.Body {
		t.Errorf("PostIssueCalls[0] = %+v, want title/body %+v", fc.PostIssueCalls[0], want0)
	}
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "evil-label" {
			t.Errorf("PostIssueCalls[0].Labels leaked the payload's own label: %v", fc.PostIssueCalls[0].Labels)
		}
	}
	want1 := forge.PostIssueCall{Title: "second bug", Body: "second body", Labels: issueIntentLabels}
	if fc.PostIssueCalls[1].Title != want1.Title || fc.PostIssueCalls[1].Body != want1.Body {
		t.Errorf("PostIssueCalls[1] = %+v, want title/body %+v", fc.PostIssueCalls[1], want1)
	}
	if len(urls) != 2 || urls[0] != fc.PostIssueURL || urls[1] != fc.PostIssueURL {
		t.Errorf("returned urls = %v, want [%s %s]", urls, fc.PostIssueURL, fc.PostIssueURL)
	}
}

// TestFileIssueIntents_MalformedPayloadSkipped verifies a payload that fails
// to decode as the issue-intent JSON shape (or carries a blank title) is
// skipped rather than filed or aborting the remaining, well-formed intents.
func TestFileIssueIntents_MalformedPayloadSkipped(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`not valid json`,
			`{"title":"","body":"blank title"}`,
			`{"title":"good one","body":"body"}`,
		},
	}

	c := baseConfig()
	s := New(c, fc.AsIssueFiler(), fc.AsPushOnly())
	urls := s.fileIssueIntents("1", result)

	if len(fc.PostIssueCalls) != 1 || fc.PostIssueCalls[0].Title != "good one" {
		t.Fatalf("PostIssueCalls = %+v, want exactly the well-formed intent", fc.PostIssueCalls)
	}
	if len(urls) != 1 {
		t.Errorf("urls = %v, want exactly 1", urls)
	}
}

// TestFileIssueIntents_TrackerWithoutHostPostedIssueFilerNoOps verifies a
// tracker that doesn't implement forge.HostPostedIssueFiler (every real
// adapter today) leaves fileIssueIntents a no-op rather than panicking —
// this is a best-effort side channel, not part of the run's own landing
// decision.
func TestFileIssueIntents_TrackerWithoutHostPostedIssueFilerNoOps(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents:      []string{`{"title":"first","body":"body"}`},
	}

	c := baseConfig()
	s := New(c, fc, fc.AsPushOnly())
	urls := s.fileIssueIntents("1", result)

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want none", urls)
	}
}

// TestSettle_FilesIssueIntents_OnReadyOutcome verifies the full Settle entry
// point -- not just the standalone fileIssueIntents helper above -- actually
// drives the host-mediated issue-filing relay (issue #2019, wiring #2018's
// dormant fileIssueIntents into Settle.Settle) on the "ready" outcome path.
func TestSettle_FilesIssueIntents_OnReadyOutcome(t *testing.T) {
	const issNum = "2019"
	const prURL = "https://github.com/owner/repo/pull/2019"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/77"

	result := dispatch.Result{
		Success:           true,
		OutcomeFound:      true,
		Outcome:           outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(auth): validate token expiry","body":"body"}`,
		},
	}

	s := New(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 || fc.PostIssueCalls[0].Title != "fix(auth): validate token expiry" {
		t.Errorf("PostIssueCalls = %+v, want exactly the one intent filed", fc.PostIssueCalls)
	}
}

// TestSettle_FilesIssueIntents_OnBlockedOutcome verifies filing fires on the
// "blocked" path too, not only "ready" -- the relay is best-effort and
// orthogonal to whether this run's own PR ever went green.
func TestSettle_FilesIssueIntents_OnBlockedOutcome(t *testing.T) {
	const issNum = "2019"
	const prURL = "https://github.com/owner/repo/pull/2019"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/78"

	result := dispatch.Result{
		Success:           true,
		OutcomeFound:      true,
		Outcome:           outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(auth): validate token expiry","body":"body"}`,
		},
	}

	s := New(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Errorf("PostIssueCalls = %+v, want exactly 1", fc.PostIssueCalls)
	}
}

// TestSettle_NoIssueIntentsFound_NoFilingAttempted verifies the common
// read-write path (the Filer files directly via `gh issue create` in-box, so
// the box log carries no SPINDRIFT_ISSUE_INTENT line at all) drives no
// PostIssue call through Settle -- byte-for-byte the pre-#2019 behavior.
func TestSettle_NoIssueIntentsFound_NoFilingAttempted(t *testing.T) {
	const issNum = "2019"
	const prURL = "https://github.com/owner/repo/pull/2019"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
	}

	s := New(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
}

// TestSettle_IssueIntentFilingFailure_DoesNotBlockOutcome verifies a filing
// failure (PostIssue error) never changes the run's own landing decision --
// AC5's best-effort guarantee: the "ready" outcome still merges through
// selfHeal even though the relay itself failed.
func TestSettle_IssueIntentFilingFailure_DoesNotBlockOutcome(t *testing.T) {
	const issNum = "2019"
	const prURL = "https://github.com/owner/repo/pull/2019"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PostIssueErr = errFake

	result := dispatch.Result{
		Success:           true,
		OutcomeFound:      true,
		Outcome:           outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"fix(auth): validate token expiry","body":"body"}`,
		},
	}

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\ncost: 0.10"
	s := New(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Errorf("PostIssueCalls = %+v, want the failed attempt recorded", fc.PostIssueCalls)
	}
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("a failed issue-intent file must not block the run's own ready/merge flow; CommentCalls = %+v", fc.CommentCalls)
	}
}
