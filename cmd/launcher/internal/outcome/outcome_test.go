package outcome_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// --- Parse tests ---

func TestParse_WellFormed(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Issue != "127" {
		t.Errorf("Issue: got %q, want %q", o.Issue, "127")
	}
	if o.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing: got %q, want %q", o.Landing, "https://github.com/o/r/pull/1")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
	if o.Note != "all good" {
		t.Errorf("Note: got %q, want %q", o.Note, "all good")
	}
}

func TestParse_NoteWithEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=key=value"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "key=value" {
		t.Errorf("Note: got %q, want %q", o.Note, "key=value")
	}
}

func TestParse_NoteWithSpacesAndEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stalled on feat=2"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "stalled on feat=2" {
		t.Errorf("Note: got %q, want %q", o.Note, "stalled on feat=2")
	}
}

// TestParse_NoteExcludesTrailingNonceField guards against the nonce
// (issue #1939) leaking into Note: Note gets posted as a public comment on
// status=blocked (settle.postBlockedNoteComment), and the Dispatch reuses
// one nonce across every retry, so a leaked nonce lets a comment author
// replay it against a later attempt of the same Dispatch.
func TestParse_NoteExcludesTrailingNonceField(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stalled on it nonce=abc123"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "stalled on it" {
		t.Errorf("Note: got %q, want %q (nonce field leaked into Note)", o.Note, "stalled on it")
	}
}

// TestParse_NoteLiterallyContainingNonceWordIsPreserved documents the
// deliberately narrow heuristic: a trailing "nonce=<value>" is only treated
// as the field, not as note text, when <value> has no spaces — exactly the
// shape a genuine single-token nonce always has. Free note text that
// happens to contain "nonce=" followed by more words is left alone.
func TestParse_NoteLiterallyContainingNonceWordIsPreserved(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=rejected for a bad nonce=abc123 in the request"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "rejected for a bad nonce=abc123 in the request"
	if o.Note != want {
		t.Errorf("Note: got %q, want %q", o.Note, want)
	}
}

func TestParse_ColonDelimited(t *testing.T) {
	line := "SPINDRIFT_OUTCOME: issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := outcome.Outcome{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}
}

func TestParse_SurroundingWhitespace(t *testing.T) {
	line := "  SPINDRIFT_OUTCOME issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good  \n"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := outcome.Outcome{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}
}

func TestParse_MissingLanding(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing landing, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestParse_EmptyStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status= note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for empty status, got nil")
	}
}

func TestParse_MissingStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing status, got nil")
	}
}

// TestReadyBeforeNote pins ReadyBeforeNote to the exact substring test the
// deleted bash note-field split (agent/entrypoint.sh, commit a43506a8) used:
// bounded at the first " note=", literal "status=ready" token match, and
// tolerant of stripToken's space/colon delimiter handling -- deliberately
// looser than Parse's full grammar (e.g. an empty landing field still
// counts as ready here) and deliberately narrower on the note field (a
// "status=ready" mention inside note text must not count).
func TestReadyBeforeNote(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "empty landing still nudges",
			line: "SPINDRIFT_OUTCOME issue=7 landing= status=ready note=done",
			want: true,
		},
		{
			name: "note-embedded status=ready mention does not count",
			line: "SPINDRIFT_OUTCOME issue=7 landing=x note=I set status=ready earlier",
			want: false,
		},
		{
			name: "normal valid ready line",
			line: "SPINDRIFT_OUTCOME issue=7 landing=y status=ready note=fine",
			want: true,
		},
		{
			name: "non-ready status",
			line: "SPINDRIFT_OUTCOME issue=7 landing=y status=blocked note=fine",
			want: false,
		},
		{
			name: "no SPINDRIFT_OUTCOME token at all",
			line: "just some prose about status=ready",
			want: false,
		},
		{
			name: "colon-delimited token form",
			line: "SPINDRIFT_OUTCOME: status=ready",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outcome.ReadyBeforeNote(tt.line)
			if got != tt.want {
				t.Errorf("ReadyBeforeNote(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParse_WrongPrefix(t *testing.T) {
	line := "OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for wrong prefix, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error, got near-miss: %v", err)
	}
}

// --- ParseAnywhere tests ---

func TestParseAnywhere_TokenMidLineStillParses(t *testing.T) {
	line := "[implementor] SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"
	o, ok := outcome.ParseAnywhere(line)
	if !ok {
		t.Fatal("ParseAnywhere: got ok=false, want true")
	}
	if o.Issue != "7" || o.Status != "ready" {
		t.Errorf("ParseAnywhere = %+v, want issue=7 status=ready", o)
	}
}

func TestParseAnywhere_MarkdownWrappedTokenStillParses(t *testing.T) {
	line := "[implementor] `SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc`"
	o, ok := outcome.ParseAnywhere(line)
	if !ok {
		t.Fatal("ParseAnywhere: got ok=false, want true")
	}
	if o.Issue != "7" || o.Status != "ready" {
		t.Errorf("ParseAnywhere = %+v, want issue=7 status=ready", o)
	}
}

func TestParseAnywhere_NoTokenReturnsNotFound(t *testing.T) {
	if _, ok := outcome.ParseAnywhere("[implementor] Investigating the failing test."); ok {
		t.Error("ParseAnywhere: got ok=true, want false (no token present)")
	}
}

func TestParseAnywhere_TokenPresentButUnparsableReturnsNotFound(t *testing.T) {
	if _, ok := outcome.ParseAnywhere("[implementor] SPINDRIFT_OUTCOME issue=7 (missing landing/status)"); ok {
		t.Error("ParseAnywhere: got ok=true, want false (missing required fields)")
	}
}

func TestParse_EmptyLine(t *testing.T) {
	_, err := outcome.Parse("")
	if err == nil {
		t.Fatal("expected error for empty line, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error, got near-miss: %v", err)
	}
}

func TestParse_LongerIdentifierIsNotNearMiss(t *testing.T) {
	line := "SPINDRIFT_OUTCOMES issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a differently-named token, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error (not our token), got near-miss: %v", err)
	}
}

func TestParse_TokenAsInfixOfLongerIdentifierIsNotNearMiss(t *testing.T) {
	line := "MY_SPINDRIFT_OUTCOME_THING issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a differently-named identifier, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error (not our token), got near-miss: %v", err)
	}
}

func TestParse_TokenMatchContinuesPastFalseHit(t *testing.T) {
	line := "MY_SPINDRIFT_OUTCOME_THING then SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a mid-sentence line, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error (the genuine token appears later), got %v", err)
	}
}

func TestParse_TokenEmbeddedMidSentence(t *testing.T) {
	line := "the box printed SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok in its log"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for mid-sentence token, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

// --- Line / round-trip tests ---

var roundTripCases = []outcome.Outcome{
	{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"},
	{Issue: "1", Landing: "https://github.com/o/r/pull/99", Status: "blocked", Note: "stalled"},
	{Issue: "42", Landing: "https://github.com/o/r/pull/5", Status: "ready", Note: "key=value"},
	{Issue: "7", Landing: "https://github.com/o/r/pull/7", Status: "blocked", Note: "stalled on feat=2"},
	{Issue: "3", Landing: "https://github.com/o/r/pull/3", Status: "merged", Note: ""},
	{Issue: "9", Landing: "agent/issue-9", Status: "blocked", Note: "driver exited without emitting an outcome", Synthetic: true},
}

func TestLine_RoundTrip(t *testing.T) {
	for _, want := range roundTripCases {
		got, err := outcome.Parse(want.Line())
		if err != nil {
			t.Errorf("Parse(Line(%+v)) error: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", want, got)
		}
	}
}

// TestParse_Synthetic verifies a line carrying the synthetic=true field
// (issue #2223) parses with Synthetic==true and that the field does not
// bleed into Note, which follows it in the grammar.
func TestParse_Synthetic(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=some note here"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !o.Synthetic {
		t.Error("Synthetic: got false, want true")
	}
	if o.Status != "blocked" {
		t.Errorf("Status: got %q, want %q", o.Status, "blocked")
	}
	if o.Note != "some note here" {
		t.Errorf("Note: got %q, want %q (synthetic field leaked into Note)", o.Note, "some note here")
	}
}

// TestParse_NotSynthetic verifies a normal full-grammar line with no
// synthetic field parses with Synthetic==false.
func TestParse_NotSynthetic(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Synthetic {
		t.Error("Synthetic: got true, want false")
	}
}

// TestLine_NonSyntheticByteIdentical is a regression guard (issue #2223):
// a non-synthetic Outcome's rendered line must stay byte-identical to the
// pre-Synthetic-field wire format.
func TestLine_NonSyntheticByteIdentical(t *testing.T) {
	o := outcome.Outcome{Issue: "1", Landing: "x", Status: "ready", Note: "ok"}
	want := "SPINDRIFT_OUTCOME issue=1 landing=x status=ready note=ok"
	if got := o.Line(); got != want {
		t.Errorf("Line(): got %q, want %q", got, want)
	}
}

// lastInLog and lastSelfReportInLog scanner-mechanics tests (near-miss
// propagation, the skipped-flag nuances, oversized-line handling,
// synthetic-line exclusion from a self-report) live in
// outcome_internal_test.go (package outcome), since the scanners themselves
// are unexported (issue #2260). This file's Resolve tests below cover the
// policy-level behavior (which tier wins, and Resolve's own Skipped field).

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func writeBigLog(t *testing.T, preLines []string, bigLineSize int, postLines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range preLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	// Write oversized line
	big := make([]byte, bigLineSize)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := f.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	for _, l := range postLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// --- LastPRIntentInLog tests ---

// encodePRIntent base64-encodes title and body the same way a read-only
// Box's prompt fragment instructs it to: title, a blank line, then body.
func encodePRIntent(t *testing.T, title, body string) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(title + "\n\n" + body))
}

func TestLastPRIntentInLog_Found(t *testing.T) {
	payload := encodePRIntent(t, "feat: add widget", "Adds a widget. Closes #42")
	path := writeLog(t,
		"some output",
		"SPINDRIFT_PR_INTENT the-nonce "+payload,
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "feat: add widget\n\nAdds a widget. Closes #42"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

// TestLastPRIntentInLog_PreservesShellAndMarkdownMetacharacters verifies a
// body containing shell/markdown metacharacters ($(), backticks, quotes)
// decodes back byte-for-byte: base64 is transparent to them, but since
// CreateDraftPR passes title/body as argv (never through a shell), this
// grammar must hand them through intact rather than escaping or stripping
// anything.
func TestLastPRIntentInLog_PreservesShellAndMarkdownMetacharacters(t *testing.T) {
	body := "Runs `rm -rf /tmp/x`; also handles $(whoami) and \"quoted\" text."
	payload := encodePRIntent(t, "feat: widget", body)
	path := writeLog(t, "SPINDRIFT_PR_INTENT the-nonce "+payload)
	got, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "feat: widget\n\n" + body
	if got != want {
		t.Errorf("body: got %q, want %q", got, want)
	}
}

func TestLastPRIntentInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no pr-intent line here")
	_, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastPRIntentInLog_TakesLast(t *testing.T) {
	stale := encodePRIntent(t, "stale title", "stale body")
	final := encodePRIntent(t, "final title", "final body")
	path := writeLog(t,
		"SPINDRIFT_PR_INTENT the-nonce "+stale,
		"some more output",
		"SPINDRIFT_PR_INTENT the-nonce "+final,
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "final title\n\nfinal body"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

// TestLastPRIntentInLog_ValidLineNotShadowedByLaterRejectedMention verifies
// that a genuine, verified PR-intent line is not suppressed by a later line
// that merely carries the token without verifying — an untrusted
// issue/comment author's echoed or reasoning-adjacent mention, or a
// stale/wrong-nonce line from a different run — mirroring
// LastCommentLineInLog's same guarantee (unlike LastInLog's unconditional
// last-line-wins, which SPINDRIFT_OUTCOME isn't nonce-gated against yet).
func TestLastPRIntentInLog_ValidLineNotShadowedByLaterRejectedMention(t *testing.T) {
	genuine := encodePRIntent(t, "real title", "real body")
	path := writeLog(t,
		"SPINDRIFT_PR_INTENT the-nonce "+genuine,
		"SPINDRIFT_PR_INTENT wrong-nonce not-valid-base64!!!",
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true — the genuine line must not be shadowed")
	}
	want := "real title\n\nreal body"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

// TestLastPRIntentInLog_ValidLineNotShadowedByEarlierRejectedMention
// verifies the same non-shadowing guarantee in the opposite order: a
// rejected line preceding the genuine one must not prevent it from being
// found either — the scan doesn't short-circuit or otherwise treat an
// earlier rejection as disqualifying a later, distinct verified line.
func TestLastPRIntentInLog_ValidLineNotShadowedByEarlierRejectedMention(t *testing.T) {
	genuine := encodePRIntent(t, "real title", "real body")
	path := writeLog(t,
		"SPINDRIFT_PR_INTENT wrong-nonce not-valid-base64!!!",
		"SPINDRIFT_PR_INTENT the-nonce "+genuine,
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true — the genuine line must still be found")
	}
	want := "real title\n\nreal body"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

// TestLastPRIntentInLog_NonceMismatchIgnoredAndWarned verifies that a
// candidate line whose nonce doesn't match this run's own nonce — the shape
// an untrusted issue/comment author's echoed text would take, since they
// wrote it before the nonce was minted — is never used to open a PR, and
// surfaces as an error the caller can warn on rather than silently vanishing.
func TestLastPRIntentInLog_NonceMismatchIgnoredAndWarned(t *testing.T) {
	payload := encodePRIntent(t, "spoofed title", "spoofed body")
	path := writeLog(t, "SPINDRIFT_PR_INTENT wrong-nonce "+payload)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a nonce mismatch")
	}
	if body != "" {
		t.Errorf("body: got %q, want empty", body)
	}
	if err == nil {
		t.Fatal("expected a warnable error for a nonce mismatch, got nil")
	}
}

// TestLastPRIntentInLog_EmptyExpectedNonceNeverMatches mirrors
// LastCommentLineInLog's own invariant: an empty expectedNonce (the zero
// value a caller might pass by mistake) must never verify a line, even one
// that happens to carry no real nonce field at all. The fixture line has
// only one field after the token, so under issue #2089 it also looks like a
// one-field doc example rather than a genuine signal attempt: it must stay
// silently non-matched (err == nil) rather than warn, while still preserving
// the empty-nonce-never-verifies invariant via found == false.
func TestLastPRIntentInLog_EmptyExpectedNonceNeverMatches(t *testing.T) {
	payload := encodePRIntent(t, "title", "body")
	path := writeLog(t, "SPINDRIFT_PR_INTENT "+payload)
	_, found, _, err := outcome.LastPRIntentInLog(path, "")
	if found {
		t.Fatal("expected found=false for an empty expectedNonce")
	}
	if err != nil {
		t.Errorf("unexpected error for a one-field non-attempt line: %v", err)
	}
}

// TestLastPRIntentInLog_StrictDecodeRejectsMalformedPayload verifies the
// pinned strict decoder rejects a malformed payload outright rather than
// best-effort- or whitespace-stripped-decoding it.
func TestLastPRIntentInLog_StrictDecodeRejectsMalformedPayload(t *testing.T) {
	path := writeLog(t, "SPINDRIFT_PR_INTENT the-nonce not-valid-base64!!!")
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a malformed payload")
	}
	if body != "" {
		t.Errorf("body: got %q, want empty", body)
	}
	if err == nil {
		t.Fatal("expected a decode error, got nil")
	}
}

// TestLastPRIntentInLog_BareProseMentionDoesNotWarn verifies that a line
// merely naming the token in prose — not leading with it — is not treated as
// a signal attempt at all, so it neither verifies nor warns (issue #2089).
func TestLastPRIntentInLog_BareProseMentionDoesNotWarn(t *testing.T) {
	path := writeLog(t, "the SPINDRIFT_PR_INTENT line, please relay it")
	_, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a bare prose mention")
	}
	if err != nil {
		t.Errorf("unexpected error for a bare prose mention: %v", err)
	}
}

// TestLastPRIntentInLog_DocExampleDoesNotWarn verifies that a line leading
// with the token but carrying only one field after it (a doc example missing
// the base64 payload field) is not treated as a signal attempt, so it neither
// verifies nor warns (issue #2089).
func TestLastPRIntentInLog_DocExampleDoesNotWarn(t *testing.T) {
	path := writeLog(t, "SPINDRIFT_PR_INTENT deadbeefcafe1234")
	_, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a one-field doc example")
	}
	if err != nil {
		t.Errorf("unexpected error for a one-field doc example: %v", err)
	}
}

// TestLastPRIntentInLog_SurvivesStreamJSONCollapse verifies the line is
// found inside a real Claude Code stream-json JSONL log shape, where a
// multi-line block would collapse onto one JSON-escaped physical line and
// never match an exact-line marker scan (issue #1921's dogfood failure,
// the reason this signal moved to a single line at all).
func TestLastPRIntentInLog_SurvivesStreamJSONCollapse(t *testing.T) {
	payload := encodePRIntent(t, "feat: add widget", "Adds a widget. Closes #42")
	path := writeLog(t,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"some reasoning\n"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"SPINDRIFT_PR_INTENT the-nonce `+payload+`\n"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=ok\n"}]}}`,
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "feat: add widget\n\nAdds a widget. Closes #42"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

// TestLastPRIntentInLog_MarkerAfterNarrationInSameTextField verifies the
// marker is still found when the Box's own preceding narration shares the
// same assistant-message text field, separated only by a real newline —
// which stream-json JSON-encodes as a literal backslash-n, landing the 'n'
// of that escape directly against the token's first letter with no real
// whitespace between them. This is the same physical-line-collapse class
// issue #1921 is about, just for a token that isn't already at the very
// start of its JSON text field (the placement TestLastPRIntentInLog_
// SurvivesStreamJSONCollapse's fixture uses, which doesn't exercise this
// boundary).
func TestLastPRIntentInLog_MarkerAfterNarrationInSameTextField(t *testing.T) {
	payload := encodePRIntent(t, "feat: add widget", "Adds a widget. Closes #42")
	path := writeLog(t,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Wrapping up now.\nSPINDRIFT_PR_INTENT the-nonce `+payload+`\n"}]}}`,
	)
	body, found, _, err := outcome.LastPRIntentInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "feat: add widget\n\nAdds a widget. Closes #42"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

func TestLastPRIntentInLog_FileNotFound(t *testing.T) {
	_, found, _, err := outcome.LastPRIntentInLog("/nonexistent/path/test.log", "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

// --- LastFieldedOutcomeLine / LastNearMissOutcomeLine tests ---
//
// These pin the deleted bash extractor's filter-then-last semantics
// (outcomeExtractFnBody/outcomeExtractNearMissFnBody, git show
// a2addd2b:lib/drivers/claude.nix -- see markergate's ShouldNudgeOutcome
// doc comment): a SPINDRIFT_OUTCOME-token-leading line is "fielded" purely
// by carrying both
// a landing= and a status= field marker, any value included -- never by
// Parse's full grammar validity -- and the LAST such fielded line wins over
// any later non-fielded token-leading line.

func TestLastFieldedOutcomeLine_NoFile(t *testing.T) {
	line, found, err := outcome.LastFieldedOutcomeLine("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing file, got line %q", line)
	}
}

func TestLastNearMissOutcomeLine_NoFile(t *testing.T) {
	line, found, err := outcome.LastNearMissOutcomeLine("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing file, got line %q", line)
	}
}

// TestLastFieldedOutcomeLine_EmptyLandingStillFielded pins bug 1: the
// deleted bash's fielded filter only checks for the literal "landing="
// field marker, any value included, so an empty landing value still counts
// as fielded -- unlike outcome.Parse, which rejects an empty landing as
// ErrNearMiss.
func TestLastFieldedOutcomeLine_EmptyLandingStillFielded(t *testing.T) {
	want := outcome.Token + " issue=7 landing= status=ready note=done"
	logPath := writeLog(t, want)
	got, found, err := outcome.LastFieldedOutcomeLine(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for a fielded line with an empty landing value")
	}
	if got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// TestLastFieldedOutcomeLine_FieldedLineWinsOverLaterNonFieldedLine pins bug
// 2: the deleted bash filtered to fielded lines FIRST, then took the last of
// those -- a later token-leading line that carries neither field marker
// (e.g. a bare "SPINDRIFT_OUTCOME: all set" paraphrase) must never shadow an
// earlier genuine fielded line.
func TestLastFieldedOutcomeLine_FieldedLineWinsOverLaterNonFieldedLine(t *testing.T) {
	fielded := outcome.Token + " issue=7 landing=agent/issue-7 status=ready note=done"
	logPath := writeLog(t, fielded, outcome.Token+": all set")
	got, found, err := outcome.LastFieldedOutcomeLine(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got != fielded {
		t.Fatalf("line = %q, want the earlier fielded line %q", got, fielded)
	}
}

// TestLastNearMissOutcomeLine_PicksUpNonFieldedLine covers the near-miss
// complement: when no fielded line exists at all, the last token-leading
// line that fails the fielded test is the near-miss candidate.
func TestLastNearMissOutcomeLine_PicksUpNonFieldedLine(t *testing.T) {
	nearMiss := outcome.Token + ": all set"
	logPath := writeLog(t, nearMiss)
	got, found, err := outcome.LastNearMissOutcomeLine(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got != nearMiss {
		t.Fatalf("line = %q, want %q", got, nearMiss)
	}
}

// --- LineHasNonce tests ---

func TestLineHasNonce_Match(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=abc123"
	if !outcome.LineHasNonce(line, "abc123") {
		t.Error("expected line to carry the nonce")
	}
}

func TestLineHasNonce_Missing(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	if outcome.LineHasNonce(line, "abc123") {
		t.Error("expected line without any nonce mention to not match")
	}
}

func TestLineHasNonce_Mismatched(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=deadbeef"
	if outcome.LineHasNonce(line, "abc123") {
		t.Error("expected line carrying a different nonce to not match")
	}
}

func TestLineHasNonce_EmptyExpectedNeverMatches(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=abc123"
	if outcome.LineHasNonce(line, "") {
		t.Error("expected an empty nonce to never match")
	}
}

// --- LastCommentLineInLog tests ---

func TestLastCommentLineInLog_Found(t *testing.T) {
	body := "**Verdict** — recommend\n\n<!-- spindrift-research -->"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	path := writeLog(t,
		"some output",
		"SPINDRIFT_COMMENT the-nonce "+encoded,
	)
	got, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got != body {
		t.Errorf("body: got %q, want %q", got, body)
	}
}

func TestLastCommentLineInLog_TakesLast(t *testing.T) {
	stale := base64.StdEncoding.EncodeToString([]byte("stale verdict"))
	final := base64.StdEncoding.EncodeToString([]byte("final verdict"))
	path := writeLog(t,
		"SPINDRIFT_COMMENT the-nonce "+stale,
		"some more output",
		"SPINDRIFT_COMMENT the-nonce "+final,
	)
	got, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if got != "final verdict" {
		t.Errorf("body: got %q, want %q", got, "final verdict")
	}
}

// TestLastCommentLineInLog_ValidLineNotShadowedByLaterRejectedMention
// verifies that a genuine, verified COMMENT line is not suppressed by a
// later line that merely carries the token without verifying (e.g. an
// untrusted issue/comment author's echoed or reasoning-adjacent mention,
// or a stale/wrong-nonce line from a different run) — unlike LastInLog's
// unconditional last-line-wins, the nonce-gated COMMENT signal prefers the
// last line that actually verifies so an attacker cannot suppress a real
// verdict/blocked-note by posting COMMENT-shaped noise after it.
func TestLastCommentLineInLog_ValidLineNotShadowedByLaterRejectedMention(t *testing.T) {
	genuine := base64.StdEncoding.EncodeToString([]byte("genuine verdict"))
	path := writeLog(t,
		"SPINDRIFT_COMMENT the-nonce "+genuine,
		"SPINDRIFT_COMMENT wrong-nonce not-valid-base64!!!",
	)
	got, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true — the genuine line must not be shadowed")
	}
	if got != "genuine verdict" {
		t.Errorf("body: got %q, want %q", got, "genuine verdict")
	}
}

func TestLastCommentLineInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no comment line here")
	_, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastCommentLineInLog_FileNotFound(t *testing.T) {
	_, found, _, err := outcome.LastCommentLineInLog("/nonexistent/path/test.log", "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

// TestLastCommentLineInLog_NonceMismatchIgnoredAndWarned verifies the
// acceptance criterion that a COMMENT line whose nonce does not match is
// ignored (found=false) and reported via a non-nil error rather than ever
// being posted — an untrusted issue/comment author echoing the token (or a
// stale nonce from a different run) never verifies.
func TestLastCommentLineInLog_NonceMismatchIgnoredAndWarned(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("attacker-controlled"))
	path := writeLog(t, "SPINDRIFT_COMMENT wrong-nonce "+encoded)
	_, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a nonce mismatch")
	}
	if err == nil {
		t.Fatal("expected a non-nil error to warn on a nonce mismatch")
	}
}

// TestLastCommentLineInLog_NonceMismatchCountsRejectedLines verifies that
// every token-bearing line that fails to verify is counted, not merely
// collapsed into a bool — a caller wants to know how many spoof/echo
// attempts it saw, not just that at least one happened.
func TestLastCommentLineInLog_NonceMismatchCountsRejectedLines(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("attacker-controlled"))
	path := writeLog(t,
		"SPINDRIFT_COMMENT wrong-nonce-1 "+encoded,
		"SPINDRIFT_COMMENT wrong-nonce-2 "+encoded,
	)
	_, found, rejected, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for two nonce mismatches")
	}
	if err == nil {
		t.Fatal("expected a non-nil error to warn on a nonce mismatch")
	}
	if rejected != 2 {
		t.Errorf("rejected: got %d, want 2", rejected)
	}
}

// TestLastCommentLineInLog_EmptyExpectedNonceNeverMatches verifies that an
// empty expectedNonce (the zero value a caller might pass by mistake) never
// verifies a line, even one that happens to carry no nonce field at all —
// mirroring LineHasNonce's own "empty never matches" invariant now that
// parseSignalLine no longer calls it directly. The fixture line has only one
// field after the token, so under issue #2089 it also looks like a one-field
// doc example rather than a genuine signal attempt: it must stay silently
// non-matched (err == nil) rather than warn, while still preserving the
// empty-nonce-never-verifies invariant via found == false.
func TestLastCommentLineInLog_EmptyExpectedNonceNeverMatches(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("verdict"))
	path := writeLog(t, "SPINDRIFT_COMMENT "+encoded)
	_, found, _, err := outcome.LastCommentLineInLog(path, "")
	if found {
		t.Fatal("expected found=false for an empty expectedNonce")
	}
	if err != nil {
		t.Errorf("unexpected error for a one-field non-attempt line: %v", err)
	}
}

// TestLastCommentLineInLog_MalformedBase64Rejected verifies the strict-decoder
// discipline: a correct nonce with a payload that fails to decode is rejected
// outright, never best-effort decoded.
func TestLastCommentLineInLog_MalformedBase64Rejected(t *testing.T) {
	path := writeLog(t, "SPINDRIFT_COMMENT the-nonce not-valid-base64!!!")
	_, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for malformed base64")
	}
	if err == nil {
		t.Fatal("expected a non-nil error for malformed base64")
	}
}

// TestLastCommentLineInLog_BareProseMentionDoesNotWarn verifies that a line
// merely naming the token in prose — not leading with it — is not treated as
// a signal attempt at all, so it neither verifies nor warns (issue #2089).
func TestLastCommentLineInLog_BareProseMentionDoesNotWarn(t *testing.T) {
	path := writeLog(t, "the SPINDRIFT_COMMENT line, please relay it")
	_, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a bare prose mention")
	}
	if err != nil {
		t.Errorf("unexpected error for a bare prose mention: %v", err)
	}
}

// TestLastCommentLineInLog_DocExampleDoesNotWarn verifies that a line leading
// with the token but carrying only one field after it (a doc example missing
// the base64 payload field) is not treated as a signal attempt, so it neither
// verifies nor warns (issue #2089).
func TestLastCommentLineInLog_DocExampleDoesNotWarn(t *testing.T) {
	path := writeLog(t, "SPINDRIFT_COMMENT deadbeefcafe1234")
	_, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if found {
		t.Fatal("expected found=false for a one-field doc example")
	}
	if err != nil {
		t.Errorf("unexpected error for a one-field doc example: %v", err)
	}
}

// TestLastCommentLineInLog_SurvivesJSONLShapedLog verifies the fix's core
// property (issue #1940, #1921's twin): a stream-json JSONL box log collapses
// the Box's printed line onto one physical file line, JSON-escaping the
// trailing newline into a literal `\n` immediately abutting the base64
// payload with no whitespace in between. The single-line token must still be
// found and decode cleanly out of that shape, unlike the old exact-line
// SPINDRIFT_COMMENT_BEGIN/END block parser it replaces.
func TestLastCommentLineInLog_SurvivesJSONLShapedLog(t *testing.T) {
	body := "**Verdict** — recommend"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	jsonl := `{"type":"assistant","message":{"content":[{"type":"text","text":"blah SPINDRIFT_COMMENT the-nonce ` +
		encoded + `\nSPINDRIFT_OUTCOME issue=1 landing=none status=recommend note=ok\n"}]}}`
	path := writeLog(t, jsonl)
	got, found, _, err := outcome.LastCommentLineInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true from a JSONL-shaped log")
	}
	if got != body {
		t.Errorf("body: got %q, want %q", got, body)
	}
}

// --- AllIssueIntentLinesInLog tests ---

func TestAllIssueIntentLinesInLog_Found(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"title":"bug: widget breaks"}`))
	path := writeLog(t,
		"some output",
		"SPINDRIFT_ISSUE_INTENT the-nonce "+payload,
	)
	got, _, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`{"title":"bug: widget breaks"}`}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestAllIssueIntentLinesInLog_CollectsAll verifies the 1-to-many contract
// (issue #2018's acceptance criterion): several distinct verifying lines in
// one run's log must each contribute their payload, in encounter order —
// unlike LastPRIntentInLog/LastCommentLineInLog's singleton last-wins
// scanners, issue filing is not a single overwritable slot.
func TestAllIssueIntentLinesInLog_CollectsAll(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte(`{"title":"first"}`))
	second := base64.StdEncoding.EncodeToString([]byte(`{"title":"second"}`))
	path := writeLog(t,
		"SPINDRIFT_ISSUE_INTENT the-nonce "+first,
		"some more output",
		"SPINDRIFT_ISSUE_INTENT the-nonce "+second,
	)
	got, _, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`{"title":"first"}`, `{"title":"second"}`}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestAllIssueIntentLinesInLog_NotFound verifies a log with no
// SPINDRIFT_ISSUE_INTENT line at all yields an empty result, not an error.
func TestAllIssueIntentLinesInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no issue-intent line here")
	got, rejected, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if rejected != 0 {
		t.Errorf("rejected: got %d, want 0", rejected)
	}
}

// TestAllIssueIntentLinesInLog_NonceMismatchCountedAsRejected verifies a
// pre-nonce or mismatched-nonce line (issue #1939's shape: an untrusted
// issue/comment author's echoed token, written before this run's nonce was
// minted) is dropped from the collected payloads but still counted via the
// rejected-count return, exactly as the singleton scanners' rejectedCount
// counts a non-verifying token-bearing line (issue #2976) — a caller can now
// settle-log a warning instead of the drop staying entirely silent.
func TestAllIssueIntentLinesInLog_NonceMismatchCountedAsRejected(t *testing.T) {
	genuine := base64.StdEncoding.EncodeToString([]byte(`{"title":"genuine"}`))
	spoofed := base64.StdEncoding.EncodeToString([]byte(`{"title":"spoofed"}`))
	path := writeLog(t,
		"SPINDRIFT_ISSUE_INTENT the-nonce "+genuine,
		"SPINDRIFT_ISSUE_INTENT wrong-nonce "+spoofed,
	)
	got, rejected, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`{"title":"genuine"}`}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
	if rejected != 1 {
		t.Errorf("rejected: got %d, want 1", rejected)
	}
}

// TestAllIssueIntentLinesInLog_DedupsSubagentEcho covers the subagent-echo
// case (issue #2068): when the Filer runs as a subagent, its single
// SPINDRIFT_ISSUE_INTENT line appears twice in the raw stream-json log — once
// as the subagent's own `assistant` event and once echoed in the parent
// implementor's `tool_result` event. Both decode to a byte-identical payload
// and both verify, so a no-dedup scan would collect the same finding twice and
// file two issues. The scan must collapse identical decoded payloads to one so
// one finding files exactly one issue.
func TestAllIssueIntentLinesInLog_DedupsSubagentEcho(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"title":"bug: widget breaks","body":"details"}`))
	line := "SPINDRIFT_ISSUE_INTENT the-nonce " + payload
	path := writeLog(t,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"`+line+`"}]}}`,
		`{"type":"tool_result","content":[{"type":"text","text":"`+line+`"}]}`,
	)
	got, rejected, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`{"title":"bug: widget breaks","body":"details"}`}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
	if rejected != 0 {
		t.Errorf("rejected: got %d, want 0 (both lines verify; dedup is not rejection)", rejected)
	}
}

// TestAllIssueIntentLinesInLog_MalformedBase64CountedAsRejected verifies a
// line carrying the token with an undecodable payload is dropped from the
// collected payloads but counted via rejectedCount, exactly as a nonce
// mismatch is (issue #2976): both are "token-bearing lines that failed to
// verify" per parseSignalLine, so both count.
func TestAllIssueIntentLinesInLog_MalformedBase64CountedAsRejected(t *testing.T) {
	genuine := base64.StdEncoding.EncodeToString([]byte(`{"title":"genuine"}`))
	path := writeLog(t,
		"SPINDRIFT_ISSUE_INTENT the-nonce not-valid-base64!!!",
		"SPINDRIFT_ISSUE_INTENT the-nonce "+genuine,
	)
	got, rejected, err := outcome.AllIssueIntentLinesInLog(path, "the-nonce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`{"title":"genuine"}`}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
	if rejected != 1 {
		t.Errorf("rejected: got %d, want 1", rejected)
	}
}

// --- Resolve tests ---

func TestResolve(t *testing.T) {
	cases := []struct {
		name           string
		logs           func(t *testing.T) []outcome.PassLog
		kind           string
		wantFound      bool
		wantProvenance outcome.Provenance
		wantStatus     string
		wantKind       string
	}{
		{
			name: "single log genuine",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"some output",
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "single log synthetic",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"some output",
					"SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=driver exited nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceSynthetic,
			wantStatus:     "blocked",
			wantKind:       "work",
		},
		{
			name: "multi log second has outcome",
			logs: func(t *testing.T) []outcome.PassLog {
				first := writeLog(t, "no outcome here")
				second := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: first}, {Label: "pass-2", Path: second}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "multi log last pass wins",
			logs: func(t *testing.T) []outcome.PassLog {
				first := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stale nonce=the-nonce",
				)
				second := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: first}, {Label: "pass-2", Path: second}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "no logs no outcome",
			logs: func(t *testing.T) []outcome.PassLog {
				return nil
			},
			wantFound: false,
			wantKind:  "work",
		},
		{
			name: "empty kind normalizes to work",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			kind:           "",
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "research kind passes through unchanged",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/comments/1 status=ready note=ok nonce=the-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			kind:           "research",
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "research",
		},
		{
			name: "no genuine outcome falls back to self-report",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"some narration, no outcome line at all",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound: false,
			wantKind:  "work",
		},
		{
			// Pre-#2274 this nonce-free full-grammar line was excluded from the
			// genuine tier and surfaced via the self-report fallback. With the
			// gate gone it is simply a genuine outcome.
			name: "nonce-free full-grammar line is genuine",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=merged note=self only",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "merged",
			wantKind:       "work",
		},
		{
			// ADR 0039 / issue #2274: the outcome path no longer gates on a
			// nonce, so a genuine line carrying any nonce= value (even one
			// that would once have failed the gate) is accepted as genuine.
			name: "genuine line with any nonce value is accepted (no gate)",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=someone-elses-guess",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "genuine-shaped line with a nonce field is accepted",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok nonce=whatever",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			name: "genuine blocked line is accepted regardless of nonce field",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=no gate here nonce=wrong-nonce",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "blocked",
			wantKind:       "work",
		},
		{
			name: "multi log last pass wins across nonce-free genuine lines",
			logs: func(t *testing.T) []outcome.PassLog {
				first := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=first",
				)
				second := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=second",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: first}, {Label: "pass-2", Path: second}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "ready",
			wantKind:       "work",
		},
		{
			// Issue #2973: a single log whose only content is a tool_result echo
			// of issue/comment text that happens to embed the token mid-JSON,
			// with field markers but no leading-token line anywhere in the log,
			// resolves as plain no-outcome (mirrors
			// TestLastInLog_ToolResultEchoIsNotFound one layer up, through
			// Resolve).
			name: "tool_result echo mid-JSON mention is not a candidate",
			logs: func(t *testing.T) []outcome.PassLog {
				path := writeLog(t,
					`{"type":"tool_result","content":"...text mentioning SPINDRIFT_OUTCOME issue=1 landing=agent/issue-2973 status=ready note=echoed from issue body..."}`,
				)
				return []outcome.PassLog{{Label: "pass-1", Path: path}}
			},
			wantFound: false,
			wantKind:  "work",
		},
		{
			// Issue #2973: a later log whose only content is a mid-sentence
			// mention of the token (field-bearing but not leading the line, so
			// not a candidate at all -- see
			// TestLastInLog_FieldBearingMidSentenceMentionIsNotACandidate) must
			// not shadow an earlier log's genuine leading-token outcome. Before
			// the mention-tier fallback was deleted, that later near-miss would
			// have overridden the earlier winner via the err != nil precedence
			// arm in Resolve.
			name: "later mid-sentence mention does not shadow earlier genuine outcome",
			logs: func(t *testing.T) []outcome.PassLog {
				first := writeLog(t,
					"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=final nonce=the-nonce",
				)
				second := writeLog(t,
					"done: SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok wrapped in a sentence",
				)
				return []outcome.PassLog{{Label: "pass-1", Path: first}, {Label: "pass-2", Path: second}}
			},
			wantFound:      true,
			wantProvenance: outcome.ProvenanceGenuine,
			wantStatus:     "blocked",
			wantKind:       "work",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := tc.logs(t)
			got, err := outcome.Resolve(logs, tc.kind)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Found != tc.wantFound {
				t.Errorf("Found: got %v, want %v", got.Found, tc.wantFound)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind: got %q, want %q", got.Kind, tc.wantKind)
			}
			if !tc.wantFound {
				return
			}
			if got.Provenance != tc.wantProvenance {
				t.Errorf("Provenance: got %q, want %q", got.Provenance, tc.wantProvenance)
			}
			if got.Outcome.Status != tc.wantStatus {
				t.Errorf("Outcome.Status: got %q, want %q", got.Outcome.Status, tc.wantStatus)
			}
		})
	}
}

// TestResolve_BareWordLeadingLineIsNearMiss pins the post-#2274 behavior for a
// bare-word leading line ("SPINDRIFT_OUTCOME: success"). Before the nonce gate
// was retired (ADR 0039), a nonce-less line like this was excluded from the
// genuine tier ahead of Parse, letting Resolve fall through to the self-report
// tier. With the gate gone, lastInLog reaches Parse and the line is its normal
// near-miss, which Resolve propagates as an error rather than a fabricated
// outcome.
func TestResolve_BareWordLeadingLineIsNearMiss(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME: success",
	)
	logs := []outcome.PassLog{{Label: "pass-1", Path: path}}

	got, err := outcome.Resolve(logs, "")
	if err == nil {
		t.Fatal("expected a near-miss error, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
	if got.Found {
		t.Error("Found: got true, want false on a near-miss")
	}
}

// TestResolve_SelfReportAlwaysPopulated pins that Resolved.SelfReport /
// Resolved.SelfReportFound carry the self-report signal alongside whichever
// tier actually won Outcome/Provenance -- not only when the self-report tier
// is Resolve's own last-resort fallback (issue #2268 slice 1).
func TestResolve_SelfReportAlwaysPopulated(t *testing.T) {
	t.Run("later synthetic backstop wins but earlier self-report survives", func(t *testing.T) {
		path := writeLog(t,
			"SPINDRIFT_OUTCOME: success",
			"SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=driver exited nonce=the-nonce",
		)
		logs := []outcome.PassLog{{Label: "pass-1", Path: path}}

		got, err := outcome.Resolve(logs, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Found {
			t.Fatal("Found: got false, want true")
		}
		if got.Provenance != outcome.ProvenanceSynthetic {
			t.Errorf("Provenance: got %q, want %q", got.Provenance, outcome.ProvenanceSynthetic)
		}
		if got.Outcome.Status != "blocked" {
			t.Errorf("Outcome.Status: got %q, want %q", got.Outcome.Status, "blocked")
		}
		if !got.SelfReportFound {
			t.Fatal("SelfReportFound: got false, want true")
		}
		if got.SelfReport.Status != "success" {
			t.Errorf("SelfReport.Status: got %q, want %q", got.SelfReport.Status, "success")
		}
	})

	t.Run("no self-report line at all leaves SelfReportFound false", func(t *testing.T) {
		// The only leading-token line is the synthetic backstop itself, which
		// lastSelfReportInLog explicitly excludes (synthetic=true), so there
		// is no non-synthetic self-report line anywhere in the log.
		path := writeLog(t,
			"some output",
			"SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=driver exited nonce=the-nonce",
		)
		logs := []outcome.PassLog{{Label: "pass-1", Path: path}}

		got, err := outcome.Resolve(logs, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Found {
			t.Fatal("Found: got false, want true")
		}
		if got.Provenance != outcome.ProvenanceSynthetic {
			t.Errorf("Provenance: got %q, want %q", got.Provenance, outcome.ProvenanceSynthetic)
		}
		if got.SelfReportFound {
			t.Errorf("SelfReportFound: got true, want false")
		}
		if got.SelfReport != (outcome.SelfReport{}) {
			t.Errorf("SelfReport: got %+v, want zero value", got.SelfReport)
		}
	})
}

// TestResolve_SelfReportErrorPopulated pins Resolved.SelfReportError (issue
// #2343 slice 1): when the self-report tier's log walk hits a genuine I/O
// error on one log, the error is now surfaced on Resolved.SelfReportError
// while Found/Provenance/Outcome selection -- driven entirely by the
// genuine/synthetic tier here -- is unaffected. This is purely additive: a
// caller that ignores SelfReportError sees identical Found/Provenance/Outcome
// behavior to before this field existed.
func TestResolve_SelfReportErrorPopulated(t *testing.T) {
	badDir := t.TempDir()
	goodPath := writeLog(t,
		"SPINDRIFT_OUTCOME issue=9 landing=https://github.com/o/r/pull/9 status=ready note=all good",
	)
	logs := []outcome.PassLog{
		{Label: "bad", Path: badDir},
		{Label: "good", Path: goodPath},
	}

	got, err := outcome.Resolve(logs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Found {
		t.Fatal("Found: got false, want true")
	}
	if got.Provenance != outcome.ProvenanceGenuine {
		t.Errorf("Provenance: got %q, want %q", got.Provenance, outcome.ProvenanceGenuine)
	}
	if got.Outcome.Status != "ready" {
		t.Errorf("Outcome.Status: got %q, want %q", got.Outcome.Status, "ready")
	}
	if got.SelfReportError == nil {
		t.Fatal("SelfReportError: got nil, want a non-nil I/O error from the bad log")
	}
}

// TestResolve_MultiLogSelfReportErrorDoesNotAbortWalk pins the existing
// silent-skip guarantee (issue #2343 slice 1): a bad log sandwiched between
// two good ones does not abort the self-report walk -- it is skipped and the
// walk keeps going, same as before this field existed. Here pass-1 supplies
// an early self-report, pass-2 (a directory) errors, and pass-3 supplies both
// the authoritative genuine outcome and a later self-report that overwrites
// pass-1's as the winner (last pass wins, unaffected by pass-2's error in
// between). Found/Provenance/Outcome are driven entirely by pass-3's clean
// genuine match; SelfReportError still surfaces pass-2's I/O error even
// though the self-report walk's very next pass succeeded -- the error field
// is its own last-seen tracker, independent of the winning report. A caller
// who ignores SelfReportError sees identical Found/Provenance/Outcome/
// SelfReport/SelfReportFound behavior to before this field existed.
func TestResolve_MultiLogSelfReportErrorDoesNotAbortWalk(t *testing.T) {
	pass1 := writeLog(t, "SPINDRIFT_OUTCOME: success")
	badDir := t.TempDir()
	pass3 := writeLog(t,
		"SPINDRIFT_OUTCOME issue=9 landing=https://github.com/o/r/pull/9 status=ready note=all good",
	)
	logs := []outcome.PassLog{
		{Label: "pass-1", Path: pass1},
		{Label: "pass-2-bad", Path: badDir},
		{Label: "pass-3", Path: pass3},
	}

	got, err := outcome.Resolve(logs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Found {
		t.Fatal("Found: got false, want true: the walk must not abort because of the bad middle log")
	}
	if got.Provenance != outcome.ProvenanceGenuine {
		t.Errorf("Provenance: got %q, want %q", got.Provenance, outcome.ProvenanceGenuine)
	}
	if got.Outcome.Status != "ready" {
		t.Errorf("Outcome.Status: got %q, want %q", got.Outcome.Status, "ready")
	}
	if !got.SelfReportFound {
		t.Fatal("SelfReportFound: got false, want true")
	}
	if got.SelfReport.Status != "ready" {
		t.Errorf("SelfReport.Status: got %q, want %q (pass-3's later self-report wins)", got.SelfReport.Status, "ready")
	}
	if got.SelfReportError == nil {
		t.Fatal("SelfReportError: got nil, want the bad middle log's I/O error to still surface")
	}
}
