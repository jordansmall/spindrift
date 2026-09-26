package main

import (
	"testing"
)

// TestIssueIntentSocketFragmentContract pins the prose of the three
// issue-intent socket fragments (issue #3866): a one-call stdin heredoc
// send, the complete flag set (no `-body` flag), the three `-type` values,
// backtick-safe title quoting, and never piping the send.
func TestIssueIntentSocketFragmentContract(t *testing.T) {
	fragments := []string{
		"fragments/filer-file-relay-socket.md",
		"fragments/file-issues-relay-socket.md",
		"fragments/research-file-issues-relay-socket.md",
	}

	cases := []promptClause{
		{
			name:   "there is no -body flag",
			clause: "no `-body` flag",
		},
		{
			name:   "the three type values",
			clause: "`bug`, `enhancement`, `chore`",
		},
		{
			name:   "backtick-safe title quoting",
			clause: "title with backticks needs single quotes, not double",
		},
		{
			name:   "apostrophe-safe title quoting",
			clause: "title containing a single quote needs `'\\''` in its place",
		},
		{
			name:   "the exit code is the acceptance",
			clause: "exit code is the acceptance",
		},
	}

	// filer-file-relay-socket.md addresses the filer directly, the one
	// that actually runs the command ("Run the command bare"). The other
	// two only summarize the filer's send for a coordinator/researcher who
	// never runs it themselves, so they stay third-person throughout
	// ("The filer runs the command bare").
	runCommandBareClause := map[string]string{
		"fragments/filer-file-relay-socket.md":           "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		"fragments/file-issues-relay-socket.md":          "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
		"fragments/research-file-issues-relay-socket.md": "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
	}

	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			assertPromptClauses(t, fragment, cases)
			assertPromptClauses(t, fragment, []promptClause{
				{name: "run the command bare, never piped", clause: runCommandBareClause[fragment]},
			})

			// Only filer-file-relay-socket.md is a runnable example (the
			// filer is who actually sends the command); the other two are
			// third-person summaries for a coordinator/researcher who never
			// runs it, so they carry no heredoc example to pin here.
			if fragment == "fragments/filer-file-relay-socket.md" {
				assertPromptClauses(t, fragment, []promptClause{
					{
						name:   "the heredoc opens with the title flag and the -type flag",
						clause: `driver-exec signal issue-intent -title '<title>' -type`,
					},
				})
				assertBodyFileAfterHeredoc(t, fragment)
				assertNoBareEOFDelimiter(t, fragment)
			}
		})
	}
}

// TestIssueIntentRelaySummariesSharedSendParagraph pins the two relay
// summaries' send paragraph byte-for-byte (issue #3866): both
// file-issues-relay-socket.md and research-file-issues-relay-socket.md
// intentionally duplicate the "The filer sends each in one call" paragraph,
// so a wording fix applied to only one of the pair drifts silently unless
// this test catches it.
func TestIssueIntentRelaySummariesSharedSendParagraph(t *testing.T) {
	marker := "The filer sends each in one call"

	fileIssues := sharedSpanBetween(t, readPromptFile(t, repoRoot, "fragments/file-issues-relay-socket.md"), marker)
	researchFileIssues := sharedSpanBetween(t, readPromptFile(t, repoRoot, "fragments/research-file-issues-relay-socket.md"), marker)

	if fileIssues != researchFileIssues {
		t.Errorf("file-issues-relay-socket.md and research-file-issues-relay-socket.md diverge in their shared send paragraph (from %q):\nfile-issues-relay-socket.md:\n%s\n\nresearch-file-issues-relay-socket.md:\n%s", marker, fileIssues, researchFileIssues)
	}
}

// TestFilerFileRelaySocketFragmentSendShape pins filer-file-relay-socket.md's
// heredoc example carrying -dedup, the closing delimiter sitting alone at
// column one, and no bare EOF delimiter, since that fragment (unlike the
// coordinator/research ones, which only summarize the filer's send) is the
// one actually instructing the filer to send the command.
func TestFilerFileRelaySocketFragmentSendShape(t *testing.T) {
	assertPromptClauses(t, "fragments/filer-file-relay-socket.md", []promptClause{
		{
			name:   "the heredoc example carries -dedup",
			clause: `driver-exec signal issue-intent -title '<title>' -type bug -dedup '<site key>' <<'SPINDRIFT_SIGNAL_EOF'`,
		},
	})
	assertClosingDelimiterAtColumnOne(t, "fragments/filer-file-relay-socket.md")
	assertNoBareEOFDelimiter(t, "fragments/filer-file-relay-socket.md")
}
