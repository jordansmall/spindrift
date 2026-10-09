package main

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
)

// TestResearchVerdictSocketFragmentContract pins the prose of the three
// research-verdict socket fragments (issue #3726, fixing a blocking review
// finding): the socket has a hard signalwire.MaxBodyBytes-per-field ceiling
// (enforced by signalwire.CheckFields), and an oversize body is refused
// outright, never truncated. It also pins the #3866 clauses shared with the
// other socket fragments: a one-call stdin heredoc send, the `-body-file`
// flag set, a bare/unpiped send, and the exit code as the acceptance.
func TestResearchVerdictSocketFragmentContract(t *testing.T) {
	fragments := []string{
		"fragments/research-verdict-github-readonly-socket.md",
		"fragments/research-verdict-forgejo-readonly-socket.md",
		"fragments/research-verdict-local-socket.md",
	}

	cases := []promptClause{
		{
			name:   "driver-exec signal comment is the verb, heredoc on stdin",
			clause: "driver-exec signal comment <<'SPINDRIFT_SIGNAL_EOF'",
		},
		{
			name:   "the only flag is named",
			clause: "The only flag is `-body-file` (optional)",
		},
		{
			name:   "the byte ceiling is named",
			clause: fmt.Sprintf("a hard ceiling of %d bytes per field", signalwire.MaxBodyBytes),
		},
		{
			name:   "an oversize body is refused, not truncated",
			clause: "A body over that ceiling is refused outright, never truncated",
		},
		{
			name:   "run the command bare, never piped",
			clause: "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		},
		{
			name:   "the exit code is the acceptance",
			clause: "The command's exit code is the acceptance",
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

// TestResearchVerdictSocketFragmentsSharedClosingSpanParity pins the three
// research-verdict socket fragments' shared closing span byte-for-byte
// (issue #3866): all three intentionally duplicate everything from "The
// closing SPINDRIFT_SIGNAL_EOF" onward, so a wording fix applied to fewer
// than all three drifts silently unless this test catches it.
func TestResearchVerdictSocketFragmentsSharedClosingSpanParity(t *testing.T) {
	marker := "The closing `SPINDRIFT_SIGNAL_EOF`"

	github := sharedSpanFrom(t, readPromptFile(t, repoRoot, "fragments/research-verdict-github-readonly-socket.md"), marker)
	forgejo := sharedSpanFrom(t, readPromptFile(t, repoRoot, "fragments/research-verdict-forgejo-readonly-socket.md"), marker)
	local := sharedSpanFrom(t, readPromptFile(t, repoRoot, "fragments/research-verdict-local-socket.md"), marker)

	if github != forgejo {
		t.Errorf("research-verdict-github-readonly-socket.md and research-verdict-forgejo-readonly-socket.md diverge in their shared span (from %q to end of file):\ngithub:\n%s\n\nforgejo:\n%s", marker, github, forgejo)
	}
	if github != local {
		t.Errorf("research-verdict-github-readonly-socket.md and research-verdict-local-socket.md diverge in their shared span (from %q to end of file):\ngithub:\n%s\n\nlocal:\n%s", marker, github, local)
	}
}
