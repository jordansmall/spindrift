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
