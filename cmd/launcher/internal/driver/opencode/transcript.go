package opencode

import (
	"encoding/json"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// textEvent is the minimal NDJSON envelope RenderTranscript needs: a
// type:"text" event's part.text carries the agent's assistant prose,
// including its SPINDRIFT_OUTCOME and VERDICT: lines.
type textEvent struct {
	Type string `json:"type"`
	Part struct {
		Text string `json:"text"`
	} `json:"part"`
}

// RenderTranscript scans the box log at logPath — one JSON object per line,
// per the opencode CLI's `--format json` output — and returns each
// type:"text" event's part.text, joined by "\n" in log order. Internal
// newlines within a single event's text are preserved verbatim, since the
// orchestrator's scanPassLog/scanReviewLog scan this rendering (via
// outcome.ParseAnywhere and a "VERDICT:" substring search) for the agent's
// own outcome/verdict prose, which must survive unmodified.
//
// Returns ("", nil) when logPath does not exist, matching the claude
// Driver's RenderTranscript not-found contract.
func RenderTranscript(logPath string) (string, error) {
	var texts []string
	err := driverkit.ScanLog(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev textEvent
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return
		}
		if ev.Type != "text" {
			return
		}
		texts = append(texts, ev.Part.Text)
	})
	if err != nil {
		return "", err
	}
	if len(texts) == 0 {
		return "", nil
	}
	return strings.Join(texts, "\n"), nil
}
