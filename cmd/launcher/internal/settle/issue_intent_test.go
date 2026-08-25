package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/outcome"
)

// TestFileIssueIntents_FilesEachIntentWithHostDerivedLabels verifies the
// 1-to-many host-mediated issue-filing relay channel (issue #2018): every
// decoded SPINDRIFT_ISSUE_INTENT payload in Result.IssueIntents is filed via
// the tracker's HostPostedIssueFiler with the caller-supplied provenance
// label — never the payload's own "labels" field (issue #1949's
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

	urls := fileIssueIntents(fc.AsIssueFiler(), "1", result, "agent-review-finding")

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
	want0 := forge.PostIssueCall{Title: "first bug", Body: "first body", Labels: []string{"agent-review-finding"}}
	if fc.PostIssueCalls[0].Title != want0.Title || fc.PostIssueCalls[0].Body != want0.Body {
		t.Errorf("PostIssueCalls[0] = %+v, want title/body %+v", fc.PostIssueCalls[0], want0)
	}
	if len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != want0.Labels[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", fc.PostIssueCalls[0].Labels, want0.Labels)
	}
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "evil-label" {
			t.Errorf("PostIssueCalls[0].Labels leaked the payload's own label: %v", fc.PostIssueCalls[0].Labels)
		}
	}
	want1 := forge.PostIssueCall{Title: "second bug", Body: "second body", Labels: []string{"agent-review-finding"}}
	if fc.PostIssueCalls[1].Title != want1.Title || fc.PostIssueCalls[1].Body != want1.Body {
		t.Errorf("PostIssueCalls[1] = %+v, want title/body %+v", fc.PostIssueCalls[1], want1)
	}
	if len(fc.PostIssueCalls[1].Labels) != 1 || fc.PostIssueCalls[1].Labels[0] != want1.Labels[0] {
		t.Errorf("PostIssueCalls[1].Labels = %v, want %v", fc.PostIssueCalls[1].Labels, want1.Labels)
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

	urls := fileIssueIntents(fc.AsIssueFiler(), "1", result, "agent-review-finding")

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

	urls := fileIssueIntents(fc, "1", result, "agent-review-finding")

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want none", urls)
	}
}

// TestFileIssueIntents_RealLocalTracker_FilesIssueOnDisk exercises the relay
// path (issue #2018) end-to-end against a real *local.LocalTracker as the
// HostPostedIssueFiler, not just the interface-satisfaction assertion the
// fake-backed tests above cover: fileIssueIntents type-asserts the it
// parameter, and a
// real adapter is the only way to prove that assertion actually reaches a
// tracker that writes to disk.
func TestFileIssueIntents_RealLocalTracker_FilesIssueOnDisk(t *testing.T) {
	dir := t.TempDir()
	lt := local.NewLocalTracker(dir, testDispatchLabels)

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body","labels":["evil-label"]}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	urls := fileIssueIntents(lt, "1", result, "agent-review-finding")

	if len(urls) != 2 {
		t.Fatalf("urls = %v, want 2", urls)
	}
	wantSlugs := []string{"local:first-bug", "local:second-bug"}
	for i, want := range wantSlugs {
		if urls[i] != want {
			t.Errorf("urls[%d] = %q, want %q", i, urls[i], want)
		}
	}

	// Read each filed issue back through the tracker's own read path,
	// proving the relay actually landed a file on disk with the expected
	// title/body and host-derived labels -- never the payload's own
	// "labels" field (issue #1949).
	iss0, err := lt.Issue(strings.TrimPrefix(urls[0], "local:"))
	if err != nil {
		t.Fatalf("Issue(%s): %v", urls[0], err)
	}
	if iss0.Title != "first bug" || iss0.Body != "first body" {
		t.Errorf("iss0 = %+v, want title %q body %q", iss0, "first bug", "first body")
	}
	foundLabel, leakedLabel := false, false
	for _, l := range iss0.Labels {
		if l == "agent-review-finding" {
			foundLabel = true
		}
		if l == "evil-label" {
			leakedLabel = true
		}
	}
	if !foundLabel {
		t.Errorf("iss0.Labels = %v, want %q", iss0.Labels, "agent-review-finding")
	}
	if leakedLabel {
		t.Errorf("iss0.Labels = %v, leaked the payload's own label", iss0.Labels)
	}

	iss1, err := lt.Issue(strings.TrimPrefix(urls[1], "local:"))
	if err != nil {
		t.Fatalf("Issue(%s): %v", urls[1], err)
	}
	if iss1.Title != "second bug" || iss1.Body != "second body" {
		t.Errorf("iss1 = %+v, want title %q body %q", iss1, "second bug", "second body")
	}
}

// TestFileIssueIntents_ArbitraryProvenanceLabel verifies fileIssueIntents
// files under whatever provenanceLabel the caller supplies, not a label
// baked into the routine itself — "some-other-caller-label" here is a
// deliberately arbitrary placeholder, not ADR 0041's real
// "agent-research-finding" label (that label isn't registered in
// lib/labels.nix and isn't what this test is exercising). This is the seam
// a future research-settle caller (issue #2590, ResearchSettle in
// research.go) needs: fileIssueIntents is a package-level function, not a
// *Settle method, so ResearchSettle can call it directly without
// fabricating a *Settle it has no Config/push-only forge.CodeForge for.
func TestFileIssueIntents_ArbitraryProvenanceLabel(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/55"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"research finding","body":"body"}`,
		},
	}

	urls := fileIssueIntents(fc.AsIssueFiler(), "1", result, "some-other-caller-label")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	if len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "some-other-caller-label" {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [some-other-caller-label]", fc.PostIssueCalls[0].Labels)
	}
	if len(urls) != 1 || urls[0] != fc.PostIssueURL {
		t.Errorf("urls = %v, want [%s]", urls, fc.PostIssueURL)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
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
	// Pins gate.go's own call site to the "agent-review-finding" provenance
	// label -- unlike the direct fileIssueIntents calls above, this test
	// drives the real Settle.Settle -> gate.go work path, so this is the
	// assertion that actually guards gate.go's literal argument.
	if len(fc.PostIssueCalls) == 1 && (len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "agent-review-finding") {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [agent-review-finding]", fc.PostIssueCalls[0].Labels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
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
	// Pins gate.go's own call site to the "agent-review-finding" provenance
	// label on the blocked path too -- see the matching assertion in
	// TestSettle_FilesIssueIntents_OnReadyOutcome above.
	if len(fc.PostIssueCalls) == 1 && (len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "agent-review-finding") {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [agent-review-finding]", fc.PostIssueCalls[0].Labels)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	s := New(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
}

// TestFileIssueIntentsDetailed_ReturnsSuccessAndFailureEntries verifies
// fileIssueIntentsDetailed reports both branches -- success and failure --
// as filedIntent entries rather than silently dropping failures the way
// fileIssueIntents' URL-only return does. fc.PostIssueErr scripts every
// PostIssue call on a given fake uniformly, so the all-success and
// all-failure cases each need their own fake to exercise independently.
func TestFileIssueIntentsDetailed_ReturnsSuccessAndFailureEntries(t *testing.T) {
	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body"}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	t.Run("all success", func(t *testing.T) {
		fc := forge.NewFake(testDispatchLabels)
		fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

		detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

		if len(detailed) != 2 {
			t.Fatalf("detailed = %+v, want 2 entries", detailed)
		}
		for i, d := range detailed {
			if d.Failed {
				t.Errorf("detailed[%d].Failed = true, want false", i)
			}
			if d.URL != fc.PostIssueURL {
				t.Errorf("detailed[%d].URL = %q, want %q", i, d.URL, fc.PostIssueURL)
			}
		}
	})

	t.Run("all failure", func(t *testing.T) {
		fc := forge.NewFake(testDispatchLabels)
		fc.PostIssueErr = errFake

		detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

		if len(detailed) != 2 {
			t.Fatalf("detailed = %+v, want 2 entries", detailed)
		}
		wantBodies := []string{"first body", "second body"}
		for i, d := range detailed {
			if !d.Failed {
				t.Errorf("detailed[%d].Failed = false, want true", i)
			}
			if d.URL != "" {
				t.Errorf("detailed[%d].URL = %q, want empty", i, d.URL)
			}
			if d.Body != wantBodies[i] {
				t.Errorf("detailed[%d].Body = %q, want %q", i, d.Body, wantBodies[i])
			}
		}
	})
}

// TestFileIssueIntentsDetailed_AppendsBacklinkToPostedBody verifies a
// non-empty bodyBacklink is appended to the posted issue's body -- e.g. a
// research-settle caller (issue #2590) attributing a filed issue back to
// the research run that found it -- without touching the Body reported
// on failure, which stays the intent's own original body.
func TestFileIssueIntentsDetailed_AppendsBacklinkToPostedBody(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "Filed from research on #99")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := "first body" + "\n\n" + "Filed from research on #99"
	if fc.PostIssueCalls[0].Body != want {
		t.Errorf("PostIssueCalls[0].Body = %q, want %q", fc.PostIssueCalls[0].Body, want)
	}
}

// TestFileIssueIntentsDetailed_EmptyBacklinkLeavesBodyUnchanged verifies an
// empty bodyBacklink posts the intent's body byte-for-byte, with no trailing
// separator or text -- proving fileIssueIntents' existing wrapper behavior
// (which always passes "") is preserved unchanged by this refactor.
func TestFileIssueIntentsDetailed_EmptyBacklinkLeavesBodyUnchanged(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	if fc.PostIssueCalls[0].Body != "first body" {
		t.Errorf("PostIssueCalls[0].Body = %q, want %q", fc.PostIssueCalls[0].Body, "first body")
	}
}

// TestFileIssueIntentsDetailed_MalformedPayloadSkipped mirrors
// TestFileIssueIntents_MalformedPayloadSkipped but asserts on the detailed
// return value: a malformed or blank-title payload never had a title to
// file or degrade with, so it produces no filedIntent entry at all -- not
// even a Failed one.
func TestFileIssueIntentsDetailed_MalformedPayloadSkipped(t *testing.T) {
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

	detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(detailed) != 1 || detailed[0].Title != "good one" {
		t.Fatalf("detailed = %+v, want exactly the well-formed intent", detailed)
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
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
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
