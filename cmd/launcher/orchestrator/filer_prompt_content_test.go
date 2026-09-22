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
