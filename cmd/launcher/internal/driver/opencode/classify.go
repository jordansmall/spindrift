// Package opencode is the opencode Driver's host-side half (ADR 0009): the
// transient-error taxonomy and NDJSON event parsing for the `opencode run
// --format json` transcript shape (one JSON object per line — no envelope
// wrapping the whole stream, unlike claude's stream-json array-of-events
// framing). The parent driver package owns the Driver interface and the
// registry wiring; the shared Class/Reason/Classification vocabulary lives
// in driverkit, and this package uses driverkit's types directly (no local
// aliases), so the registration adapter in driver/opencode.go needs no cast
// between this package's and driver's Class/Reason values.
package opencode

import (
	"encoding/json"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// transientExtras holds opencode's complete ordered marker list, checked
// before the shared driverkit.BaseTransientPatterns network suffix.
// opencode deliberately supplies these (it does NOT "pass none") to preserve
// its loose-digit "429"/"529" markers and their original precedence over
// "overloaded_error"/"Overloaded".
var transientExtras = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "429", Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "529", Reason: driverkit.Overloaded},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
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
		return driverkit.ScanDecision{Text: s}
	}, transientExtras, nil)
	if err != nil {
		return driverkit.Classification{}, err
	}
	if !found {
		return driverkit.Classification{Class: driverkit.Terminal, Reason: driverkit.TaskFailed}, nil
	}
	return cl, nil
}
