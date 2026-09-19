// Package opencode is the opencode Driver's host-side half (ADR 0009): the
// transient-error taxonomy and NDJSON event parsing for `opencode run
// --format json`, which emits one JSON object per line with no envelope
// wrapping the stream. This package uses driverkit's Class/Reason types
// directly, so the registration adapter in driver/opencode.go needs no cast.
package opencode

import (
	"encoding/json"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// transientExtras is opencode's complete ordered marker list, checked before
// the shared driverkit.BaseTransientPatterns network suffix. opencode supplies
// its own rather than passing none, to keep the bare "429"/"529" markers and
// their precedence over "overloaded_error"/"Overloaded".
var transientExtras = []driverkit.Pattern{
	{Substr: "rate_limit_error", Reason: driverkit.RateLimit},
	{Substr: "429", Reason: driverkit.RateLimit},
	{Substr: "overloaded_error", Reason: driverkit.Overloaded},
	{Substr: "529", Reason: driverkit.Overloaded},
	{Substr: "Overloaded", Reason: driverkit.Overloaded},
}

type event struct {
	Type string `json:"type"`
}

// Classify scans the box log at logPath and reports whether the failure is
// transient (retryable) or terminal. Only type:"error" lines are scanned, so a
// marker quoted in the agent's own type:"text" prose is not attributed as the
// cause. A log with no error event, or whose error text carries no known
// marker, classifies as Terminal/TaskFailed, as does a missing log file.
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
		return driverkit.ScanDecision{Text: s, Overwrite: true}
	}, transientExtras, nil)
	if err != nil {
		return driverkit.Classification{}, err
	}
	if !found {
		return driverkit.Classification{Class: driverkit.Terminal, Reason: driverkit.TaskFailed}, nil
	}
	return cl, nil
}
