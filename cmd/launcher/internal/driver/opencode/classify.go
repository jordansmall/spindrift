// Package opencode is the opencode Driver's host-side half (ADR 0009): the
// transient-error taxonomy and NDJSON event parsing for `opencode run
// --format json`, which emits one JSON object per line with no envelope
// wrapping the stream. This package uses driverkit's Class/Reason types
// directly, so the registration adapter in driver/opencode.go needs no cast.
package opencode

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
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

// agentProgressTypes are the event types that show the agent kept working after
// an error, so that error was recovered from. step_finish is absent on purpose:
// it can close the very step whose error was just emitted.
var agentProgressTypes = map[string]bool{
	"text": true, "reasoning": true, "tool_use": true, "step_start": true,
}

// event's Error is either a plain string (the form this package's fixtures
// use) or a NamedError-style object such as
// {"name":"APIError","data":{"statusCode":429,...}}.
type event struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"` // epoch milliseconds
	Error     json.RawMessage `json:"error"`
}

// resetFrom derives the rate-limit reset time from the response headers of a
// NamedError payload, in the order opencode's own session/retry.ts reads them:
// retry-after-ms, then retry-after as seconds, then retry-after as an HTTP date.
// A relative delay is anchored to the event timestamp rather than a wall clock,
// so the result is deterministic; a relative delay with no timestamp yields nil.
func resetFrom(raw json.RawMessage, timestampMs int64) *time.Time {
	var payload struct {
		Data struct {
			ResponseHeaders map[string]string `json:"responseHeaders"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	headers := map[string]string{}
	for k, v := range payload.Data.ResponseHeaders {
		headers[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	relative := func(value string, unit time.Duration) *time.Time {
		// MaxInt32 keeps n*unit inside time.Duration's int64 range for either unit.
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || !(n >= 0 && n <= math.MaxInt32) || timestampMs <= 0 {
			return nil
		}
		t := time.UnixMilli(timestampMs).Add(time.Duration(n * float64(unit))).UTC()
		return &t
	}
	if t := relative(headers["retry-after-ms"], time.Millisecond); t != nil {
		return t
	}
	if t := relative(headers["retry-after"], time.Second); t != nil {
		return t
	}
	if t, err := http.ParseTime(headers["retry-after"]); err == nil {
		t = t.UTC()
		return &t
	}
	return nil
}

// errorText returns the string payload unquoted, or any other payload in
// compact JSON so a marker like `"statusCode":429` has no stray whitespace. A
// missing or invalid payload yields "", which matches no marker.
func errorText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}

// Classify scans the box log at logPath and reports whether the failure is
// transient (retryable) or terminal. Only the error payload of type:"error"
// lines is scanned for transient markers, so a marker quoted in the agent's own
// type:"text" prose is not attributed as the cause. An error followed by an
// agentProgressTypes event was recovered from and is dropped; the last
// unrecovered error wins. Non-JSON lines (stderr shares the log) count only
// when they match a terminalExtras marker. A log with no unrecovered error, or
// whose error text carries no known marker, classifies as Terminal/TaskFailed,
// as does a missing log file.
func Classify(logPath string) (driverkit.Classification, error) {
	// resetAt is the reset of the latched error, so it moves only with the latch.
	var resetAt *time.Time
	cl, found, err := driverkit.ClassifyScan(logPath, logscan.SkipOversized, func(chunk string) driverkit.ScanDecision {
		s := strings.TrimSpace(chunk)
		if s == "" {
			return driverkit.ScanDecision{Skip: true}
		}
		var ev event
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			if _, ok := driverkit.MatchExtras(s, terminalExtras); ok {
				return driverkit.ScanDecision{Text: s}
			}
			return driverkit.ScanDecision{Skip: true}
		}
		if agentProgressTypes[ev.Type] {
			resetAt = nil
			return driverkit.ScanDecision{Reset: true, Skip: true}
		}
		if ev.Type != "error" {
			return driverkit.ScanDecision{Skip: true}
		}
		text := errorText(ev.Error)
		if _, ok := driverkit.MatchTransient(text, transientExtras); ok {
			resetAt = resetFrom(ev.Error, ev.Timestamp)
		}
		return driverkit.ScanDecision{Text: text, Overwrite: true}
	}, transientExtras, terminalExtras)
	if err != nil {
		return driverkit.Classification{}, err
	}
	if !found {
		return driverkit.Classification{Class: driverkit.Terminal, Reason: driverkit.TaskFailed}, nil
	}
	if cl.Reason == driverkit.RateLimit {
		cl.ResetAt = resetAt
	}
	return cl, nil
}

// ResultText returns the text of the last assistant message in the log at
// logPath, one text part per line, as claude's result is its final message
// only: an outcome-looking line in mid-pass prose must not end the run (issue
// #5354). Any event whose part.messageID differs from the current one starts a
// new message, so a textless final message yields "" like claude's empty
// result; that also covers an oversized final text line skipped under
// SkipOversized, whose step events still carry the new ID. A text event with
// no messageID also starts a new message, so the ID-less synthetic ResultEvent
// stays the final one; other ID-less events do not. Empty text is dropped, and
// a line that fails to decode (non-string text included) is skipped whole, its
// messageID too.
func ResultText(logPath string) (string, error) {
	var b strings.Builder
	var curID string
	err := driverkit.ScanLog(logPath, logscan.SkipOversized, func(line string) {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				MessageID string `json:"messageID"`
				Text      string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		id := ev.Part.MessageID
		isText := ev.Type == "text"
		if (id != "" && id != curID) || (isText && id == "") {
			b.Reset()
			curID = id
		}
		if isText && ev.Part.Text != "" {
			b.WriteString(ev.Part.Text)
			b.WriteByte('\n')
		}
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
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
