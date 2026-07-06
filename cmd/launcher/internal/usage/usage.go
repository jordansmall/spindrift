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
