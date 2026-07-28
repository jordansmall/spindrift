package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/outcome"
)

// failureExitCode is the synthesized exit code SynthesizeExit reports when
// it finds no trustworthy evidence of success. Any non-zero value works —
// callers only branch on zero vs. non-zero — but a fixed constant keeps the
// two return sites below consistent.
const failureExitCode = 1

// SynthesizeExit returns 0 iff logPath contains both a valid
// SPINDRIFT_OUTCOME line in some type:"text" event's part.text and no
// type:"error" event anywhere in the log; otherwise it returns a non-zero
// code. This exists because the opencode CLI exits 0 even when it hit an
// error mid-run — unlike claude, whose stream-json type:"result" event
// carries its own trustworthy is_error/subtype fields — so a caller cannot
// rely on the opencode process's own exit code to detect a mid-run failure.
//
// A missing log file — no evidence of a valid outcome — returns
// failureExitCode.
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
