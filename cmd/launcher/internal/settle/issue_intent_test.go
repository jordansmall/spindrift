package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/outcome"
)

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

	urls := fileIssueIntents(fc.AsIssueFiler(), "1", result, "agent-review-finding")

	if len(fc.PostIssueCalls) != 1 || fc.PostIssueCalls[0].Title != "good one" {
		t.Fatalf("PostIssueCalls = %+v, want exactly the well-formed intent", fc.PostIssueCalls)
	}
	if len(urls) != 1 {
		t.Errorf("urls = %v, want exactly 1", urls)
	}
}

// A tracker that doesn't implement forge.HostPostedIssueFiler (every real
// adapter today) leaves fileIssueIntents a no-op rather than panicking. The
// relay is a best-effort side channel, not part of the run's own landing
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

// Exercises the relay (issue #2018) against a real *local.LocalTracker rather
// than the fake: fileIssueIntents type-asserts its it parameter, and a real
// adapter is the only way to prove that assertion reaches a tracker that
// writes to disk.
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

	// Read back through the tracker's own read path: the labels on disk must
	// be the host-derived ones, never the payload's own "labels" field
	// (issue #1949).
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

// The provenance label comes from the caller, not from a label baked into the
// routine. "some-other-caller-label" is a deliberate placeholder, not ADR
// 0041's real "agent-research-finding" (which isn't registered in
// lib/labels.nix). A research-settle caller (issue #2590) needs this seam:
// fileIssueIntents is a package-level function, not a *Settle method.
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
	want := "first body" + "\n\n" + "Filed from research on #99"
	if fc.PostIssueCalls[0].Body != want {
		t.Errorf("PostIssueCalls[0].Body = %q, want %q", fc.PostIssueCalls[0].Body, want)
	}
}

// An empty bodyBacklink posts the intent's body byte-for-byte with no trailing
// separator, proving fileIssueIntents' wrapper behavior (it always passes "")
// survived the refactor.
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
