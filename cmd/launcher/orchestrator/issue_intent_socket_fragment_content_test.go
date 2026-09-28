package main

import (
	"strings"
	"testing"
)

// TestIssueIntentSocketFragmentContract pins the prose of the four
// issue-intent socket fragments (issues #3866, #3909): a one-call stdin
// heredoc send, the complete flag set (no `-body` flag), the three `-type`
// values, backtick-safe title quoting, and never piping the send.
func TestIssueIntentSocketFragmentContract(t *testing.T) {
	fragments := []string{
		"fragments/filer-file-relay-socket.md",
		"fragments/file-issues-relay-socket.md",
		"fragments/research-file-issues-relay-socket.md",
		"fragments/butler-file-issues-relay-socket.md",
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
	// three only summarize the filer's send for a coordinator/researcher/
	// butler who never runs it themselves, so they stay third-person
	// throughout ("The filer runs the command bare").
	for _, fragment := range fragments {
		t.Run(fragment, func(t *testing.T) {
			runCommandBareClause := "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`"
			if fragment == "fragments/filer-file-relay-socket.md" {
				runCommandBareClause = "Run the command bare — never through `tail`, `head`, or `2>&1 |`"
			}
			assertPromptClauses(t, fragment, cases)
			assertPromptClauses(t, fragment, []promptClause{
				{name: "run the command bare, never piped", clause: runCommandBareClause},
			})

			// Only filer-file-relay-socket.md is a runnable example (the
			// filer is who actually sends the command); the other three are
			// third-person summaries for a coordinator/researcher/butler who
			// never runs it, so they carry no heredoc example to pin here.
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

// TestIssueIntentRelaySummariesSharedSendParagraph pins the three relay
// summaries' send paragraph byte-for-byte (issues #3866, #3909):
// file-issues-relay-socket.md, research-file-issues-relay-socket.md, and
// butler-file-issues-relay-socket.md intentionally duplicate the "The filer
// sends each in one call" paragraph, so a wording fix applied to only one of
// the three drifts silently unless this test catches it. The butler's flag
// clause legitimately differs — `-class` and `-concurrence` are butler-only
// (cmd/launcher/driver-exec/signal_cmd.go, issue #3880) — so that clause is
// normalized to the shared "and `-body-file`" wording before comparing; any
// other wording change in any copy still fails.
func TestIssueIntentRelaySummariesSharedSendParagraph(t *testing.T) {
	marker := "The filer sends each in one call"

	fileIssues := sharedSpanBetween(t, readPromptFile(t, repoRoot, "fragments/file-issues-relay-socket.md"), marker)
	researchFileIssues := sharedSpanBetween(t, readPromptFile(t, repoRoot, "fragments/research-file-issues-relay-socket.md"), marker)
	butlerFileIssues := sharedSpanBetween(t, readPromptFile(t, repoRoot, "fragments/butler-file-issues-relay-socket.md"), marker)

	if fileIssues != researchFileIssues {
		t.Errorf("file-issues-relay-socket.md and research-file-issues-relay-socket.md diverge in their shared send paragraph (from %q):\nfile-issues-relay-socket.md:\n%s\n\nresearch-file-issues-relay-socket.md:\n%s", marker, fileIssues, researchFileIssues)
	}

	const butlerOnlyClause = "`-body-file`, `-class`, and `-concurrence` (the last two only\nwhen the finding has one)"
	if strings.Count(butlerFileIssues, butlerOnlyClause) != 1 {
		t.Fatalf("butler-file-issues-relay-socket.md send paragraph does not contain the butler-only flag clause exactly once (from %q; re-wrapping that clause also trips this):\n%s", marker, butlerFileIssues)
	}
	normalizedButler := strings.Replace(butlerFileIssues, butlerOnlyClause, "and `-body-file`", 1)
	if normalizedButler != fileIssues {
		t.Errorf("butler-file-issues-relay-socket.md diverges from file-issues-relay-socket.md in their shared send paragraph beyond the butler-only flag clause (from %q):\nfile-issues-relay-socket.md:\n%s\n\nbutler-file-issues-relay-socket.md (normalized):\n%s", marker, fileIssues, normalizedButler)
	}
}

// TestFilerFileRelaySocketFragmentSendShape pins filer-file-relay-socket.md's
// heredoc example carrying -dedup, the closing delimiter sitting alone at
// column one, and no bare EOF delimiter, since that fragment (unlike the
// coordinator/research/butler ones, which only summarize the filer's send) is the
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
