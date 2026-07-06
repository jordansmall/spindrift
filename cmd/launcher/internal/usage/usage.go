// Package usage parses the aggregate usage statistics from the final result
// event in a Box run log.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

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

type assistantEvent struct {
	Type            string        `json:"type"`
	Message         *assistantMsg `json:"message,omitempty"`
	ParentToolUseID string        `json:"parent_tool_use_id,omitempty"`
}

type assistantMsg struct {
	Content []msgContent `json:"content"`
	Usage   usageData    `json:"usage"`
}

type msgContent struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// BreakdownByRole scans the file at path and returns per-role token breakdowns
// by parsing assistant message events. Messages with no parent_tool_use_id are
// attributed to the implementor. Task tool-use IDs in the implementor's messages
// are mapped to roles in encounter order: first Task → scout, second → reviewer.
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

	// Pass 1: collect Task tool-use IDs in encounter order from implementor messages.
	var taskOrder []string
	for _, line := range lines {
		if !strings.Contains(line, `"type":"assistant"`) {
			continue
		}
		var ev assistantEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		if ev.ParentToolUseID != "" || ev.Message == nil {
			continue
		}
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" && c.Name == "Task" && c.ID != "" {
				taskOrder = append(taskOrder, c.ID)
			}
		}
	}

	// Build task ID → role name map.
	roleNames := []string{"scout", "reviewer"}
	taskRole := make(map[string]string, len(taskOrder))
	for i, id := range taskOrder {
		if i < len(roleNames) {
			taskRole[id] = roleNames[i]
		} else {
			taskRole[id] = "subagent"
		}
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
		var ev assistantEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		if ev.Message == nil {
			continue
		}
		role := "implementor"
		if ev.ParentToolUseID != "" {
			if r, ok := taskRole[ev.ParentToolUseID]; ok {
				role = r
			} else {
				role = "subagent"
			}
		}
		b := ensure(role)
		b.InputTokens += ev.Message.Usage.InputTokens
		b.OutputTokens += ev.Message.Usage.OutputTokens
		b.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
		b.CacheCreationInputTokens += ev.Message.Usage.CacheCreationInputTokens
	}

	// Return in deterministic order: implementor, scout, reviewer, then others.
	order := []string{"implementor", "scout", "reviewer", "subagent"}
	var result []RoleUsage
	for _, role := range order {
		if b, ok := buckets[role]; ok {
			result = append(result, *b)
		}
	}
	return result, nil
}
