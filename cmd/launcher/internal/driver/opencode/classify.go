// Package opencode is the opencode Driver's host-side half (ADR 0009): the
// transient-error taxonomy and NDJSON event parsing for the `opencode run
// --format json` transcript shape (one JSON object per line — no envelope
// wrapping the whole stream, unlike claude's stream-json array-of-events
// framing). The parent driver package owns the Driver interface, the shared
// Class/Reason/Classification vocabulary, and the registry wiring; this
// package must not import it (the registration adapter in
// driver/opencode.go imports this package, not the other way around, to
// avoid a cycle) — Classify therefore returns its own Class/Reason values,
// mirrored 1:1 onto driver.Class/driver.Reason by that adapter.
package opencode

import (
	"encoding/json"
	"errors"
	"os"
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
	RateLimit  Reason = "rateLimit"  // API 429 rate limit
	Overloaded Reason = "overloaded" // API 529 / overloaded_error / capacity error
	Network    Reason = "network"    // transient network failure
	TaskFailed Reason = "taskFailed" // agent ran but produced no valid result
)

// Classification is the result of Classify.
type Classification struct {
	Class   Class
	Reason  Reason
	ResetAt *time.Time // non-nil only for RateLimit with a known reset time
}

// transientPatterns lists log-line substrings that mark a transient failure,
// mirrored from driver/claude/classify.go's own table (the same underlying
// API-error and network-failure vocabulary applies regardless of which CLI
// is fronting the model). The first match in the ordered list wins when
// multiple markers appear on the same line.
var transientPatterns = []struct {
	substr string
	reason Reason
}{
	{"rate_limit_error", RateLimit},
	{"429", RateLimit},
	{"overloaded_error", Overloaded},
	{"529", Overloaded},
	{"Overloaded", Overloaded},
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// event is the minimal NDJSON envelope Classify needs: enough to tell a
// type:"error" event (scanned for transient markers) from a type:"text"
// event (agent-authored prose, never scanned — see the package doc).
type event struct {
	Type string `json:"type"`
}

// Classify scans the box log at logPath — one JSON object per line, per the
// opencode CLI's `--format json` output — and returns a Classification
// describing whether the failure is transient (retryable) or terminal
// (genuine).
//
// Only type:"error" event lines are scanned for transient markers; a marker
// quoted inside a type:"text" event (the agent's own prose, e.g. discussing
// rate-limit code) is not attributed as the cause. A log with no
// type:"error" event at all — or one whose error text carries no known
// marker — classifies as Terminal/TaskFailed, as does a missing log file.
func Classify(logPath string) (Classification, error) {
	found := false
	var reason Reason
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev event
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return
		}
		if ev.Type != "error" {
			return
		}
		if r, ok := matchTransient(s); ok {
			found = true
			reason = r
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Classification{Class: Terminal, Reason: TaskFailed}, nil
		}
		return Classification{}, err
	}
	if !found {
		return Classification{Class: Terminal, Reason: TaskFailed}, nil
	}
	return Classification{Class: Transient, Reason: reason}, nil
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
