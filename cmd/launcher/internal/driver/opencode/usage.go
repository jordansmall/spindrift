package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

// stepEvent decodes the fields of one opencode NDJSON line that this file
// cares about: a step_start's timestamp (the run's start-of-turn marker) or a
// step_finish's timestamp, per-turn token/cost tallies, and cache figures.
type stepEvent struct {
	Type      string   `json:"type"`
	Timestamp int64    `json:"timestamp"`
	Part      stepPart `json:"part"`
}

type stepPart struct {
	MessageID string     `json:"messageID"`
	Tokens    stepTokens `json:"tokens"`
	Cost      float64    `json:"cost"`
}

type stepTokens struct {
	Input     int            `json:"input"`
	Output    int            `json:"output"`
	Reasoning int            `json:"reasoning"`
	Cache     stepTokenCache `json:"cache"`
}

type stepTokenCache struct {
	Write int `json:"write"`
	Read  int `json:"read"`
}

// ExtractUsage scans logPath — an opencode NDJSON run log — and sums the
// per-turn token/cost tallies carried by every step_finish event into one
// aggregate usage.Report.
//
// Each step_finish is an independent per-turn tally, not a cumulative
// snapshot (unlike claude-code's single result event), so aggregation here
// is a plain sum over every step_finish line — no dedup or "last one wins"
// logic is needed. Reasoning tokens are folded into OutputTokens since
// opencode reports them as a separate token class but spindrift's
// usage.Usage has no distinct reasoning field. DurationMs is wall-clock: the
// last step_finish timestamp minus the first step_start timestamp seen in
// the log. DurationApiMs is left zero — opencode's NDJSON stream carries no
// separate api-only timing. Per-model breakdown (usage.Report.Models) is out
// of scope for this slice and left nil (issue #262).
//
// Returns usage.Report{Found: false} when the log contains no step_finish
// event or does not exist. Returns (usage.Report{}, err) on other I/O
// errors.
func ExtractUsage(logPath string) (usage.Report, error) {
	var u usage.Usage
	var firstStart, lastFinish int64
	haveStart := false

	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev stepEvent
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			return
		}
		switch ev.Type {
		case "step_start":
			if !haveStart {
				firstStart = ev.Timestamp
				haveStart = true
			}
		case "step_finish":
			u.InputTokens += ev.Part.Tokens.Input
			u.OutputTokens += ev.Part.Tokens.Output + ev.Part.Tokens.Reasoning
			u.CacheReadInputTokens += ev.Part.Tokens.Cache.Read
			u.CacheCreationInputTokens += ev.Part.Tokens.Cache.Write
			u.TotalCostUSD += ev.Part.Cost
			u.NumTurns++
			lastFinish = ev.Timestamp
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usage.Report{Found: false}, nil
		}
		return usage.Report{}, err
	}

	if u.NumTurns == 0 {
		return usage.Report{Found: false}, nil
	}

	// Only report wall-clock when a step_start anchored the window; a
	// step_finish with no preceding step_start would otherwise subtract a
	// zero firstStart and yield a raw epoch-ms figure.
	if haveStart {
		u.DurationMs = lastFinish - firstStart
	}
	return usage.Report{Usage: u, Found: true}, nil
}
