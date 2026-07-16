// Package claude is the claude Driver's host-side half (ADR 0009): the
// Anthropic transient-error taxonomy, stream-json heartbeat parsing, the
// claude CLI transcript shape, and usage-log parsing. The parent driver
// package owns the Driver interface, the shared Class/Reason/Classification
// vocabulary, and the registry wiring; this package must not import it (the
// registration adapter in driver/claude.go imports this package, not the
// other way around, to avoid a cycle) — Classify therefore returns its own
// Class/Reason values, mirrored 1:1 onto driver.Class/driver.Reason by that
// adapter.
package claude

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/logscan"
)

// Class describes whether a non-zero agent exit is retryable or not.
// Mirrors driver.Class; see the package doc for why this is a local copy.
type Class string

const (
	Transient Class = "transient"
	Terminal  Class = "terminal"
)

// Reason identifies the specific cause of a classified exit.
// Mirrors driver.Reason; see the package doc for why this is a local copy.
type Reason string

const (
	RateLimit  Reason = "rateLimit"  // Claude API 429 rate limit
	Overloaded Reason = "overloaded" // Claude API 529 / overloaded_error
	Network    Reason = "network"    // transient network failure
	TaskFailed Reason = "taskFailed" // agent ran but produced no valid result
)

// Classification is the result of Classify.
type Classification struct {
	Class   Class
	Reason  Reason
	ResetAt *time.Time // non-nil only for RateLimit with a known reset time
}

// resetsAtRe matches the JSON field "resetsAt":UNIX_TIMESTAMP (integer).
var resetsAtRe = regexp.MustCompile(`"resetsAt"\s*:\s*(\d+)`)

// transientPatterns lists log-line substrings that mark a transient failure.
// Patterns are deliberately specific to avoid matching ordinary log content
// (issue numbers, byte counts, port numbers, etc. containing digit sequences).
// The first match in the ordered list wins when multiple markers appear.
var transientPatterns = []struct {
	substr string
	reason Reason
}{
	// Structured API error types — highest specificity, check first.
	{"rate_limit_error", RateLimit},
	{"overloaded_error", Overloaded},
	{"usage_limit_reached", RateLimit},
	{"server_error", Overloaded},
	// HTTP status phrase patterns — specific enough to avoid false positives.
	{"429 Too Many Requests", RateLimit},
	{"529 Overloaded", Overloaded},
	// Claude plain-text error messages.
	{"Claude Code usage limit reached", RateLimit},
	{"Overloaded", Overloaded},
	// Network-level failures logged by the Go HTTP client or stdlib.
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"net/http: request canceled", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// scanResult accumulates everything Classify needs from one pass over the log.
type scanResult struct {
	reason   Reason
	found    bool
	resetsAt *time.Time
}

// Classify scans the box log at logPath and returns a Classification
// describing whether the failure is transient (retryable) or terminal
// (genuine).
//
// Markers are scoped to lines that are not agent-authored content: a
// tool_result, assistant-text, or file-edit line quoting a rate-limit string
// verbatim (e.g. a box working on rate-limit code) is not attributed as the
// cause (issue #579). See isAgentContentEvent.
//
// When the log contains a 429 rate-limit marker with a "resetsAt" field, the
// returned Classification carries a non-nil ResetAt so callers can hold until
// the known reset time.
//
// A missing log file is treated as terminal/taskFailed. Lines larger than the
// 4 MiB scan buffer are processed in chunks, matching the same resilience
// contract as lastInLog.
func Classify(logPath string) (Classification, error) {
	sr, err := scanLog(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Classification{Class: Terminal, Reason: TaskFailed}, nil
		}
		return Classification{}, err
	}

	if !sr.found {
		return Classification{Class: Terminal, Reason: TaskFailed}, nil
	}

	cl := Classification{Class: Transient, Reason: sr.reason}
	if sr.reason == RateLimit {
		cl.ResetAt = sr.resetsAt
	}
	return cl, nil
}

// scanLog reads logPath line by line and returns a scanResult with the
// transient reason and resetsAt timestamp of the last unrecovered candidate:
// a match is dropped once agent-authored content (see isAgentContentEvent)
// is seen after it, since that means the run continued past it. Oversized
// lines (> 4 MiB) are processed in chunks rather than skipped, so markers in
// large JSON blobs are still detected — except a chunk of an oversized
// agent-content line, which fails the whole-chunk JSON parse in
// isAgentContentEvent and so falls through to the normal scan (known gap,
// issue #579 review).
//
// A type:"result" line immediately following genuine agent content also
// gets special treatment: the claude CLI echoes the preceding assistant
// turn's text into that line's "result" field on an ordinary completion, so
// if the genuine content quoted a transient marker, the echo is recognized
// and not scanned as a fresh signal (issue #818).
func scanLog(logPath string) (scanResult, error) {
	var sr scanResult
	var echoReason Reason
	var echoPending bool
	err := logscan.ForEachLine(logPath, logscan.ChunkOversized, func(chunk string) {
		if isAgentContentEvent(chunk) {
			// The agent's own tool_result / assistant-text / file-edit
			// content can quote rate-limit markers verbatim (e.g. while
			// working on rate-limit code). Any transient candidate found so
			// far is unattributable to the actual exit — the run continued
			// past it — so drop it and look for a later, genuine cause
			// (issue #579).
			sr = scanResult{}
			// Remember whether this genuine content itself quoted a marker,
			// so a type:"result" line right after it that echoes the same
			// marker is recognized as that same echo, not a fresh signal
			// (issue #818).
			echoReason, echoPending = matchTransient(chunk)
			return
		}
		if echoPending {
			echoPending = false
			if resultText, ok := resultEventText(chunk); ok {
				if reason, matched := matchTransient(resultText); matched && reason == echoReason {
					return
				}
			}
		}
		if !sr.found {
			if reason, ok := matchTransient(chunk); ok {
				sr.found = true
				sr.reason = reason
			}
		}
		if sr.resetsAt == nil {
			if t := extractResetsAt(chunk); t != nil {
				sr.resetsAt = t
			}
		}
	})
	if err != nil {
		return scanResult{}, err
	}
	return sr, nil
}

// agentContentEvent is the minimal envelope needed to identify a Claude Code
// stream-json line as agent-authored content (an "assistant" turn or a
// "user" tool-result turn) rather than a genuine terminating API error event.
type agentContentEvent struct {
	Type    string `json:"type"`
	Error   string `json:"error"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// isAgentContentEvent reports whether chunk is a stream-json line carrying
// agent-authored content — an assistant message (prose, or a file-edit tool
// call's input) or a user message (tool_result content, per the Claude API's
// convention of returning tool results as a user-role turn). Markers inside
// either are the agent's own work product, not a genuine terminating API
// error, and must not be scanned for transient patterns or a resetsAt
// timestamp. Lines that fail to parse as JSON (plain-text driver/network
// error output) or that parse with any other type ("error", "system",
// "result", or none) are left to the normal scan.
//
// The one exception: an assistant-typed event with message.model:"<synthetic>"
// and a top-level "error" field is not agent-authored — it's the claude CLI's
// own synthetic terminator for a mid-stream API error (issue #815) — so it is
// left to the normal scan too.
func isAgentContentEvent(chunk string) bool {
	var ev agentContentEvent
	if err := json.Unmarshal([]byte(chunk), &ev); err != nil {
		return false
	}
	if ev.Type == "assistant" && ev.Message.Model == "<synthetic>" && ev.Error != "" {
		return false
	}
	return ev.Type == "assistant" || ev.Type == "user"
}

// resultEventEnvelope is the minimal envelope for identifying a Claude Code
// stream-json type:"result" line and extracting its echoed result text — the
// terminal line's "result" field mirrors the immediately preceding assistant
// turn's text on an ordinary (non-error) completion.
type resultEventEnvelope struct {
	Type   string `json:"type"`
	Result string `json:"result"`
}

// resultEventText reports whether chunk is a stream-json type:"result" line
// and, if so, returns its "result" field text.
func resultEventText(chunk string) (string, bool) {
	var ev resultEventEnvelope
	if err := json.Unmarshal([]byte(chunk), &ev); err != nil {
		return "", false
	}
	if ev.Type != "result" {
		return "", false
	}
	return ev.Result, true
}

// matchTransient checks whether line contains a known transient marker.
// Returns the first matching reason in pattern order.
func matchTransient(line string) (Reason, bool) {
	for _, p := range transientPatterns {
		if strings.Contains(line, p.substr) {
			return p.reason, true
		}
	}
	return "", false
}

// extractResetsAt parses the first "resetsAt":UNIX_TIMESTAMP occurrence in
// content and returns a UTC time, or nil if none is found or the value is
// unparseable.
func extractResetsAt(content string) *time.Time {
	m := resetsAtRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil
	}
	t := time.Unix(secs, 0).UTC()
	return &t
}
