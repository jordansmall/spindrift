package opencode

import (
	"encoding/json"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// textEvent is the minimal NDJSON envelope RenderTranscript needs: part.text
// carries the agent's prose, including its SPINDRIFT_OUTCOME and VERDICT: lines.
type textEvent struct {
	Type string `json:"type"`
	Part struct {
		Text string `json:"text"`
	} `json:"part"`
}

// RenderTranscript returns each type:"text" event's part.text from the box log
// at logPath (one JSON object per line, per opencode's `--format json`), joined
// by "\n" in log order. It keeps each event's text verbatim, because the
// orchestrator scans this rendering for the agent's own outcome and verdict
// lines. Returns ("", nil) when logPath does not exist, as the claude Driver does.
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
