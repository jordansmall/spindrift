package main

import "testing"

// TestFilerFileRelayFragmentDedupTermsExample pins filer-file-relay.md's
// two `dedupTerms` JSON examples (issue #4108), mirroring how
// TestIssueIntentSocketFragmentContract pins the socket carrier's `-dedup
// 'path/to/file.go:Symbol'` example: both examples must keep the
// `path/to/file.go:Symbol` site key literally, so a rewording can't silently
// drop the one concrete shape a filer has to match when building a payload.
func TestFilerFileRelayFragmentDedupTermsExample(t *testing.T) {
	assertPromptClauses(t, "fragments/filer-file-relay.md", []promptClause{
		{
			name:   "the untyped payload example's dedupTerms example",
			clause: `'{"title":"...","body":"...","dedupTerms":["path/to/file.go:Symbol"]}'`,
		},
		{
			name:   "the typed payload example's dedupTerms example",
			clause: `'{"title":"...","body":"...","dedupTerms":["path/to/file.go:Symbol"],"type":"bug"}'`,
		},
	})
}

// TestFilerFileRelayFragmentsWarnLineKeyGoesStale pins the parenthetical both
// filer-file-relay fragments carry beside the dedup-key instruction (issue
// #3812): a line key from step 2 still goes stale once the code around it
// moves, so the carrier prose doesn't imply any key it is handed is durable.
func TestFilerFileRelayFragmentsWarnLineKeyGoesStale(t *testing.T) {
	const clause = "(a line key from step 2 still goes stale once the code around it moves)"
	for _, name := range []string{"filer-file-relay", "filer-file-relay-socket"} {
		assertPromptClauses(t, "fragments/"+name+".md", []promptClause{
			{
				name:   name + " warns a line key still goes stale",
				clause: clause,
			},
		})
	}
}
