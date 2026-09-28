package settle

import (
	"encoding/json"
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
	"spindrift.dev/launcher/internal/signalwire"
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

// TestParseIssueIntent_ClassMustBeALowercaseSlug: a Class outside
// signalwire.ValidClass's shape is cleared rather than rejecting the whole
// intent (issue #3880 review finding) -- it is Box-supplied text
// interpolated unescaped into host-authored backlink/note strings, so
// anything but a plain slug is treated the same as no claimed class at all.
func TestParseIssueIntent_ClassMustBeALowercaseSlug(t *testing.T) {
	cases := []struct {
		name      string
		class     string
		wantClass string
	}{
		{"plain slug", "error-handling", "error-handling"},
		{"single char", "a", "a"},
		{"digits and hyphens", "a1-b2", "a1-b2"},
		{"empty stays empty", "", ""},
		{"uppercase rejected", "Error-Handling", ""},
		{"leading hyphen rejected", "-error", ""},
		{"embedded space rejected", "error handling", ""},
		{"embedded newline rejected", "error\nhandling", ""},
		{"backtick rejected", "error`handling", ""},
		{"markdown-ish rejected", "**bold**", ""},
		{"too long rejected", strings.Repeat("a", signalwire.MaxClassLen+1), ""},
		{"exactly max length kept", strings.Repeat("a", signalwire.MaxClassLen), strings.Repeat("a", signalwire.MaxClassLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// json.Marshal, not string concatenation, so a class containing a
			// character JSON must escape (a literal newline, a quote) still
			// produces well-formed input -- the test is about
			// signalwire.ValidClass's shape check, not JSON's own escaping
			// rules.
			payload, err := json.Marshal(map[string]string{"title": "t", "class": tc.class})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			in, ok := parseIssueIntent(string(payload))
			if !ok {
				t.Fatalf("parseIssueIntent(%q) ok = false, want true", payload)
			}
			if in.Class != tc.wantClass {
				t.Errorf("Class = %q, want %q", in.Class, tc.wantClass)
			}
		})
	}
}

// TestParseIssueIntent_ConcurrenceIsBoundedAndBacktickNeutralized: Concurrence
// is Box-supplied prose too, interpolated into the same host-authored
// promotion note as Class, so it gets the same treatment: capped length and
// no backticks that could open/close a markdown code span in that note.
func TestParseIssueIntent_ConcurrenceIsBoundedAndBacktickNeutralized(t *testing.T) {
	// family and eCombining are each a single grapheme cluster made of
	// multiple runes -- a ZWJ emoji sequence and a base rune plus a
	// combining mark, respectively. The "as last kept grapheme stays whole"
	// cases place one straddling the maxConcurrenceLen boundary: a
	// rune-boundary cut would split it, corrupting the last visible
	// character, while a grapheme-boundary cut keeps it whole. The "past
	// limit dropped whole" cases pin that nothing of a cluster past the
	// limit leaks into the result, whichever cut made the call.
	const family = "👩‍👩‍👧‍👦"
	const eCombining = "é"

	cases := []struct {
		name        string
		concurrence string
		want        string
	}{
		{"plain text kept", "looks good", "looks good"},
		{"backtick neutralized", "agreed, `rm -rf /` is safe", "agreed, 'rm -rf /' is safe"},
		{"exactly max length kept", strings.Repeat("a", maxConcurrenceLen), strings.Repeat("a", maxConcurrenceLen)},
		{"too long truncated to max graphemes", strings.Repeat("a", maxConcurrenceLen+50), strings.Repeat("a", maxConcurrenceLen)},
		{
			"emoji ZWJ sequence as last kept grapheme stays whole",
			strings.Repeat("a", maxConcurrenceLen-1) + family,
			strings.Repeat("a", maxConcurrenceLen-1) + family,
		},
		{
			"emoji ZWJ sequence past limit dropped whole",
			strings.Repeat("a", maxConcurrenceLen) + family,
			strings.Repeat("a", maxConcurrenceLen),
		},
		{
			"combining mark sequence as last kept grapheme stays whole",
			strings.Repeat("a", maxConcurrenceLen-1) + eCombining,
			strings.Repeat("a", maxConcurrenceLen-1) + eCombining,
		},
		{
			"combining mark sequence past limit dropped whole",
			strings.Repeat("a", maxConcurrenceLen) + eCombining,
			strings.Repeat("a", maxConcurrenceLen),
		},
		{
			"single grapheme cluster over byte cap dropped whole, fails closed to empty",
			"a" + strings.Repeat("\u0301", 100000),
			"",
		},
		{
			"oversized cluster after short clusters truncates to the last kept boundary",
			"ok " + "a" + strings.Repeat("\u0301", 100000),
			"ok ",
		},
		{
			"byte cap reached before grapheme cap, multibyte clusters",
			strings.Repeat(family, 100),
			strings.Repeat(family, maxConcurrenceBytes/len(family)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"title": "t", "concurrence": tc.concurrence})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			in, ok := parseIssueIntent(string(payload))
			if !ok {
				t.Fatalf("parseIssueIntent(%q) ok = false, want true", payload)
			}
			if in.Concurrence != tc.want {
				t.Errorf("Concurrence = %q, want %q", in.Concurrence, tc.want)
			}
			// promotionNote re-sanitizes an already-parsed value, so a
			// second pass must change nothing.
			if again := sanitizeConcurrence(in.Concurrence); again != in.Concurrence {
				t.Errorf("sanitizeConcurrence not idempotent: %q -> %q", in.Concurrence, again)
			}
		})
	}
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

// lineContaining returns the single line of captured output containing
// substr, failing the test if none or more than one line matches -- so an
// assertion can pin a claim (e.g. a key and a reference) to the one line
// that's supposed to carry it, not the whole capture.
func lineContaining(t *testing.T, output, substr string) string {
	t.Helper()
	var matches []string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, substr) {
			matches = append(matches, line)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("lineContaining(%q) matched %d lines, want 1: %v", substr, len(matches), matches)
	}
	return matches[0]
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
	line := lineContaining(t, stdout, "skipped duplicate issue-intent")
	if !strings.Contains(line, "already tracked: #501") {
		t.Errorf("skip line = %q, want it to name #501", line)
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

// A two-site intent must still file when only one of its sites is already
// tracked -- the other site would otherwise never be tracked at all (issue
// #3808). The filed body's marker carries both terms, since the finding
// itself is still the full two-site one.
func TestFileIssueIntentsDetailed_Dedup_PartialOverlapStillFiles(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding covering only site a",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site a -->",
		Labels: []string{"agent-review-finding"},
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding spanning two sites","body":"new body","dedupTerms":["site a","site b"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want exactly 1 (partial overlap must still file)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || filed[0].Skipped {
		t.Fatalf("filed = %+v, want one non-skipped entry", filed)
	}
	body := fc.PostIssueCalls[0].Body
	if !strings.Contains(body, "site a") || !strings.Contains(body, "site b") {
		t.Errorf("filed body = %q, want the marker to carry both site a and site b", body)
	}
	line := lineContaining(t, stdout, "partial dedup overlap")
	if !strings.Contains(line, `#1  filing issue-intent "a finding spanning two sites" despite partial dedup overlap`) ||
		!strings.Contains(line, "site a") || !strings.Contains(line, "#501") {
		t.Errorf("partial-overlap line = %q, want it naming site a and #501", line)
	}
}

// The covered sites of a partial overlap can be tracked by two different
// backlog issues, and the line then names every one of them -- with the
// covered keys bracketed, so the two lists it joins stay apart (issue #3808).
func TestFileIssueIntentsDetailed_Dedup_PartialOverlapNamesEveryCoveringIssue(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding covering site a",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site a -->",
		Labels: []string{"agent-review-finding"},
	})
	fc.SetIssue(forge.Issue{
		Number: "502",
		Title:  "An earlier finding covering site b",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site b -->",
		Labels: []string{"agent-review-finding"},
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding spanning three sites","body":"new body","dedupTerms":["site a","site b","site c"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want exactly 1 (site c is tracked nowhere)", fc.PostIssueCalls)
	}
	if got := tallyFiled(filed).String(); got != "ok:1,failed:0,skipped:0" {
		t.Errorf("tallyFiled = %q, want %q", got, "ok:1,failed:0,skipped:0")
	}
	line := lineContaining(t, stdout, "partial dedup overlap")
	if !strings.Contains(line, "already tracked: [site a, site b] via #501, #502") {
		t.Errorf("partial-overlap line = %q, want it naming both covered sites and both covering issues", line)
	}
}

// A fully-disjoint intent -- no key already tracked -- prints no
// partial-overlap line: that line is reserved for the case where the run
// really is filing on top of an already-covered site (issue #3808).
func TestFileIssueIntentsDetailed_Dedup_DisjointIntentPrintsNoPartialOverlapLine(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding covering an unrelated site",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site z -->",
		Labels: []string{"agent-review-finding"},
	})
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a fully distinct finding","body":"new body","dedupTerms":["site a","site b"]}`,
		},
	}

	stdout := captureStdout(t, func() {
		fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if strings.Contains(stdout, "partial dedup overlap") {
		t.Errorf("stdout = %q, want no partial-overlap line for a fully-disjoint intent", stdout)
	}
}

// A two-site intent whose both sites are already tracked -- even by two
// different backlog issues -- is skipped, since every site the finding
// spans is already covered (issue #3808).
func TestFileIssueIntentsDetailed_Dedup_FullOverlapAcrossAllSitesSkips(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding covering site a",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site a -->",
		Labels: []string{"agent-review-finding"},
	})
	fc.SetIssue(forge.Issue{
		Number: "502",
		Title:  "An earlier finding covering site b",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site b -->",
		Labels: []string{"agent-review-finding"},
	})

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding spanning two sites","body":"new body","dedupTerms":["site a","site b"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none (both sites already tracked)", fc.PostIssueCalls)
	}
	if got := tallyFiled(filed).String(); got != "ok:0,failed:0,skipped:1" {
		t.Errorf("tallyFiled = %q, want %q", got, "ok:0,failed:0,skipped:1")
	}
	line := lineContaining(t, stdout, "skipped duplicate issue-intent")
	if !strings.Contains(line, "already tracked: #501, #502") {
		t.Errorf("skip line = %q, want it to name both #501 and #502", line)
	}
}

// The same full skip when a single backlog issue's marker carries both of
// the finding's sites: coverage is what decides the skip, not how many
// issues supply it, and the skip line then names that one issue once.
func TestFileIssueIntentsDetailed_Dedup_FullOverlapBySingleIssueSkips(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "501",
		Title:  "An earlier finding covering both sites",
		Body:   "some earlier body\n\n<!-- spindrift-dedup: site a, site b -->",
		Labels: []string{"agent-review-finding"},
	})

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding spanning two sites","body":"new body","dedupTerms":["site a","site b"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none (both sites already tracked)", fc.PostIssueCalls)
	}
	if got := tallyFiled(filed).String(); got != "ok:0,failed:0,skipped:1" {
		t.Errorf("tallyFiled = %q, want %q", got, "ok:0,failed:0,skipped:1")
	}
	line := lineContaining(t, stdout, "skipped duplicate issue-intent")
	if !strings.Contains(line, "already tracked: #501") {
		t.Errorf("skip line = %q, want it to name #501", line)
	}
}

// A finding whose site is already tracked by a CLOSED finding issue is
// skipped exactly like an open match, naming the closed issue as the DupRef
// -- a closed finding was already filed once and must never be refiled just
// because it was since closed (issue #3873).
func TestFileIssueIntentsDetailed_Dedup_ClosedFullOverlapSkips(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
	filer := fc.AsIssueFiler()
	stub := &combinedBacklogListerFilerStub{
		IssueTracker:         filer,
		HostPostedIssueFiler: filer.(forge.HostPostedIssueFiler),
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{
					Number: "501",
					Title:  "A closed earlier finding covering site a",
					Body:   "some earlier body\n\n<!-- spindrift-dedup: site a -->",
					Labels: []string{"agent-review-finding"},
				},
			},
		},
	}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding on site a","body":"new body","dedupTerms":["site a"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(stub, "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 0 {
		t.Fatalf("PostIssueCalls = %+v, want none (site already tracked by a closed finding)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || !filed[0].Skipped || filed[0].DupRef != "#501" {
		t.Fatalf("filed = %+v, want one skipped entry with DupRef #501", filed)
	}
	line := lineContaining(t, stdout, "skipped duplicate issue-intent")
	if !strings.Contains(line, "already tracked: #501") {
		t.Errorf("skip line = %q, want it to name #501", line)
	}
}

// A two-site finding where only one site is covered by a closed finding
// still files -- same partial-overlap behaviour as an open match, since the
// uncovered site is tracked nowhere else (issue #3873).
func TestFileIssueIntentsDetailed_Dedup_ClosedPartialOverlapStillFiles(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/900"
	filer := fc.AsIssueFiler()
	stub := &combinedBacklogListerFilerStub{
		IssueTracker:         filer,
		HostPostedIssueFiler: filer.(forge.HostPostedIssueFiler),
		issues: map[forge.IssueState][]forge.Issue{
			forge.IssueClosed: {
				{
					Number: "501",
					Title:  "A closed earlier finding covering site a",
					Body:   "some earlier body\n\n<!-- spindrift-dedup: site a -->",
					Labels: []string{"agent-review-finding"},
				},
			},
		},
	}

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"a finding spanning two sites","body":"new body","dedupTerms":["site a","site b"]}`,
		},
	}

	var filed []filedIntent
	stdout := captureStdout(t, func() {
		filed = fileIssueIntentsDetailed(stub, "1", result, "agent-review-finding", "")
	})

	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1 (uncovered site b must still file)", fc.PostIssueCalls)
	}
	if len(filed) != 1 || filed[0].Skipped {
		t.Fatalf("filed = %+v, want one non-skipped entry", filed)
	}
	if !strings.Contains(stdout, "partial dedup overlap") || !strings.Contains(stdout, "#501") {
		t.Errorf("stdout = %q, want a partial-overlap line naming #501", stdout)
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

// combinedBacklogListerFilerStub composes a scriptable LabeledBacklogLister
// with a HostPostedIssueFiler-capable forge.Fake, so a test can drive
// fileIssueIntentsDetailed's full path through backlogDedupIndex: err (when
// set) fails both the closed and open lookups alike, for the list-failure
// branch (dedup_test.go's own list-failure test calls backlogDedupIndex
// directly, which never reaches a filer); issues, keyed by state, scripts a
// per-state result for the closed-match tests (issue #3873).
type combinedBacklogListerFilerStub struct {
	forge.IssueTracker
	forge.HostPostedIssueFiler
	err    error
	issues map[forge.IssueState][]forge.Issue
	calls  int
}

func (s *combinedBacklogListerFilerStub) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.issues[state], nil
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
// the two list round trips (closed and open) could not match anything, so
// they are not paid (issue #3609 review).
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
		t.Errorf("ListIssuesWithLabels calls = %d, want 0 (no intent carries a dedup key)", stub.calls)
	}
	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
}

// The backlog index is built at most once even across several key-carrying
// intents -- laziness must not turn the hoist into a per-intent round trip.
// Two ListIssuesWithLabels calls (closed, then open) is that one build, not
// two (issue #3873).
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

	if stub.calls != 2 {
		t.Errorf("ListIssuesWithLabels calls = %d, want 2 (one closed, one open, for the single build)", stub.calls)
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
