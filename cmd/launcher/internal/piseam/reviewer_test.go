//go:build integration

package piseam

import (
	"strings"
	"testing"
)

// TestPiReviewerVerdict scripts the reply shape a reviewer role must produce:
// VERDICT on the first line, findings below. It runs a reasoning model with
// --thinking high so the captured stream shows a non-off thinking level.
func TestPiReviewerVerdict(t *testing.T) {
	const verdict = "VERDICT: APPROVE"
	s := newStub(t, reasoningTextReply("Checking the diff for blocking defects.",
		verdict+"\n\n## Blocking\n- none\n\n## Non-blocking\n- none"))

	r := runPiWith(t, s, piOpts{
		reasoning: true,
		args:      []string{"--model", stubModelRef, "--thinking", "high"},
	}, "Review the change and open with a VERDICT line.")
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	text := finalAssistantText(r.events)
	if first, _, _ := strings.Cut(text, "\n"); first != verdict {
		t.Errorf("final assistant text first line = %q, want %q:\n%s", first, verdict, text)
	}
}
