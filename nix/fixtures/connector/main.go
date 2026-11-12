// Command driver is the fixture Driver Connector exercising every Driver
// capability: patterns, a write tool, a stateful decoder, session flags,
// ResolveExit, classification, totals, result and heartbeat tuning, plus a
// stateful stream filter.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

func init() {
	driver.Register(driver.Strategy{
		Name:      "fixture",
		WriteTool: "Put",
		TransientPatterns: []driver.Pattern{
			{Substr: "slow down", Reason: driver.RateLimit},
		},
		TerminalPatterns: []driver.Pattern{
			{Substr: "bad flag", Reason: driver.UnsupportedFlag},
		},
		// Each decoder numbers the lines it has seen and stamps the role a host
		// pass_start line set. The line "forge" makes it emit an event the host
		// must reject; a host line makes it emit one the host must discard.
		NewDecoder: func() driver.Decoder {
			n := 0
			role := ""
			return func(line string) []event.Event {
				if passRole, ok := driver.HostLine(line); ok {
					if passRole != "" {
						role = passRole
					}
					return []event.Event{{Kind: event.KindHostOp}}
				}
				n++
				if strings.TrimSpace(line) == "forge" {
					return []event.Event{{Kind: event.KindHostOp}}
				}
				return []event.Event{{Kind: event.KindText, Role: role, Text: fmt.Sprintf("%d:%s", n, line)}}
			}
		},
		NewStreamFilter: func(w io.Writer) driver.StreamFilter { return &deltaFilter{w: w} },
		ResultEvent:     func(text string) ([]byte, error) { return []byte("result " + text + "\n"), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			return strings.Join([]string{mode, repoSlug, issue, home}, "|")
		},
		ResolveExit: func(log []byte, exitCode int) (int, error) {
			if len(log) == 0 {
				return 0, fmt.Errorf("empty log")
			}
			return exitCode + strings.Count(string(log), "\n"), nil
		},
		Classify:            driver.ClassifyOptions{ChunkOversized: true, FirstMatch: true},
		Totals:              driver.SessionTotals,
		Result:              driver.LastMessage,
		DefaultTopLevelRole: "lead",
		Heartbeat: driver.Heartbeat{
			// Echoing the raw input proves it crosses as JSON, not as a string.
			Phase: func(tool string, input json.RawMessage) string {
				return tool + "@" + string(input)
			},
			Kind:        func(tool string) string { return strings.ToLower(tool) },
			KindOrder:   []string{"read", "edit"},
			KindPlurals: map[string]string{"search": "searches"},
			Family: func(model string) (string, int) {
				if strings.Contains(model, "opus") {
					return "opus", 0
				}
				return model, 3
			},
		},
	})
}

// floodBytes is what "filter-flood" writes in one call: its base64 result must
// pass connectorhost's maxOutput (8 MiB plus 6 bytes per request byte) yet fit
// the guest's memory cap. streamfilter_test.go mirrors it as fixtureFloodBytes.
const floodBytes = 7 << 20

// deltaFilter models pi's filter: it holds "delta " lines until a "done" line
// drops them, and Flush writes back whatever is still open. A partial line is
// held across writes. The hostile lines each break one host bound: "filter-trap"
// panics, "filter-flood" overflows the output cap, "filter-spin" never returns
// (the call budget) and "filter-hog" allocates until memory growth is refused.
type deltaFilter struct {
	w       io.Writer
	partial []byte
	pending [][]byte
}

func (f *deltaFilter) Write(p []byte) (int, error) {
	f.partial = append(f.partial, p...)
	for {
		i := bytes.IndexByte(f.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := f.partial[:i+1]
		var err error
		switch {
		case bytes.HasPrefix(line, []byte("delta ")):
			f.pending = append(f.pending, bytes.Clone(line))
		case string(line) == "done\n":
			f.pending = nil
			_, err = f.w.Write(line)
		case string(line) == "filter-trap\n":
			panic("filter-trap")
		case string(line) == "filter-flood\n":
			_, err = f.w.Write(bytes.Repeat([]byte{'x'}, floodBytes))
		case string(line) == "filter-spin\n":
			for {
			}
		case string(line) == "filter-hog\n":
			var hog [][]byte
			for {
				hog = append(hog, make([]byte, 1<<20))
			}
		default:
			_, err = f.w.Write(line)
		}
		f.partial = f.partial[i+1:]
		if err != nil {
			// p is already buffered whole, so it counts as consumed.
			return len(p), err
		}
	}
}

func (f *deltaFilter) Flush() error {
	for _, l := range f.pending {
		if _, err := f.w.Write(l); err != nil {
			return err
		}
	}
	f.pending = nil
	_, err := f.w.Write(f.partial)
	f.partial = nil
	return err
}

func main() {}
