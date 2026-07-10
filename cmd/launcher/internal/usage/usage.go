// Package usage parses the aggregate usage statistics from the final result
// event in a Box run log.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/claudetranscript"
)

// FormatDuration converts a millisecond count to a human-readable string.
// Outputs "Xh Ym Zs", "Xm Ys", or "Xs" depending on magnitude.
func FormatDuration(ms int64) string {
	s := ms / 1000
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// RoleUsage holds the aggregated token usage attributed to one subagent role.
type RoleUsage struct {
	Role                     string
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// Usage holds the aggregate statistics from a result event.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	TotalCostUSD             float64
	DurationMs               int64
	DurationApiMs            int64
	NumTurns                 int
}

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
// as a Usage. Lines larger than the 4 MiB scan buffer are skipped rather than
// aborting the scan; the last result event wins.
//
// Returns (Usage{}, false, nil) when no result event is present or the file
// does not exist. Returns (Usage{}, false, err) on I/O errors other than
// file-not-found or oversized lines.
func LastInLog(path string) (Usage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Usage{}, false, nil
		}
		return Usage{}, false, err
	}
	defer f.Close()

	const bufSize = 4 * 1024 * 1024
	r := bufio.NewReaderSize(f, bufSize)
	var last *Usage
	for {
		line, isPrefix, err := r.ReadLine()
		if isPrefix {
			for isPrefix {
				_, isPrefix, err = r.ReadLine()
				if err != nil {
					break
				}
			}
		} else if err == nil {
			s := strings.TrimSpace(string(line))
			if strings.Contains(s, `"type":"result"`) {
				var ev resultEvent
				if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr == nil && ev.Type == "result" {
					u := Usage{
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
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Usage{}, false, err
		}
	}

	if last == nil {
		return Usage{}, false, nil
	}
	return *last, true, nil
}

// BreakdownByRole scans the file at path and returns per-role token breakdowns
// by parsing assistant message events. Messages with no parent_tool_use_id are
// attributed to the implementor. Task tool-use IDs are mapped to roles via the
// subagent_type field in each Task's input (e.g. "scout", "reviewer").
//
// Returns (nil, nil) when the file does not exist.
func BreakdownByRole(path string) ([]RoleUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	const bufSize = 4 * 1024 * 1024
	r := bufio.NewReaderSize(f, bufSize)
	var lines []string
	for {
		line, isPrefix, err := r.ReadLine()
		if isPrefix {
			for isPrefix {
				_, isPrefix, err = r.ReadLine()
				if err != nil {
					break
				}
			}
		} else if err == nil {
			s := strings.TrimSpace(string(line))
			if s != "" {
				lines = append(lines, s)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
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
		var ev claudetranscript.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		claudetranscript.CollectTaskRoles(ev, taskRole)
	}

	// Pass 2: accumulate usage per role.
	buckets := make(map[string]*RoleUsage)
	ensure := func(role string) *RoleUsage {
		if b, ok := buckets[role]; ok {
			return b
		}
		b := &RoleUsage{Role: role}
		buckets[role] = b
		return b
	}

	for _, line := range lines {
		if !strings.Contains(line, `"type":"assistant"`) {
			continue
		}
		var ev claudetranscript.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		if ev.Message == nil {
			continue
		}
		role := claudetranscript.ResolveRole(ev, taskRole)
		b := ensure(role)
		b.InputTokens += ev.Message.Usage.InputTokens
		b.OutputTokens += ev.Message.Usage.OutputTokens
		b.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
		b.CacheCreationInputTokens += ev.Message.Usage.CacheCreationInputTokens
	}

	// Return in deterministic order: implementor, scout, reviewer, filer, then others.
	order := []string{"implementor", "scout", "reviewer", "filer", "subagent"}
	var result []RoleUsage
	for _, role := range order {
		if b, ok := buckets[role]; ok {
			result = append(result, *b)
		}
	}
	return result, nil
}
