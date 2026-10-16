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

// RenderTranscript returns each non-empty type:"text" event's part.text from
// the box log at logPath (one JSON object per line, per opencode's `--format
// json`), joined by "\n" in log order. The first physical line of each event is
// prefixed "[role] " (topLevelRole, defaulting to driverkit.ImplementorRole),
// because the orchestrator's verdict scan counts only "[role]"-prefixed lines
// and slices the review findings from the lines that follow; embedded newlines
// stay verbatim so continuation lines remain bare. The outcome line is read
// from ResultText instead. Returns ("", nil) when logPath does not exist, as
// the claude Driver does.
func RenderTranscript(logPath, topLevelRole string) (string, error) {
	if topLevelRole == "" {
		topLevelRole = driverkit.ImplementorRole
	}
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
		// Trimmed so a leading newline cannot push a VERDICT: line off the
		// prefixed first line.
		text := strings.TrimSpace(ev.Part.Text)
		if text == "" {
			return
		}
		texts = append(texts, "["+topLevelRole+"] "+text)
	})
	if err != nil {
		return "", err
	}
	if len(texts) == 0 {
		return "", nil
	}
	return strings.Join(texts, "\n"), nil
}
