// Package claude is the claude Driver's host-side half (ADR 0009): the
// Anthropic transient-error taxonomy, stream-json heartbeat parsing, the
// claude CLI transcript shape, and usage-log parsing. The parent driver
// package owns the Driver interface and the registry wiring; the shared
// Class/Reason/Classification vocabulary lives in driverkit, and this
// package type-aliases it, so the registration adapter in driver/claude.go
// needs no cast between this package's and driver's Class/Reason values.
package claude

import (
	"encoding/json"
	"regexp"
	"strconv"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// Class describes whether a non-zero agent exit is retryable or not.
type Class = driverkit.Class

const (
	Transient = driverkit.Transient
	Terminal  = driverkit.Terminal
)

// Reason identifies the specific cause of a classified exit.
type Reason = driverkit.Reason

const (
	RateLimit       = driverkit.RateLimit
	Overloaded      = driverkit.Overloaded
	Network         = driverkit.Network
	TaskFailed      = driverkit.TaskFailed
	UnsupportedFlag = driverkit.UnsupportedFlag
)

// Classification is the result of Classify.
type Classification = driverkit.Classification

// resetsAtRe matches the JSON field "resetsAt":UNIX_TIMESTAMP (integer).
var resetsAtRe = regexp.MustCompile(`"resetsAt"\s*:\s*(\d+)`)

// transientExtras holds claude's complete ordered API-error marker list,
// checked before the shared driverkit.BaseTransientPatterns network suffix.
// Patterns are deliberately specific to avoid matching ordinary log content
// (issue numbers, byte counts, port numbers, etc. containing digit
// sequences).
var transientExtras = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: RateLimit},
	{Substr: "overloaded_error", Reason: Overloaded},
	{Substr: "usage_limit_reached", Reason: RateLimit},
	{Substr: "server_error", Reason: Overloaded},
	{Substr: "429 Too Many Requests", Reason: RateLimit},
	{Substr: "529 Overloaded", Reason: Overloaded},
	{Substr: "Claude Code usage limit reached", Reason: RateLimit},
	{Substr: "hit your session limit", Reason: RateLimit},
	{Substr: "hit your weekly limit", Reason: RateLimit},
	{Substr: "hit your Opus limit", Reason: RateLimit},
	{Substr: "Overloaded", Reason: Overloaded},
	{Substr: "net/http: request canceled", Reason: Network},
}

// terminalExtras holds markers for genuine, non-retryable failures whose
// specific cause is worth naming to the operator. A claude-code build that
// predates the --agents flag rejects it outright (issue #1552); classifying
// that distinctly, instead of the generic TaskFailed bucket, tells the
// operator the fix is to bump claude-code (or blank SCOUT_MODEL/REVIEW_MODEL)
// rather than retry, which is futile. Routed through the same self-poison /
// echo guard as transientExtras so a box editing this very string in its own
// agent content is not misattributed (issues #579/#818).
var terminalExtras = []driverkit.Pattern{
	{Substr: "unknown option '--agents'", Reason: UnsupportedFlag},
}

// matchMarker classifies a single log line: a transient API/network marker
// (Transient) takes precedence over a terminal CLI-usage marker (Terminal).
// Returns ("", "", false) when neither matches.
func matchMarker(line string) (Reason, Class, bool) {
	if r, ok := driverkit.MatchTransient(line, transientExtras); ok {
		return r, Transient, true
	}
	if r, ok := driverkit.MatchExtras(line, terminalExtras); ok {
		return r, Terminal, true
	}
	return "", "", false
}

// scanResult accumulates everything Classify needs from one pass over the log.
type scanResult struct {
	reason   Reason
	class    Class
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
		return Classification{}, err
	}

	if !sr.found {
		return Classification{Class: Terminal, Reason: TaskFailed}, nil
	}

	cl := Classification{Class: sr.class, Reason: sr.reason}
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
// A type:"result" line — whether immediately following genuine agent
// content or after intervening non-content lines (e.g. type:"system"
// heartbeats) — also gets special treatment: the claude CLI echoes the
// preceding assistant turn's text into that line's "result" field on an
// ordinary completion, so if the genuine content quoted a transient marker,
// the echo is recognized and not scanned as a fresh signal (issue #818).
// The pending echo survives any number of intervening non-content lines and
// is only cleared by the type:"result" line itself or by a second genuine
// agent-content event (issue #1197).
func scanLog(logPath string) (scanResult, error) {
	var sr scanResult
	var echoReason Reason
	var echoPending bool
	err := driverkit.ScanLog(logPath, logscan.ChunkOversized, func(chunk string) {
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
			echoReason, _, echoPending = matchMarker(chunk)
			return
		}
		if echoPending {
			if resultText, ok := resultEventText(chunk); ok {
				echoPending = false
				if reason, _, matched := matchMarker(resultText); matched && reason == echoReason {
					return
				}
			}
		}
		if !sr.found {
			// First marker in the log wins: once a chunk matches, sr.found
			// latches and later chunks are ignored. matchMarker prefers a
			// transient marker over a terminal one *within* a single chunk,
			// but across chunks a terminal marker seen first now latches
			// Terminal — before this change every match was transient, so
			// ordering never crossed classes. Harmless for the --agents case:
			// that CLI-usage error aborts the run before any API call, so no
			// transient marker can precede it in a genuine failure log.
			if reason, class, ok := matchMarker(chunk); ok {
				sr.found = true
				sr.reason = reason
				sr.class = class
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

// syntheticModelSentinel is the claude CLI's message.model value for its
// synthetic terminator event on a mid-stream API error (issue #815). This is
// a runtime contract with the CLI, not a spindrift constant — if a future CLI
// version changes it, isAgentContentEvent's guard below silently stops
// matching (issue #820).
//
// No official doc documents this literal model-field value (issue #1203). Two
// adjacent official pages document the surrounding behavior instead: the
// terminal error text this event carries —
// https://code.claude.com/docs/en/errors — and the stream-json output format
// — https://code.claude.com/docs/en/headless — whose distinct system/api_retry
// event shares the same error-category vocabulary (e.g. "server_error") but is
// the retry event, not this terminator.
const syntheticModelSentinel = "<synthetic>"

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
// The one exception: an assistant-typed event with message.model set to
// syntheticModelSentinel and a top-level "error" field is not agent-authored
// — it's the claude CLI's own synthetic terminator for a mid-stream API
// error (issue #815) — so it is left to the normal scan too.
func isAgentContentEvent(chunk string) bool {
	var ev agentContentEvent
	if err := json.Unmarshal([]byte(chunk), &ev); err != nil {
		return false
	}
	if ev.Type == "assistant" && ev.Message.Model == syntheticModelSentinel && ev.Error != "" {
		return false
	}
	return ev.Type == "assistant" || ev.Type == "user"
}

// resultEventEnvelope is the minimal envelope for identifying a Claude Code
// stream-json type:"result" line and extracting its echoed result text — the
// terminal line's "result" field mirrors the immediately preceding assistant
// turn's text on an ordinary (non-error) completion. IsError distinguishes
// that ordinary-completion echo from a genuine terminating API error, whose
// "result" text is not an echo and must not be suppressed as one.
type resultEventEnvelope struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// resultEventText reports whether chunk is a stream-json type:"result" line
// for an ordinary (non-error) completion and, if so, returns its "result"
// field text. It returns false for a type:"result" line with is_error:true,
// since that text is a genuine error, not an echo of preceding content.
func resultEventText(chunk string) (string, bool) {
	var ev resultEventEnvelope
	if err := json.Unmarshal([]byte(chunk), &ev); err != nil {
		return "", false
	}
	if ev.Type != "result" || ev.IsError {
		return "", false
	}
	return ev.Result, true
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
