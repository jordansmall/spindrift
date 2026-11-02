package main

import (
	"strings"
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

	// Only the IF BLOCKED step names a step 5 to exit to; the open step's
	// exit is the OUTCOME fragment.
	t.Run("if-blocked gives the resend loop an exit", func(t *testing.T) {
		assertPromptClauses(t, "fragments/if-blocked-pr-outbox-socket.md", []promptClause{{
			name:   "stop resending and go on to step 5",
			clause: "If it never exits zero, even after you fixed each rejection's reason and sent it again — here or before you reached IF BLOCKED — stop resending and go on to step 5, naming the rejection in `note=` alongside what's done and what remains.",
		}})
	})

	t.Run("outcome points at IF BLOCKED", func(t *testing.T) {
		assertPromptClauses(t, "fragments/outcome-ready-means-outbox-socket.md", []promptClause{{
			name:   "follow IF BLOCKED when no intent is queued",
			clause: "no intent is queued: follow IF BLOCKED below instead, which ends in `status=blocked` with a `note=` naming the rejection.",
		}})
	})
}

// TestPRIntentSocketFragmentSharedSpanParity pins the two PR-intent
// fragments' shared span byte-for-byte (issue #3866): both fragments
// intentionally duplicate everything from the "closing SPINDRIFT_SIGNAL_EOF"
// sentence through "never the run.", so a wording fix (e.g. the
// single-quote escape) applied to only one of the pair drifts silently
// unless this test catches it. The IF BLOCKED fragment appends its own exit after the span
// (issue #6256), so the open fragment must still end at it.
func TestPRIntentSocketFragmentSharedSpanParity(t *testing.T) {
	marker := "   The closing `SPINDRIFT_SIGNAL_EOF`"
	end := "never the run."

	openRaw := readPromptFile(t, repoRoot, "fragments/open-pr-create-outbox-socket.md")
	openSpan := sharedSpanThrough(t, openRaw, marker, end)
	blockedSpan := sharedSpanThrough(t, readPromptFile(t, repoRoot, "fragments/if-blocked-pr-outbox-socket.md"), marker, end)

	if openSpan != blockedSpan {
		t.Errorf("open-pr-create-outbox-socket.md and if-blocked-pr-outbox-socket.md diverge in their shared span (from %q through %q):\nopen-pr-create-outbox-socket.md:\n%s\n\nif-blocked-pr-outbox-socket.md:\n%s", marker, end, openSpan, blockedSpan)
	}

	if strings.TrimRight(sharedSpanFrom(t, openRaw, marker), "\n") != openSpan {
		t.Errorf("open-pr-create-outbox-socket.md has text after %q that the parity no longer pins", end)
	}
}
