// Package opencode is the opencode Driver's host-side half (ADR 0009): the
// transient-error taxonomy and NDJSON event parsing for `opencode run
// --format json`, which emits one JSON object per line with no envelope
// wrapping the stream. This package uses driverkit's Class/Reason types
// directly, so the registration adapter in driver/opencode.go needs no cast.
package opencode

import (
	"bytes"
	"encoding/json"
	"strings"

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

// event's Error is either a plain string (the form this package's fixtures
// use) or a NamedError-style object such as
// {"name":"APIError","data":{"statusCode":429,...}}.
type event struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
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
// lines is scanned, so a marker quoted in the agent's own type:"text" prose is
// not attributed as the cause. A log with no error event, or whose error text
// carries no known marker, classifies as Terminal/TaskFailed, as does a missing
// log file.
func Classify(logPath string) (driverkit.Classification, error) {
	cl, found, err := driverkit.ClassifyScan(logPath, logscan.SkipOversized, func(chunk string) driverkit.ScanDecision {
		s := strings.TrimSpace(chunk)
		if s == "" {
			return driverkit.ScanDecision{Skip: true}
		}
		var ev event
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return driverkit.ScanDecision{Skip: true}
		}
		if ev.Type != "error" {
			return driverkit.ScanDecision{Skip: true}
		}
		return driverkit.ScanDecision{Text: errorText(ev.Error), Overwrite: true}
	}, transientExtras, nil)
	if err != nil {
		return driverkit.Classification{}, err
	}
	if !found {
		return driverkit.Classification{Class: driverkit.Terminal, Reason: driverkit.TaskFailed}, nil
	}
	return cl, nil
}

// ResultText returns the text of every NDJSON text event in the log at
// logPath, one value per line: the Go twin of the in-box
// `jq -r 'select(.type == "text") | .part.text // empty'`. Non-string text is
// dropped; it can never carry an outcome line.
func ResultText(logPath string) (string, error) {
	return driverkit.ResultText(logPath, func(line string) (string, bool) {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "text" {
			return "", false
		}
		return ev.Part.Text, true
	})
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
