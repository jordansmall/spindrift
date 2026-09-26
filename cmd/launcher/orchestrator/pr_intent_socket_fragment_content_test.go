package main

import (
	"testing"
)

// TestPRIntentSocketFragmentContract pins the prose of the two PR-intent
// socket fragments (issue #3866): a one-call stdin heredoc send, the
// complete `-title`/`-body-file` flag set (no `-nonce`), and never piping
// the send.
func TestPRIntentSocketFragmentContract(t *testing.T) {
	fragments := []string{
		"fragments/open-pr-create-outbox-socket.md",
		"fragments/if-blocked-pr-outbox-socket.md",
	}

	cases := []promptClause{
		{
			name:   "the heredoc opens with the title flag and no -body-file",
			clause: `driver-exec signal pr-intent -title '<conventional title>' <<'SPINDRIFT_SIGNAL_EOF'`,
		},
		{
			name:   "there is no -nonce flag",
			clause: "There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line.",
		},
		{
			name:   "run the command bare, never piped",
			clause: "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		},
		{
			name:   "the exit code is the acceptance",
			clause: "The command's exit code is the acceptance",
		},
		{
			name:   "resending replaces the earlier intent",
			clause: "Sending the command again replaces the earlier intent rather than racing it",
		},
		{
			name:   "single-quote titles need the escape",
			clause: "A title containing a single quote needs `'\\''` in its place",
		},
	}

	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			assertPromptClauses(t, fragment, cases)
			assertBodyFileAfterHeredoc(t, fragment)
			assertClosingDelimiterAtColumnOne(t, fragment)
			assertNoBareEOFDelimiter(t, fragment)
		})
	}
}

// TestPRIntentSocketFragmentSharedSpanParity pins the two PR-intent
// fragments' shared closing span byte-for-byte (issue #3866): both
// fragments intentionally duplicate everything from the "closing
// SPINDRIFT_SIGNAL_EOF" sentence onward, so a wording fix (e.g. the
// single-quote escape) applied to only one of the pair drifts silently
// unless this test catches it.
func TestPRIntentSocketFragmentSharedSpanParity(t *testing.T) {
	marker := "   The closing `SPINDRIFT_SIGNAL_EOF`"

	openSpan := sharedSpanFrom(t, readPromptFile(t, repoRoot, "fragments/open-pr-create-outbox-socket.md"), marker)
	blockedSpan := sharedSpanFrom(t, readPromptFile(t, repoRoot, "fragments/if-blocked-pr-outbox-socket.md"), marker)

	if openSpan != blockedSpan {
		t.Errorf("open-pr-create-outbox-socket.md and if-blocked-pr-outbox-socket.md diverge in their shared span (from %q to end of file):\nopen-pr-create-outbox-socket.md:\n%s\n\nif-blocked-pr-outbox-socket.md:\n%s", marker, openSpan, blockedSpan)
	}
}
