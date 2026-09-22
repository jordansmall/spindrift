package main

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
)

// TestResearchVerdictSocketFragmentContract pins the prose of the three
// research-verdict socket fragments (issue #3726, fixing a blocking review
// finding): the socket has a hard signalwire.MaxBodyBytes-per-field ceiling
// (enforced by signalsocket.validate), and an oversize body is refused
// outright, never truncated.
func TestResearchVerdictSocketFragmentContract(t *testing.T) {
	fragments := []string{
		"fragments/research-verdict-github-readonly-socket.md",
		"fragments/research-verdict-forgejo-readonly-socket.md",
		"fragments/research-verdict-local-socket.md",
	}

	cases := []promptClause{
		{
			name:   "driver-exec signal comment is the verb",
			clause: "driver-exec signal comment -body-file verdict.md",
		},
		{
			name:   "the byte ceiling is named",
			clause: fmt.Sprintf("a hard ceiling of %d bytes per field", signalwire.MaxBodyBytes),
		},
		{
			name:   "an oversize body is refused, not truncated",
			clause: "A body over that ceiling is refused outright, never truncated",
		},
	}

	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			assertPromptClauses(t, fragment, cases)
		})
	}
}
