// Package opencode is the opencode Driver's host-side half (ADR 0009, 0065):
// the decoder for `opencode run --format json`, which emits one JSON object per
// line with no envelope wrapping the stream, and the transient-error taxonomy.
// Its readers delegate to driverkit's shared readers over that decoder. This
// package uses driverkit's Class/Reason types directly, so the registration
// adapter in driver/opencode.go needs no cast.
package opencode

import (
	"encoding/json"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// transientExtras is opencode's complete ordered marker list, checked before
// the shared driverkit.BaseTransientPatterns network suffix. The numeric status
// markers are phrase-anchored so a digit run such as a token count or timestamp
// does not match (issue #4423). opencode supplies its own list to keep 429
// precedence over "overloaded_error"/"Overloaded".
var transientExtras = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "status 429", Reason: driverkit.RateLimit},
	{Substr: "status code 429", Reason: driverkit.RateLimit},
	{Substr: "429 Too Many Requests", Reason: driverkit.RateLimit},
	{Substr: `"statusCode":429`, Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "status 529", Reason: driverkit.Overloaded},
	{Substr: "status code 529", Reason: driverkit.Overloaded},
	{Substr: `"statusCode":529`, Reason: driverkit.Overloaded},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
}

// terminalExtras holds markers for non-retryable harness argv rejections. opencode
// rejects an unknown flag (the harness passes effort as --variant) through
// yargs .strict() with a custom fail handler that prints the `run` usage dump
// instead of yargs' "Unknown argument" line, then exits 1; "Unknown argument"
// stays as a guard for an opencode that stops swallowing that message. The
// usage banner also fires on yargs' sibling rejections ("Not enough non-option
// arguments", "Invalid values:"), all of which are argv faults a retry cannot
// fix.
var terminalExtras = []driverkit.Pattern{
	{Substr: "opencode run [message..]", Reason: driverkit.UnsupportedFlag},
	{Substr: "Unknown argument", Reason: driverkit.UnsupportedFlag},
}

// Classify scans the box log at logPath and reports whether the failure is
// transient (retryable) or terminal; see driverkit.Classify for the scan
// semantics. A missing log classifies as Terminal/TaskFailed.
func Classify(logPath string) (driverkit.Classification, error) {
	return driverkit.Classify(logPath, NewDecoder, transientExtras, terminalExtras)
}

// ResultText returns the text of the last assistant message in the log at
// logPath (see driverkit.LastMessageText), as claude's result is its final
// message only: an outcome-looking line in mid-pass prose must not end the run
// (issue #5354).
func ResultText(logPath string) (string, error) {
	return driverkit.LastMessageText(logPath, NewDecoder)
}

// ResultEvent encodes text as one newline-terminated NDJSON text event, the
// inverse of ResultText: a synthetic final result the orchestrator appends to
// the stream log (issue #4406).
func ResultEvent(text string) ([]byte, error) {
	type part struct {
		Text string `json:"text"`
	}
	b, err := json.Marshal(struct {
		Type string `json:"type"`
		Part part   `json:"part"`
	}{"text", part{text}})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
