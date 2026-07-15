package claude

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

type resultEvent struct {
	Type          string    `json:"type"`
	NumTurns      int       `json:"num_turns"`
	TotalCostUSD  float64   `json:"total_cost_usd"`
	DurationMs    int64     `json:"duration_ms"`
	DurationApiMs int64     `json:"duration_api_ms"`
	UsageData     usageData `json:"usage"`
}

type usageData struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// LastInLog scans the file at path and returns the last result event parsed
// as a usage.Usage. Lines larger than the 4 MiB scan buffer are skipped
// rather than aborting the scan; the last result event wins.
//
// Returns (usage.Usage{}, false, nil) when no result event is present or the
// file does not exist. Returns (usage.Usage{}, false, err) on I/O errors
// other than file-not-found or oversized lines.
func LastInLog(path string) (usage.Usage, bool, error) {
	var last *usage.Usage
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if strings.Contains(s, `"type":"result"`) {
			var ev resultEvent
			if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr == nil && ev.Type == "result" {
				u := usage.Usage{
					InputTokens:              ev.UsageData.InputTokens,
					OutputTokens:             ev.UsageData.OutputTokens,
					CacheReadInputTokens:     ev.UsageData.CacheReadInputTokens,
					CacheCreationInputTokens: ev.UsageData.CacheCreationInputTokens,
					TotalCostUSD:             ev.TotalCostUSD,
					DurationMs:               ev.DurationMs,
					DurationApiMs:            ev.DurationApiMs,
					NumTurns:                 ev.NumTurns,
				}
				last = &u
			}
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usage.Usage{}, false, nil
		}
		return usage.Usage{}, false, err
	}

	if last == nil {
		return usage.Usage{}, false, nil
	}
	return *last, true, nil
}

// BreakdownByRole scans the file at path and returns per-role token
// breakdowns by parsing assistant message events. Messages with no
// parent_tool_use_id are attributed to the implementor. Task tool-use IDs are
// mapped to roles via the subagent_type field in each Task's input (e.g.
// "scout", "reviewer").
//
// Returns (nil, nil) when the file does not exist.
func BreakdownByRole(path string) ([]usage.RoleUsage, error) {
	var lines []string
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if s := strings.TrimSpace(line); s != "" {
			lines = append(lines, s)
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	// Pass 1: collect Task tool-use IDs → role name from implementor messages.
	// The subagent_type field in each Task's input (e.g. "scout", "reviewer")
	// is the ground-truth role, so re-invocations of the same role accumulate
	// correctly rather than being mis-attributed by position.
	taskRole := make(map[string]string)
	for _, line := range lines {
		if !strings.Contains(line, `"type":"assistant"`) {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		CollectTaskRoles(ev, taskRole)
	}

	// Pass 2: accumulate usage per role.
	buckets := make(map[string]*usage.RoleUsage)
	ensure := func(role string) *usage.RoleUsage {
		if b, ok := buckets[role]; ok {
			return b
		}
		b := &usage.RoleUsage{Role: role}
		buckets[role] = b
		return b
	}

	for _, line := range lines {
		if !strings.Contains(line, `"type":"assistant"`) {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		if ev.Message == nil {
			continue
		}
		role := ResolveRole(ev, taskRole)
		b := ensure(role)
		b.InputTokens += ev.Message.Usage.InputTokens
		b.OutputTokens += ev.Message.Usage.OutputTokens
		b.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
		b.CacheCreationInputTokens += ev.Message.Usage.CacheCreationInputTokens
	}

	// Return in deterministic order: implementor, scout, reviewer, filer, then others.
	order := []string{"implementor", "scout", "reviewer", "filer", "subagent"}
	var result []usage.RoleUsage
	for _, role := range order {
		if b, ok := buckets[role]; ok {
			result = append(result, *b)
		}
	}
	return result, nil
}

// breakdownByRole indirects to BreakdownByRole so tests can simulate a
// BreakdownByRole I/O error without a real filesystem race between it and
// the LastInLog scan.
var breakdownByRole = BreakdownByRole

// ExtractUsage scans logPath for its result event and, separately, its
// per-role breakdown, returning both in one usage.Report — the claude
// Driver's implementation of the Driver interface's ExtractUsage method.
func ExtractUsage(logPath string) (usage.Report, error) {
	u, found, err := LastInLog(logPath)
	if err != nil {
		return usage.Report{}, err
	}
	if !found {
		return usage.Report{}, nil
	}
	// A BreakdownByRole I/O error degrades the per-role section, not the
	// aggregate totals already parsed above — see issue #674.
	roles, err := breakdownByRole(logPath)
	if err != nil {
		roles = nil
	}
	return usage.Report{Usage: u, Found: true, Roles: roles}, nil
}
