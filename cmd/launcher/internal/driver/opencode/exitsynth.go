package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/outcome"
)

// failureExitCode is the exit code SynthesizeExit reports when it finds no
// trustworthy evidence of success.
const failureExitCode = 1

// SynthesizeExit returns 0 only when logPath holds a valid SPINDRIFT_OUTCOME
// line in a type:"text" event and no type:"error" event anywhere. The opencode
// CLI exits 0 even after a mid-run error, so its own exit code cannot tell a
// caller whether the run failed. A missing log file returns failureExitCode.
func SynthesizeExit(logPath string) (int, error) {
	sawError := false
	hasOutcome := false
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev textEvent
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return
		}
		switch ev.Type {
		case "error":
			sawError = true
		case "text":
			for _, textLine := range strings.Split(ev.Part.Text, "\n") {
				if _, ok := outcome.ParseAnywhere(textLine); ok {
					hasOutcome = true
				}
			}
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return failureExitCode, nil
		}
		return failureExitCode, err
	}
	if hasOutcome && !sawError {
		return 0, nil
	}
	return failureExitCode, nil
}
