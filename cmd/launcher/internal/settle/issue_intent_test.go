package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
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
