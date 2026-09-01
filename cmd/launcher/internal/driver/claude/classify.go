// Package claude is the claude Driver's host-side half (ADR 0009): the
// Anthropic transient-error taxonomy, stream-json heartbeat parsing, the claude
// CLI transcript shape, and usage-log parsing. The parent driver package owns
// the Driver interface and the registry wiring; the shared
// Class/Reason/Classification vocabulary lives in driverkit and is used here
// directly, with no local aliases.
package claude

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// resetsAtRe matches the JSON field "resetsAt":UNIX_TIMESTAMP (integer).
var resetsAtRe = regexp.MustCompile(`"resetsAt"\s*:\s*(\d+)`)

// resetsTextRe matches the human-readable reset suffix claude emits on
// plain-text rate-limit messages, e.g. "resets 6:30pm (UTC)" or "resets Mon
// 12:00am (UTC)". The optional weekday is a 3-letter prefix followed by any
// remaining letters of the full weekday name.
var resetsTextRe = regexp.MustCompile(`resets\s+(?:([A-Za-z]{3})\w*\s+)?(\d{1,2}):(\d{2})(am|pm)\s*\(UTC\)`)

// staleGraceWindow bounds how stale a bare-form (no weekday) reset-time
// candidate can be while still being returned as-is instead of rolled forward a
// full day. dispatch/retry.go's hold path already clamps a past ResetAt's wait
// to Policy.Jitter, so a trivially-stale candidate — clock skew, or a limit that
// genuinely refreshed moments ago — should fall through to that clamp for a
// near-immediate retry rather than be rolled a needless ~24h forward.
const staleGraceWindow = 5 * time.Minute

var weekdayAbbrs = map[string]time.Weekday{
	"sun": time.Sunday,
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
}

// transientExtras is claude's ordered API-error marker list, checked before the
// shared driverkit.BaseTransientPatterns network suffix. Patterns are
// deliberately specific to avoid matching ordinary log content that happens to
// contain digit sequences (issue numbers, byte counts, ports).
var transientExtras = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "usage_limit_reached", Reason: driverkit.RateLimit},
	{Substr: "server_error", Reason: driverkit.Overloaded},
	{Substr: "429 Too Many Requests", Reason: driverkit.RateLimit},
	{Substr: "529 Overloaded", Reason: driverkit.Overloaded},
	{Substr: "Claude Code usage limit reached", Reason: driverkit.RateLimit},
	{Substr: "hit your session limit", Reason: driverkit.RateLimit},
	{Substr: "hit your weekly limit", Reason: driverkit.RateLimit},
	{Substr: "hit your Opus limit", Reason: driverkit.RateLimit},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
	{Substr: "net/http: request canceled", Reason: driverkit.Network},
}

// terminalExtras holds markers for non-retryable failures whose specific cause
// is worth naming to the operator: classifying the --agents rejection distinctly
// rather than as generic TaskFailed tells them the fix is to bump claude-code
// (or blank SCOUT_MODEL/REVIEW_MODEL), not to retry futilely. Routed through the
// same self-poison/echo guard as transientExtras, so a box editing this very
// string in its own agent content is not misattributed.
var terminalExtras = []driverkit.Pattern{
	{Substr: "unknown option '--agents'", Reason: driverkit.UnsupportedFlag},
}

// matchMarker classifies a single log line; a transient API/network marker takes
// precedence over a terminal CLI-usage one. Returns ("", "", false) when neither
// matches.
func matchMarker(line string) (driverkit.Reason, driverkit.Class, bool) {
	if r, ok := driverkit.MatchTransient(line, transientExtras); ok {
		return r, driverkit.Transient, true
	}
	if r, ok := driverkit.MatchExtras(line, terminalExtras); ok {
		return r, driverkit.Terminal, true
	}
	return "", "", false
}

// scanResult accumulates everything Classify needs from one pass over the log.
type scanResult struct {
	cl       driverkit.Classification
	found    bool
	resetsAt *time.Time
}

// Classify scans the box log at logPath and returns a Classification describing
// whether the failure is transient (retryable) or terminal (genuine). A missing
// log file is terminal/taskFailed; lines larger than the 4 MiB scan buffer are
// processed in chunks rather than skipped.
//
// Markers are scoped to lines that are not agent-authored content, so a
// tool_result, assistant-text, or file-edit line quoting a rate-limit string
// verbatim is not attributed as the cause (see isAgentContentEvent). On a 429
// carrying a "resetsAt" field, the Classification's ResetAt is non-nil so
// callers can hold until the known reset time.
func Classify(logPath string) (driverkit.Classification, error) {
	return classifyAt(logPath, time.Now())
}

// classifyAt is Classify's implementation with an explicit now, so tests can pin
// the clock instead of leaking the real wall clock into the parsed resetsAt
// fallback.
func classifyAt(logPath string, now time.Time) (driverkit.Classification, error) {
	sr, err := scanLog(logPath, now)
	if err != nil {
		return driverkit.Classification{}, err
	}

	if !sr.found {
		return driverkit.Classification{Class: driverkit.Terminal, Reason: driverkit.TaskFailed}, nil
	}

	cl := sr.cl
	if cl.Reason == driverkit.RateLimit {
		cl.ResetAt = sr.resetsAt
	}
	return cl, nil
}

// scanLog returns the transient reason and resetsAt of the last unrecovered
// candidate: a match is dropped once agent-authored content is seen after it,
// since that means the run continued past it. Known gap: a chunk of an oversized
// (> 4 MiB) agent-content line fails isAgentContentEvent's whole-chunk JSON parse
// and so falls through to the normal scan.
//
// A type:"result" line gets special treatment: the claude CLI echoes the
// preceding assistant turn's text into its "result" field on an ordinary
// completion, so an echo of a marker the genuine content quoted is not scanned
// as a fresh signal. The pending echo survives any number of intervening
// non-content lines, cleared only by the type:"result" line itself or by a second
// genuine agent-content event.
func scanLog(logPath string, now time.Time) (scanResult, error) {
	var resetsAt *time.Time
	var echoReason driverkit.Reason
	var echoPending bool
	extract := func(chunk string) driverkit.ScanDecision {
		if isAgentContentEvent(chunk) {
			// Agent content can quote rate-limit markers verbatim, and any
			// candidate found so far is unattributable to the actual exit —
			// the run continued past it — so drop it and look for a later,
			// genuine cause.
			resetsAt = nil
			// Remember whether this genuine content itself quoted a marker, so
			// a later type:"result" echo of it is not read as a fresh signal.
			echoReason, _, echoPending = matchMarker(chunk)
			return driverkit.ScanDecision{Reset: true, Skip: true}
		}
		if echoPending {
			if resultText, ok := resultEventText(chunk); ok {
				echoPending = false
				if reason, _, matched := matchMarker(resultText); matched && reason == echoReason {
					return driverkit.ScanDecision{Skip: true}
				}
			}
		}
		// First marker in the log wins: ClassifyScan latches on the first
		// unrecovered match. Within one chunk it prefers transient over
		// terminal, but across chunks a terminal marker seen first latches
		// Terminal. Harmless for the --agents case: that CLI-usage error aborts
		// the run before any API call, so no transient marker can precede it in
		// a genuine failure log.
		if resetsAt == nil {
			if t := extractResetsAt(chunk, now); t != nil {
				resetsAt = t
			}
		}
		return driverkit.ScanDecision{Text: chunk}
	}

	cl, found, err := driverkit.ClassifyScan(logPath, logscan.ChunkOversized, extract, transientExtras, terminalExtras)
	if err != nil {
		return scanResult{}, err
	}
	return scanResult{cl: cl, found: found, resetsAt: resetsAt}, nil
}

// agentContentEvent is the minimal envelope for telling a stream-json line of
// agent-authored content from a genuine terminating API error event.
type agentContentEvent struct {
	Type    string `json:"type"`
	Error   string `json:"error"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// syntheticModelSentinel is the claude CLI's message.model value for its
// synthetic terminator event on a mid-stream API error. This is an undocumented
// runtime contract with the CLI, not a spindrift constant — if a future CLI
// version changes it, isAgentContentEvent's guard below silently stops matching.
const syntheticModelSentinel = "<synthetic>"

// isAgentContentEvent reports whether chunk is a stream-json line carrying
// agent-authored content — an assistant message, or a user message (tool_result
// content, per the Claude API's convention of returning tool results as a
// user-role turn). Markers inside either are the agent's own work product, not a
// terminating API error, and must not be scanned. Lines that fail to parse as
// JSON, or parse with any other type, are left to the normal scan.
//
// The one exception: an assistant-typed event with message.model set to
// syntheticModelSentinel and a top-level "error" field is the CLI's own
// synthetic terminator, not agent content, so it too falls to the normal scan.
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

// resultEventEnvelope identifies a stream-json type:"result" line and extracts
// its echoed result text — on an ordinary (non-error) completion, that field
// mirrors the preceding assistant turn. IsError distinguishes that echo from a
// genuine terminating API error, whose "result" text must not be suppressed.
type resultEventEnvelope struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// resultEventText returns chunk's "result" text when chunk is a type:"result"
// line for an ordinary completion. It returns false for is_error:true, since
// that text is a genuine error, not an echo of preceding content.
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
// content as a UTC time, falling back to parseResetsAtText's human-readable form
// when that field is absent or unparseable. Returns nil if neither form matches.
func extractResetsAt(content string, now time.Time) *time.Time {
	if m := resetsAtRe.FindStringSubmatch(content); m != nil {
		if secs, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			t := time.Unix(secs, 0).UTC()
			return &t
		}
	}
	return parseResetsAtText(content, now)
}

// parseResetsAtText parses claude's human-readable "resets <clock-time> (UTC)"
// or "resets <Weekday> <clock-time> (UTC)" suffix and returns that clock-time's
// occurrence today (bare form) or on the next matching weekday. It returns nil
// only when the suffix is absent or unparseable — never merely because the
// computed occurrence is stale relative to now. When the candidate is not after
// now:
//
//   - Weekday form: always rolls forward 7 days, however stale, since a
//     weekly-cadence marker off by even minutes still means "next week", and a
//     short generic backoff on a weekly-scale reset is far worse than one
//     correct week-long hold.
//   - Bare form: rolls forward 1 day, unless stale by no more than
//     staleGraceWindow, in which case it is returned unchanged (still in the
//     past) — see staleGraceWindow.
//
// It never calls time.Now(); the caller supplies the reference time.
func parseResetsAtText(content string, now time.Time) *time.Time {
	m := resetsTextRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}

	weekdayAbbr, hourStr, minuteStr, meridiem := m[1], m[2], m[3], m[4]

	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 1 || hour > 12 {
		return nil
	}
	minute, err := strconv.Atoi(minuteStr)
	if err != nil || minute < 0 || minute > 59 {
		return nil
	}

	switch meridiem {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	default:
		return nil
	}

	now = now.UTC()

	isWeekdayForm := weekdayAbbr != ""

	var candidate time.Time
	if !isWeekdayForm {
		candidate = time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	} else {
		wantWeekday, ok := weekdayAbbrs[strings.ToLower(weekdayAbbr)]
		if !ok {
			return nil
		}
		candidate = time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
		daysUntil := (int(wantWeekday) - int(candidate.Weekday()) + 7) % 7
		candidate = candidate.AddDate(0, 0, daysUntil)
	}

	if candidate.After(now) {
		return &candidate
	}

	// The candidate is stale (at or before now).
	if isWeekdayForm {
		candidate = candidate.AddDate(0, 0, 7)
		return &candidate
	}

	if now.Sub(candidate) <= staleGraceWindow {
		return &candidate
	}

	candidate = candidate.AddDate(0, 0, 1)
	return &candidate
}
