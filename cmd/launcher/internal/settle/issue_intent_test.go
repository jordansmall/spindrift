package settle

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/outcome"
)

// withEmptyMarker is the body a term-less intent actually files: the
// launcher appends its own marker line unconditionally, empty when the
// intent carried no usable dedup term (issue #3609 review).
func withEmptyMarker(body string) string {
	return body + "\n\n" + dedupMarkerPrefix + dedupMarkerSuffix
}

// filedURLs returns filed's successful entries' URLs in payload order,
// mirroring the URL-only shape the now-deleted fileIssueIntents wrapper used
// to return, so tests written against that shape keep their assertions.
func filedURLs(filed []filedIntent) []string {
	var urls []string
	for _, f := range filed {
		if !f.Failed {
			urls = append(urls, f.URL)
		}
	}
	return urls
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn wrote, matching the capture idiom already used elsewhere in
// this package (settle_entry_test.go).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(captured)
}

// Pins the 1-to-many host-mediated issue-filing relay (issue #2018): every
// decoded SPINDRIFT_ISSUE_INTENT payload is filed through the tracker's
// HostPostedIssueFiler with the caller-supplied provenance label, never the
// payload's own "labels" field (issue #1949's do-not-trust-the-agent-target
// invariant, extended from destination repo to labels).
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

	urls := filedURLs(fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", ""))

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
	want0 := forge.PostIssueCall{Title: "first bug", Body: withEmptyMarker("first body"), Labels: []string{"agent-review-finding"}}
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
	want1 := forge.PostIssueCall{Title: "second bug", Body: withEmptyMarker("second body"), Labels: []string{"agent-review-finding"}}
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

// A payload that fails to decode as the issue-intent JSON shape, or carries a
// blank title, is skipped rather than filed or aborting the remaining
// well-formed intents.
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

	urls := filedURLs(fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", ""))

	if len(fc.PostIssueCalls) != 1 || fc.PostIssueCalls[0].Title != "good one" {
		t.Fatalf("PostIssueCalls = %+v, want exactly the well-formed intent", fc.PostIssueCalls)
	}
	if len(urls) != 1 {
		t.Errorf("urls = %v, want exactly 1", urls)
	}
}

// A tracker that doesn't implement forge.HostPostedIssueFiler (every real
// adapter today) leaves fileIssueIntentsDetailed a no-op rather than
// panicking. The relay is a best-effort side channel, not part of the run's
// own landing decision.
func TestFileIssueIntents_TrackerWithoutHostPostedIssueFilerNoOps(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents:      []string{`{"title":"first","body":"body"}`},
	}

	urls := filedURLs(fileIssueIntentsDetailed(fc, "1", result, "agent-review-finding", ""))

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
	if len(urls) != 0 {
		t.Errorf("urls = %v, want none", urls)
	}
}

// Exercises the relay (issue #2018) against a real *local.LocalTracker rather
// than the fake: fileIssueIntentsDetailed type-asserts its it parameter, and
// a real adapter is the only way to prove that assertion reaches a tracker
// that writes to disk.
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

	urls := filedURLs(fileIssueIntentsDetailed(lt, "1", result, "agent-review-finding", ""))

	if len(urls) != 2 {
		t.Fatalf("urls = %v, want 2", urls)
	}
	wantSlugs := []string{"local:first-bug", "local:second-bug"}
	for i, want := range wantSlugs {
		if urls[i] != want {
			t.Errorf("urls[%d] = %q, want %q", i, urls[i], want)
		}
	}

	// Read back through the tracker's own read path: the labels on disk must
	// be the host-derived ones, never the payload's own "labels" field
	// (issue #1949).
	iss0, err := lt.Issue(strings.TrimPrefix(urls[0], "local:"))
	if err != nil {
		t.Fatalf("Issue(%s): %v", urls[0], err)
	}
	if iss0.Title != "first bug" || iss0.Body != withEmptyMarker("first body") {
		t.Errorf("iss0 = %+v, want title %q body %q", iss0, "first bug", withEmptyMarker("first body"))
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
	if iss1.Title != "second bug" || iss1.Body != withEmptyMarker("second body") {
		t.Errorf("iss1 = %+v, want title %q body %q", iss1, "second bug", withEmptyMarker("second body"))
	}
}

// The provenance label comes from the caller, not from a label baked into the
// routine. "some-other-caller-label" is a deliberate placeholder, not ADR
// 0041's real "agent-research-finding" (which isn't registered in
// lib/labels.nix). A research-settle caller (issue #2590) needs this seam:
// fileIssueIntentsDetailed is a package-level function, not a *Settle method.
func TestFileIssueIntents_ArbitraryProvenanceLabel(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/55"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"research finding","body":"body"}`,
		},
	}

	urls := filedURLs(fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "some-other-caller-label", ""))

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

// The full Settle entry point, not just the standalone fileIssueIntents helper
// above, drives the filing relay on the "ready" outcome path. Issue #2019
// wired #2018's dormant fileIssueIntents into Settle.Settle.
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

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 || fc.PostIssueCalls[0].Title != "fix(auth): validate token expiry" {
		t.Errorf("PostIssueCalls = %+v, want exactly the one intent filed", fc.PostIssueCalls)
	}
	// Guards gate.go's own literal argument: unlike the direct
	// fileIssueIntents calls above, this test drives the real work path from
	// Settle.Settle through gate.go.
	if len(fc.PostIssueCalls) == 1 && (len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "agent-review-finding") {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [agent-review-finding]", fc.PostIssueCalls[0].Labels)
	}
}

// Filing fires on the "blocked" path too, not only "ready". The relay is
// best-effort and orthogonal to whether this run's own PR ever went green.
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

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Errorf("PostIssueCalls = %+v, want exactly 1", fc.PostIssueCalls)
	}
	// Guards gate.go's own literal argument on the blocked path too. See the
	// matching assertion in TestSettle_FilesIssueIntents_OnReadyOutcome.
	if len(fc.PostIssueCalls) == 1 && (len(fc.PostIssueCalls[0].Labels) != 1 || fc.PostIssueCalls[0].Labels[0] != "agent-review-finding") {
		t.Errorf("PostIssueCalls[0].Labels = %v, want [agent-review-finding]", fc.PostIssueCalls[0].Labels)
	}
}

// The common read-write path (the Filer files directly via `gh issue create`
// in-box, so the box log carries no SPINDRIFT_ISSUE_INTENT line at all) drives
// no PostIssue call through Settle, byte-for-byte the pre-#2019 behavior.
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

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.PostIssueCalls) != 0 {
		t.Errorf("PostIssueCalls = %+v, want none", fc.PostIssueCalls)
	}
}

// fileIssueIntentsDetailed reports both success and failure as filedIntent
// entries, rather than silently dropping failures the way fileIssueIntents'
// URL-only return does. fc.PostIssueErr scripts every PostIssue call on a
// given fake uniformly, so each case needs its own fake.
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

// A non-empty bodyBacklink is appended to the posted issue's body, for example
// a research-settle caller (issue #2590) attributing a filed issue back to the
// research run that found it. The Body reported on failure stays the intent's
// own original body.
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
	want := withEmptyMarker("first body" + "\n\n" + "Filed from research on #99")
	if fc.PostIssueCalls[0].Body != want {
		t.Errorf("PostIssueCalls[0].Body = %q, want %q", fc.PostIssueCalls[0].Body, want)
	}
}

// An empty bodyBacklink adds no backlink separator -- only the dedup marker
// line every filing carries -- proving fileIssueIntents' wrapper behavior (it
// always passes "") survived the refactor.
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
	if fc.PostIssueCalls[0].Body != withEmptyMarker("first body") {
		t.Errorf("PostIssueCalls[0].Body = %q, want %q", fc.PostIssueCalls[0].Body, withEmptyMarker("first body"))
	}
}

// Mirrors TestFileIssueIntents_MalformedPayloadSkipped on the detailed return
// value: a malformed or blank-title payload never had a title to file or
// degrade with, so it produces no filedIntent entry at all, not even a Failed
// one.
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

// A payload's optional "type" field, when it names one of the closed set of
// recognized finding types (issue #2594 / ADR 0041), is ensure-created and
// applied alongside the caller's provenanceLabel: provenance first, type
// second.
func TestFileIssueIntentsDetailed_RecognizedTypeAppliesMappedLabel(t *testing.T) {
	for _, typ := range []string{"bug", "enhancement", "chore"} {
		t.Run(typ, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

			result := dispatch.Result{
				IssueIntentsFound: true,
				IssueIntents: []string{
					`{"title":"t","body":"b","type":"` + typ + `"}`,
				},
			}

			fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

			if len(fc.PostIssueCalls) != 1 {
				t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
			}
			want := []string{"agent-review-finding", typ}
			got := fc.PostIssueCalls[0].Labels
			if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
				t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
			}

			if len(fc.CreateLabelCalls) != 1 {
				t.Fatalf("CreateLabelCalls = %+v, want 1", fc.CreateLabelCalls)
			}
			wantMeta := doctor.FindingTypeLabels[typ]
			gotCall := fc.CreateLabelCalls[0]
			if gotCall.Name != typ || gotCall.Description != wantMeta.Description || gotCall.Color != wantMeta.Color {
				t.Errorf("CreateLabelCalls[0] = %+v, want {%q %q %q}", gotCall, typ, wantMeta.Description, wantMeta.Color)
			}
		})
	}
}

// AC1 of issue #2594 on the research-path provenance label, rather than the
// work-path one every other type-label test in this file uses.
func TestFileIssueIntentsDetailed_ResearchProvenanceWithTypeLabel(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-research-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := []string{"agent-research-finding", "bug"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
}

// A payload that omits the "type" key entirely files with only the provenance
// label: no CreateLabel call, no type label appended.
func TestFileIssueIntentsDetailed_AbsentTypeFilesUntyped(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := []string{"agent-review-finding"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
	if len(fc.CreateLabelCalls) != 0 {
		t.Errorf("CreateLabelCalls = %+v, want none", fc.CreateLabelCalls)
	}
}

// A "type" value outside the closed set never rejects or skips the payload. It
// still files, just without a type label.
func TestFileIssueIntentsDetailed_UnknownTypeFilesUntyped(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"feature"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := []string{"agent-review-finding"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
	if len(fc.CreateLabelCalls) != 0 {
		t.Errorf("CreateLabelCalls = %+v, want none", fc.CreateLabelCalls)
	}
}

// A Box smuggling a real dispatch label such as "ready-for-agent" through the
// "type" field never gets that label applied: the closed map (issue #2594's
// host-side type to label mapping) never echoes an arbitrary caller-supplied
// token back as a label, only its own three mapped tokens.
func TestFileIssueIntentsDetailed_UnrecognizedTypeCannotSmuggleADispatchLabel(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"ready-for-agent"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "ready-for-agent" {
			t.Fatalf("PostIssueCalls[0].Labels = %v, leaked a smuggled dispatch label", fc.PostIssueCalls[0].Labels)
		}
	}
	want := []string{"agent-review-finding"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
}

// ensureTypeLabel skips CreateLabel when ListLabels already reports the mapped
// label present. The label still gets applied to the filed issue, just without
// a redundant create call.
func TestFileIssueIntentsDetailed_LabelAlreadyExistsSkipsCreate(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.Labels = []string{"bug"}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.CreateLabelCalls) != 0 {
		t.Errorf("CreateLabelCalls = %+v, want none", fc.CreateLabelCalls)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	found := false
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "bug" {
			found = true
		}
	}
	if !found {
		t.Errorf("PostIssueCalls[0].Labels = %v, want to include %q", fc.PostIssueCalls[0].Labels, "bug")
	}
}

// A CreateLabel failure only drops the type label, not the whole filing. The
// issue still files successfully with just the provenance label.
func TestFileIssueIntentsDetailed_LabelCreateFailureFilesUntypedNonFatally(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.CreateLabelErr = errFake

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(detailed) != 1 || detailed[0].Failed || detailed[0].URL == "" {
		t.Fatalf("detailed = %+v, want a single successful entry", detailed)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := []string{"agent-review-finding"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
}

// The hoisted ListLabels error branch is non-fatal: with existingLabels
// unusable, ensureTypeLabel falls through to CreateLabel, which still succeeds
// and gets the type label applied.
func TestFileIssueIntentsDetailed_ListLabelsErrSkipsPreCheckButCreateSucceeds(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.ListLabelsErr = errFake

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(detailed) != 1 || detailed[0].Failed || detailed[0].URL == "" {
		t.Fatalf("detailed = %+v, want a single successful entry", detailed)
	}
	if len(fc.CreateLabelCalls) != 1 {
		t.Fatalf("CreateLabelCalls = %+v, want 1", fc.CreateLabelCalls)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	found := false
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "bug" {
			found = true
		}
	}
	if !found {
		t.Errorf("PostIssueCalls[0].Labels = %v, want to include %q", fc.PostIssueCalls[0].Labels, "bug")
	}
}

// Pins the finding-B bug: the hoisted ListLabels call misses the label (empty
// or stale), CreateLabel then fails because the label already exists, and
// ensureTypeLabel's retry ListLabels call reveals it was there all along. The
// label must still be applied, not dropped.
func TestFileIssueIntentsDetailed_CreateLabelFailsButLabelAlreadyExists_StillApplied(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.LabelsSeq = [][]string{{}, {"bug"}}
	fc.CreateLabelErr = errFake

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	found := false
	for _, l := range fc.PostIssueCalls[0].Labels {
		if l == "bug" {
			found = true
		}
	}
	if !found {
		t.Errorf("PostIssueCalls[0].Labels = %v, want to include %q despite the CreateLabel error", fc.PostIssueCalls[0].Labels, "bug")
	}
}

// The worst case, both the hoisted ListLabels call and the CreateLabel call
// failing, still degrades to an untyped but successful filing rather than
// failing the issue.
func TestFileIssueIntentsDetailed_ListLabelsErrAndCreateLabelErr_DropsLabelNonFatally(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.ListLabelsErr = errFake
	fc.CreateLabelErr = errFake

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","type":"bug"}`,
		},
	}

	detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")

	if len(detailed) != 1 || detailed[0].Failed || detailed[0].URL == "" {
		t.Fatalf("detailed = %+v, want a single successful entry", detailed)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	want := []string{"agent-review-finding"}
	got := fc.PostIssueCalls[0].Labels
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("PostIssueCalls[0].Labels = %v, want %v", got, want)
	}
}

// A filing failure (PostIssue error) never changes the run's own landing
// decision, AC5's best-effort guarantee: the "ready" outcome still merges
// through selfHeal even though the relay itself failed.
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
	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.PostIssueCalls) != 1 {
		t.Errorf("PostIssueCalls = %+v, want the failed attempt recorded", fc.PostIssueCalls)
	}
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("a failed issue-intent file must not block the run's own ready/merge flow; CommentCalls = %+v", fc.CommentCalls)
	}
}

// The work path (Settle.Settle via gate.go, not a direct
// fileIssueIntentsDetailed call) prints the filed= tally line even when the
// run carried no issue intents at all (issue #3608): "reached filing, filed
// nothing" must still show up in the transcript.
func TestSettle_ReportsFiledTally_NoIntents(t *testing.T) {
	const issNum = "3608"
	const prURL = "https://github.com/owner/repo/pull/3608"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	if !strings.Contains(stdout, "    #3608  filed=ok:0,failed:0,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the zero filed= tally", stdout)
	}
}

// The work path's filed= tally line counts two successfully filed intents as
// ok:2,failed:0,skipped:0 (issue #3608).
func TestSettle_ReportsFiledTally_AllOK(t *testing.T) {
	const issNum = "3608"
	const prURL = "https://github.com/owner/repo/pull/3608"

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
			`{"title":"first bug","body":"first body"}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	if !strings.Contains(stdout, "    #3608  filed=ok:2,failed:0,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the all-ok filed= tally", stdout)
	}
}

// The work path (Settle.Settle via gate.go) posts a standalone "## Skipped
// (deduplicated)" comment when a finding dedups against the open backlog:
// the work path has no filed-issues comment to append it to, so a dedup skip
// needs its own post to be visible anywhere but stdout (issue #3811).
func TestSettle_WorkPath_AllDedupedPostsSkippedComment(t *testing.T) {
	const issNum = "3811"
	const prURL = "https://github.com/owner/repo/pull/3811"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding of the same bug",
		Body:   "earlier body\n\n<!-- spindrift-dedup: race in settle -->",
		Labels: []string{"agent-review-finding"},
	})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a retry of the same finding","body":"new body","dedupTerms":["Race in Settle"]}`,
		},
	}

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	var skipComment string
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "## Skipped (deduplicated)") {
			skipComment = c.Body
		}
	}
	if skipComment == "" {
		t.Fatalf("CommentCalls = %+v, want one carrying a Skipped (deduplicated) section", fc.CommentCalls)
	}
	if !strings.Contains(skipComment, "#501") {
		t.Errorf("skip comment = %q, want it to name the matched backlog issue #501", skipComment)
	}
}

// A work-path run whose intents all file successfully posts no skipped
// comment at all: nothing was deduplicated, so there is nothing to
// acknowledge (issue #3811).
func TestSettle_WorkPath_AllFiledPostsNoSkippedComment(t *testing.T) {
	const issNum = "3811"
	const prURL = "https://github.com/owner/repo/pull/3811"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a genuinely new finding","body":"new body"}`,
		},
	}

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "## Skipped (deduplicated)") {
			t.Errorf("CommentCalls = %+v, want no Skipped (deduplicated) section (nothing was deduped)", fc.CommentCalls)
		}
	}
}

// The work path's filed= tally line counts two failed PostIssue calls as
// ok:0,failed:2,skipped:0 (issue #3608): a run that tried and failed to file must not
// read as a quiet run.
func TestSettle_ReportsFiledTally_AllFailed(t *testing.T) {
	const issNum = "3608"
	const prURL = "https://github.com/owner/repo/pull/3608"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.PostIssueErr = errFake

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first bug","body":"first body"}`,
			`{"title":"second bug","body":"second body"}`,
		},
	}

	s := newTestSettle(baseConfig(), fc.AsIssueFiler(), fc)
	stdout := captureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	if !strings.Contains(stdout, "    #3608  filed=ok:0,failed:2,skipped:0\n") {
		t.Errorf("stdout = %q, want it to contain the all-failed filed= tally", stdout)
	}
}

// A nil filed slice tallies to the zero value, not an error (issue #3608):
// a run that never reached filing must tally the same as one that filed
// zero intents.
func TestTallyFiled_Zero(t *testing.T) {
	got := tallyFiled(nil)
	if got != (filedTally{}) {
		t.Errorf("tallyFiled(nil) = %+v, want zero value", got)
	}
	if got.String() != "ok:0,failed:0,skipped:0" {
		t.Errorf("String() = %q, want %q", got.String(), "ok:0,failed:0,skipped:0")
	}
}

// All-success filings tally entirely into ok (issue #3608).
func TestTallyFiled_AllOK(t *testing.T) {
	filed := []filedIntent{
		{Title: "a", URL: "https://example/1"},
		{Title: "b", URL: "https://example/2"},
	}
	got := tallyFiled(filed)
	if got.String() != "ok:2,failed:0,skipped:0" {
		t.Errorf("String() = %q, want %q", got.String(), "ok:2,failed:0,skipped:0")
	}
}

// All-failed filings tally entirely into failed (issue #3608).
func TestTallyFiled_AllFailed(t *testing.T) {
	filed := []filedIntent{
		{Title: "a", Failed: true, Body: "body a"},
		{Title: "b", Failed: true, Body: "body b"},
	}
	got := tallyFiled(filed)
	if got.String() != "ok:0,failed:2,skipped:0" {
		t.Errorf("String() = %q, want %q", got.String(), "ok:0,failed:2,skipped:0")
	}
}

// A mix of successes and failures splits across both counters (issue #3608).
func TestTallyFiled_Mixed(t *testing.T) {
	filed := []filedIntent{
		{Title: "a", URL: "https://example/1"},
		{Title: "b", Failed: true, Body: "body b"},
		{Title: "c", URL: "https://example/3"},
	}
	got := tallyFiled(filed)
	if got.String() != "ok:2,failed:1,skipped:0" {
		t.Errorf("String() = %q, want %q", got.String(), "ok:2,failed:1,skipped:0")
	}
}

// reportFiled prints even at the zero tally, on purpose (issue #3608): a run
// that reached filing but filed nothing must read differently in the
// transcript than a run that never reached filing at all (no line printed).
func TestReportFiled_ZeroTallyStillPrints(t *testing.T) {
	stdout := captureStdout(t, func() {
		reportFiled("42", nil)
	})
	want := "    #42  filed=ok:0,failed:0,skipped:0\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// reportFiled's mixed-tally line matches the surrounding status-output
// convention (four leading spaces, "#<num>", two spaces, then the field; see
// research.go's landing/status/note line).
func TestReportFiled_MixedTally(t *testing.T) {
	filed := []filedIntent{
		{Title: "a", URL: "https://example/1"},
		{Title: "b", Failed: true, Body: "body b"},
	}
	stdout := captureStdout(t, func() {
		reportFiled("7", filed)
	})
	want := "    #7  filed=ok:1,failed:1,skipped:0\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// Two intents that share a dedup term but differ in title and body prose are
// the same finding site described two ways (issue #3609): the first files,
// the second is skipped rather than filed as a near-duplicate, the tally
// counts both, and the skip line names what it matched.
func TestFileIssueIntentsDetailed_Dedup_SameSiteDifferentProseWithinRun(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first phrasing of the bug","body":"body one","dedupTerms":["race in settle"]}`,
			`{"title":"second phrasing of the same bug","body":"body two","dedupTerms":["Race In Settle"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want exactly 1 (the second is a dedup skip)", fc.PostIssueCalls)
	}
	if got := tallyFiled(filed).String(); got != "ok:1,failed:0,skipped:1" {
		t.Errorf("tallyFiled = %q, want %q", got, "ok:1,failed:0,skipped:1")
	}
	if !strings.Contains(stdout, `skipped duplicate issue-intent: "second phrasing of the same bug"`) {
		t.Errorf("stdout = %q, want a skip line naming the second intent", stdout)
	}
}

// The dedup match reference is carried on the filedIntent itself, not just
// printed to stdout (issue #3811), so a verdict-comment renderer can name
// what an intra-run skip matched.
func TestFileIssueIntentsDetailed_Dedup_SkipCarriesDupRef(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first phrasing of the bug","body":"body one","dedupTerms":["race in settle"]}`,
			`{"title":"second phrasing of the same bug","body":"body two","dedupTerms":["Race In Settle"]}`,
		},
	}

	var filed []filedIntent
	captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(filed) != 2 {
		t.Fatalf("filed = %+v, want 2 entries", filed)
	}
	skip := filed[1]
	if !skip.Skipped {
		t.Fatalf("filed[1] = %+v, want Skipped", skip)
	}
	if !strings.Contains(skip.DupRef, "first phrasing of the bug") {
		t.Errorf("DupRef = %q, want it to name the intra-run match", skip.DupRef)
	}
}

// An open backlog issue carrying the dedup marker for a term is an exact
// retry of an already-filed finding, even under completely different prose
// (issue #3609): the intent is skipped and the skip line names the matched
// issue.
func TestFileIssueIntentsDetailed_Dedup_ExactRetryMatchesBacklogMarker(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier, differently worded finding",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: race in settle -->",
		Labels: []string{"agent-review-finding"},
	})

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"A brand new title for the retry","body":"new body","dedupTerms":["Race in Settle"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none (dedup match against the backlog)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || !filed[0].Skipped {
		t.Fatalf("filed = %+v, want one skipped entry", filed)
	}
	if !strings.Contains(stdout, "already tracked: #501") {
		t.Errorf("stdout = %q, want it to name #501", stdout)
	}
}

// Two genuinely distinct findings that happen to share a formulaic title
// (e.g. a conventional-commit prefix) but carry different -- or no -- dedup
// terms both file: the title is no longer a dedup key (issue #3609 review),
// so prose alone never merges unrelated findings, whether the match would be
// against the open backlog or, as here, within one run.
func TestFileIssueIntentsDetailed_Dedup_SharedTitleDifferentTermsBothFile(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "77",
		Title:  "test: cover the error path in the settle loop",
		Body:   "a pre-existing finding body with no marker at all",
		Labels: []string{"agent-research-finding"},
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"test: cover the error path in the settle loop","body":"new body","dedupTerms":["a different site"]}`,
			`{"title":"test: cover the error path in the settle loop","body":"another new body"}`,
		},
	}

	var filed []filedIntent
	captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-research-finding", "")
	})

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2 (title never keys dedup)", fc.PostIssueCalls)
	}
	if len(filed) != 2 || filed[0].Skipped || filed[1].Skipped {
		t.Fatalf("filed = %+v, want two non-skipped entries", filed)
	}
}

// Two genuinely distinct findings -- different dedup terms, different titles
// -- both file, and none is suppressed (issue #3609).
func TestFileIssueIntentsDetailed_Dedup_DistinctFindingsBothFile(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"first distinct bug","body":"body one","dedupTerms":["site one"]}`,
			`{"title":"second distinct bug","body":"body two","dedupTerms":["site two"]}`,
		},
	}

	var filed []filedIntent
	captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
	if got := tallyFiled(filed).String(); got != "ok:2,failed:0,skipped:0" {
		t.Errorf("tallyFiled = %q, want %q", got, "ok:2,failed:0,skipped:0")
	}
}

// A filed finding's body actually carries the hidden dedup marker line, and
// the terms recovered from it via parseDedupMarker match the intent's own
// DedupTerms, normalized (issue #3609).
func TestFileIssueIntentsDetailed_Dedup_FiledBodyCarriesMarkerAndRoundTrips(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding with terms","body":"the body","dedupTerms":["Term One","term   two"]}`,
		},
	}

	captureStdout(t, func() {
		fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	body := fc.PostIssueCalls[0].Body
	if !strings.Contains(body, "<!-- spindrift-dedup: ") {
		t.Fatalf("body = %q, want it to carry the dedup marker line", body)
	}
	got := parseDedupMarker(body)
	want := []string{"term one", "term two"}
	if len(got) != len(want) {
		t.Fatalf("parseDedupMarker(body) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseDedupMarker(body)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// An intent whose only dedup term is unusable (a Filer comma-joining two
// sites into one entry) files under an empty marker, and the run warns
// naming the dropped term -- silently swallowing it would leave the next
// run's re-file with nothing in the transcript explaining why (issue #3609
// review).
func TestFileIssueIntentsDetailed_Dedup_DroppedTermWarns(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding","body":"the body","dedupTerms":["a, b"]}`,
		},
	}

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
		})
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	body := fc.PostIssueCalls[0].Body
	if got := parseDedupMarker(body); len(got) != 0 {
		t.Errorf("parseDedupMarker(body) = %v, want no keys (only term was unusable)", got)
	}
	if !strings.Contains(stderr, "?? #1") || !strings.Contains(stderr, "a, b") {
		t.Errorf("stderr = %q, want a warning naming issue #1 and the dropped term %q", stderr, "a, b")
	}
}

// An intent with one good term and one unusable term keeps the good term as
// a dedup key (the marker still carries it) and still warns about the
// dropped one -- a partial degradation is still a degradation worth
// surfacing.
func TestFileIssueIntentsDetailed_Dedup_PartialDroppedTermKeepsGoodKeyAndWarns(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding","body":"the body","dedupTerms":["good term","a, b"]}`,
		},
	}

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
		})
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	body := fc.PostIssueCalls[0].Body
	if !strings.Contains(body, "good term") {
		t.Errorf("body = %q, want the good term kept in the marker", body)
	}
	if !strings.Contains(stderr, "?? #1") || !strings.Contains(stderr, "a, b") {
		t.Errorf("stderr = %q, want a warning naming issue #1 and the dropped term %q", stderr, "a, b")
	}
}

// An open issue that shares a title with a new intent but carries neither
// finding provenance label never suppresses filing (issue #3609): dedup only
// ever consults issues fileIssueIntentsDetailed itself filed.
func TestFileIssueIntentsDetailed_Dedup_NonFindingIssueNeverSuppresses(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "9",
		Title:  "Some Ordinary Backlog Title",
		Labels: []string{"bug", "ready-for-agent"},
		Body:   "<!-- spindrift-dedup: race in settle -->",
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	// The new intent's own dedup term matches issue #9's marker term
	// exactly -- if isFindingIssue's label check were ever bypassed, this
	// would be suppressed as a duplicate. Filing anyway proves the label
	// check, not an empty key set, is what let it through.
	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"Some Ordinary Backlog Title","body":"new body","dedupTerms":["race in settle"]}`,
		},
	}

	var filed []filedIntent
	captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1 (non-finding issue must not suppress)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || filed[0].Skipped {
		t.Fatalf("filed = %+v, want one non-skipped entry", filed)
	}
}

// combinedBacklogListerFilerStub composes a LabeledBacklogLister that always
// fails with a HostPostedIssueFiler-capable forge.Fake, so a test can drive
// fileIssueIntentsDetailed's full path through backlogDedupIndex's
// list-failure branch (dedup_test.go's own list-failure test calls
// backlogDedupIndex directly, which never reaches a filer).
type combinedBacklogListerFilerStub struct {
	forge.IssueTracker
	forge.HostPostedIssueFiler
	err   error
	calls int
}

func (s *combinedBacklogListerFilerStub) ListOpenIssuesWithLabels(labels []string) ([]forge.Issue, error) {
	s.calls++
	return nil, s.err
}

// A LabeledBacklogLister list failure degrades to intra-run-only dedup
// (empty backlog index) rather than blocking filing: the intent still files,
// and the warning fires (issue #3609 review).
func TestFileIssueIntentsDetailed_Dedup_ListFailureStillFiles(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
	filer := fc.AsIssueFiler()
	stub := &combinedBacklogListerFilerStub{
		IssueTracker:         filer,
		HostPostedIssueFiler: filer.(forge.HostPostedIssueFiler),
		err:                  errors.New("boom"),
	}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding","body":"the body","dedupTerms":["race in settle"]}`,
		},
	}

	var filed []filedIntent
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			filed = fileIssueIntentsDetailed(stub, "1", result, "agent-review-finding", "")
		})
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1 (list failure must not suppress filing)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || filed[0].Skipped {
		t.Fatalf("filed = %+v, want one non-skipped entry", filed)
	}
	if !strings.Contains(stderr, "?? #1") || !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want the list-failure warning", stderr)
	}
}

// A pass whose intents carry no dedup terms never builds the backlog index:
// the two list round trips could not match anything, so they are not paid
// (issue #3609 review).
func TestFileIssueIntentsDetailed_Dedup_NoTermsSkipsBacklogList(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
	filer := fc.AsIssueFiler()
	stub := &combinedBacklogListerFilerStub{
		IssueTracker:         filer,
		HostPostedIssueFiler: filer.(forge.HostPostedIssueFiler),
	}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding","body":"the body"}`,
			`{"title":"another finding","body":"more body"}`,
		},
	}

	captureStdout(t, func() {
		captureStderr(t, func() {
			fileIssueIntentsDetailed(stub, "1", result, "agent-review-finding", "")
		})
	})

	if stub.calls != 0 {
		t.Errorf("ListOpenIssuesWithLabels calls = %d, want 0 (no intent carries a dedup key)", stub.calls)
	}
	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
}

// The backlog index is built at most once even across several key-carrying
// intents -- laziness must not turn the hoist into a per-intent round trip.
func TestFileIssueIntentsDetailed_Dedup_BacklogListBuiltOnce(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
	filer := fc.AsIssueFiler()
	stub := &combinedBacklogListerFilerStub{
		IssueTracker:         filer,
		HostPostedIssueFiler: filer.(forge.HostPostedIssueFiler),
	}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding","body":"the body","dedupTerms":["term one"]}`,
			`{"title":"another finding","body":"more body","dedupTerms":["term two"]}`,
		},
	}

	captureStdout(t, func() {
		captureStderr(t, func() {
			fileIssueIntentsDetailed(stub, "1", result, "agent-review-finding", "")
		})
	})

	if stub.calls != 1 {
		t.Errorf("ListOpenIssuesWithLabels calls = %d, want 1", stub.calls)
	}
}

// An intent that carries no usable dedup key at all warns on stderr in the
// same style as its dropped-term sibling, so "filed without dedup" is
// observable host-side rather than only a prompt-compliance claim (issue
// #3609 review).
func TestFileIssueIntentsDetailed_Dedup_NoTermsWarns(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a term-less finding","body":"the body"}`,
		},
	}

	var filed []filedIntent
	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
		})
	})

	if len(filed) != 1 || filed[0].Failed || filed[0].Skipped {
		t.Fatalf("filed = %+v, want one filed entry (no dedup term is not a filing error)", filed)
	}
	if !strings.Contains(stderr, "?? #1") || !strings.Contains(stderr, "a term-less finding") {
		t.Errorf("stderr = %q, want a warning naming issue #1 and the intent title", stderr)
	}
}

// A finding whose body quotes a marker line in prose, filed by an intent
// with no usable dedup term of its own, indexes under zero keys: the
// launcher appends its own (empty) marker last, so last-wins parsing reads
// the launcher's, never the quote's (issue #3609 review).
func TestFileIssueIntentsDetailed_Dedup_QuotedMarkerNeverIndexed(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding about the marker","body":"the format is:\n\n<!-- spindrift-dedup: quoted, placeholder -->"}`,
		},
	}

	captureStdout(t, func() {
		captureStderr(t, func() {
			fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
		})
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	filedBody := fc.PostIssueCalls[0].Body

	backlog := forge.NewFake()
	backlog.SetIssue(forge.Issue{Number: "7", Labels: []string{findingLabelReview}, Body: filedBody})
	index := backlogDedupIndex(backlog, "100")

	if len(index) != 0 {
		t.Errorf("index = %v, want empty (the quoted marker must not become this issue's key set)", index)
	}
}
