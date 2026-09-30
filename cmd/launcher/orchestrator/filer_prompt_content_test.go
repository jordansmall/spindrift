package main

import "testing"

// TestFilerPromptReportShapeContract pins filer-prompt.md's FILED/QUEUED
// report-shape prose (issue #3726 finding 2): QUEUED must cover both signal
// carriers — a SPINDRIFT_ISSUE_INTENT marker line under the log carrier, and
// an accepted `driver-exec signal issue-intent` call under the socket
// carrier — since this text is ungated and read under either carrier as-is.
func TestFilerPromptReportShapeContract(t *testing.T) {
	assertPromptClauses(t, "filer-prompt.md", []promptClause{
		{
			name:   "QUEUED covers the log-carrier SPINDRIFT_ISSUE_INTENT line",
			clause: "a `SPINDRIFT_ISSUE_INTENT` line under the `log` carrier",
		},
		{
			name:   "QUEUED covers a socket-carrier driver-exec signal issue-intent call the socket accepted",
			clause: "a `driver-exec signal issue-intent` call the socket accepted",
		},
		{
			name:   "QUEUED is for an intent handed to the launcher's relay, not filed until relayed",
			clause: "`QUEUED` is for an intent you handed to the launcher's relay",
		},
	})
}

// TestFilerPromptSiteKeyRuleContract pins filer-prompt.md's step 2 site-key
// rule (issue #4108): a finding naming a symbol keys on `file:Symbol`, never
// a line or range even when it also cites lines, and the key names the code
// the finding is about, not the test file/line where it was noticed. It also
// pins the in-batch dedup instruction, since the host catches only exact
// keys and overlapping line ranges, never a divergent key for one symbol.
func TestFilerPromptSiteKeyRuleContract(t *testing.T) {
	assertPromptClauses(t, "filer-prompt.md", []promptClause{
		{
			name:   "a symbol keys on file:Symbol, never a line or range, even when the finding also cites lines",
			clause: "never a line or line range, even when the finding also cites lines",
		},
		{
			name:   "key on the code the finding is about, not the test file/line where it was noticed",
			clause: "Key on the code the finding is about (the symbol under test), not the test file or line where the defect was noticed",
		},
		{
			name:   "dedup the batch against itself before filing",
			clause: "Dedup the batch against itself first: two findings sharing a site key, or naming the same symbol, are one finding — file it once",
		},
	})
}
