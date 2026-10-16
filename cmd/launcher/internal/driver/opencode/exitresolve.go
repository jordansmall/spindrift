package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

// ResolveExit returns exitCode unless it is 0 and logPath holds a type:"error"
// event, in which case it returns 1: the opencode CLI exits 0 even after a
// mid-run error. It never judges outcome-line presence; a missing log passes
// exitCode through.
func ResolveExit(logPath string, exitCode int) (int, error) {
	if exitCode != 0 {
		return exitCode, nil
	}
	sawError := false
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev textEvent
		if json.Unmarshal([]byte(s), &ev) == nil && ev.Type == "error" {
			sawError = true
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if sawError {
		return 1, nil
	}
	return 0, nil
}
