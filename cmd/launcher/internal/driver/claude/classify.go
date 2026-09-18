// Package claude is the claude Driver's host-side half (ADR 0009): the
// Anthropic transient-error taxonomy, stream-json heartbeat parsing, the
// claude CLI transcript shape, and usage-log parsing. It uses driverkit's
// Class/Reason/Classification types directly, so the registration adapter in
// driver/claude.go needs no cast.
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

// resetsTextRe matches the reset suffix claude emits on plain-text rate-limit
// messages, e.g. "resets 6:30pm (UTC)" or "resets Mon 12:00am (UTC)". The
// optional weekday is a 3-letter prefix plus any remaining letters of the full
// weekday name.
var resetsTextRe = regexp.MustCompile(`resets\s+(?:([A-Za-z]{3})\w*\s+)?(\d{1,2}):(\d{2})(am|pm)\s*\(UTC\)`)

// staleGraceWindow bounds how stale a bare-form (no weekday) reset-time
// candidate can be and still be returned as-is instead of rolled a day
// forward. dispatch/retry.go's hold path already clamps a past ResetAt's wait
// to Policy.Jitter, so a trivially stale candidate (clock skew, or a limit that
// refreshed moments ago) retries almost immediately instead of waiting ~24h.
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
// shared driverkit.BaseTransientPatterns network suffix. The patterns are
// specific so they do not match ordinary log content that happens to contain
// digit sequences (issue numbers, byte counts, port numbers).
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

// terminalExtras holds markers for non-retryable failures worth naming to the
// operator. These run through the same self-poison guard as transientExtras, so
// a box editing this string in its own content is not blamed (issues #579, #818).
var terminalExtras = []driverkit.Pattern{
	// A claude-code build predating the --agents flag rejects it outright
	// (issue #1552). Naming that instead of the generic TaskFailed bucket tells
	// the operator to bump claude-code, or blank SCOUT_MODEL/REVIEW_MODEL,
	// rather than retry.
	{Substr: "unknown option '--agents'", Reason: driverkit.UnsupportedFlag},
}

// matchMarker classifies one log line, preferring a transient API or network
// marker over a terminal CLI-usage one.
func matchMarker(line string) (driverkit.Reason, driverkit.Class, bool) {
	if r, ok := driverkit.MatchTransient(line, transientExtras); ok {
		return r, driverkit.Transient, true
	}
	if r, ok := driverkit.MatchExtras(line, terminalExtras); ok {
		return r, driverkit.Terminal, true
	}
	return "", "", false
}

type scanResult struct {
	cl       driverkit.Classification
	found    bool
	resetsAt *time.Time
}

// Classify scans the box log at logPath and reports whether the failure is
// transient (retryable) or terminal. A marker quoted inside agent-authored
// content is not attributed as the cause (issue #579, see isAgentContentEvent).
// A rate-limit marker carrying "resetsAt" sets ResetAt so callers can hold
// until then; a missing log file is terminal/taskFailed.
func Classify(logPath string) (driverkit.Classification, error) {
	return classifyAt(logPath, time.Now())
}

// classifyAt takes an explicit now so tests can pin the clock instead of
// leaking the real wall clock into the parsed resetsAt fallback (issue #2443).
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

// scanLog returns the reason and resetsAt of the last unrecovered candidate:
// agent-authored content seen after a match drops it, because the run continued
// past it. Oversized lines (> 4 MiB) are scanned in chunks, so a chunk of an
// oversized agent-content line fails the whole-chunk JSON parse in
// isAgentContentEvent and is scanned as normal (known gap, issue #579 review).
func scanLog(logPath string, now time.Time) (scanResult, error) {
	var resetsAt *time.Time
	var echoReason driverkit.Reason
	var echoPending bool
	extract := func(chunk string) driverkit.ScanDecision {
		if isAgentContentEvent(chunk) {
			// The agent's own content can quote rate-limit markers verbatim,
			// and the run continued past any candidate found so far, so drop
			// it and look for a later, genuine cause (issue #579).
			resetsAt = nil
			// Remember whether this content quoted a marker, so a later
			// type:"result" line echoing the same marker is recognized as that
			// echo rather than a fresh signal (issue #818). The pending echo
			// must survive any number of intervening non-content lines, such as
			// type:"system" heartbeats (issue #1197).
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
		// ClassifyScan latches the first unrecovered match: within one chunk a
		// transient marker beats a terminal one, but across chunks a terminal
		// marker seen first latches Terminal. Safe for the --agents case, which
		// aborts before any API call, so no transient marker precedes it.
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

// agentContentEvent is the minimal stream-json envelope for telling
// agent-authored content from a terminating API error event.
type agentContentEvent struct {
	Type    string `json:"type"`
	Error   string `json:"error"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// syntheticModelSentinel is the claude CLI's message.model value on its
// synthetic terminator event for a mid-stream API error (issue #815). No
// official doc records this literal (issue #1203), so it is a runtime contract
// with the CLI: if a future version changes it, isAgentContentEvent's guard
// silently stops matching (issue #820).
const syntheticModelSentinel = "<synthetic>"

// isAgentContentEvent reports whether chunk is a stream-json line carrying
// agent-authored content: an assistant message, or a user message (the Claude
// API returns tool results as a user-role turn). Markers inside either are the
// agent's own work product, so they must not be scanned. Non-JSON lines, other
// types, and the CLI's synthetic terminator (issue #815) fall through.
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

// resultEventEnvelope is the minimal envelope for a stream-json type:"result"
// line, whose "result" field mirrors the preceding assistant turn on an
// ordinary completion. IsError marks a genuine terminating API error, whose
// text is not an echo and must not be suppressed as one.
type resultEventEnvelope struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// resultEventText returns the "result" text of an ordinary-completion
// stream-json result line. It returns false when is_error is true, because that
// text is a genuine error, not an echo of preceding content.
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

// extractResetsAt parses the first "resetsAt":UNIX_TIMESTAMP in content as a
// UTC time, falling back to parseResetsAtText when that field is absent or its
// value is not an integer. It returns nil if neither form matches.
func extractResetsAt(content string, now time.Time) *time.Time {
	if m := resetsAtRe.FindStringSubmatch(content); m != nil {
		if secs, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			t := time.Unix(secs, 0).UTC()
			return &t
		}
	}
	return parseResetsAtText(content, now)
}

// parseResetsAtText parses claude's "resets [Weekday] <clock-time> (UTC)"
// suffix into the occurrence of that clock-time today, or on the next matching
// weekday. It returns nil only when the suffix is absent or unparseable, never
// merely because the occurrence is stale. It never calls time.Now(); the caller
// supplies now.
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

	if isWeekdayForm {
		// A weekly-cadence marker off by even minutes still means next week,
		// and a short generic backoff on a weekly-scale reset is worse than one
		// correct week-long hold, so there is no grace window here (issue #2443).
		candidate = candidate.AddDate(0, 0, 7)
		return &candidate
	}

	if now.Sub(candidate) <= staleGraceWindow {
		// The candidate is only trivially stale, so return it unchanged and
		// still in the past, letting the caller's past-ResetAt clamp retry
		// almost immediately.
		return &candidate
	}

	candidate = candidate.AddDate(0, 0, 1)
	return &candidate
}
