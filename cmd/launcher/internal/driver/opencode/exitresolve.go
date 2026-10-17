package opencode

import (
	"errors"
	"os"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// ResolveExit returns exitCode unless it is 0 and logPath holds an unrecovered
// type:"error" event, in which case it returns 1: the opencode CLI exits 0 even
// after a mid-run error. An error followed by agent progress (see
// driverkit.IsProgress) was recovered from, as in driverkit.Classify. It never
// judges outcome-line presence; a missing log passes exitCode through.
func ResolveExit(logPath string, exitCode int) (int, error) {
	if exitCode != 0 {
		return exitCode, nil
	}
	sawError := false
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		if isErrorEvent(line) {
			sawError = true
			return
		}
		for _, e := range Decode(line) {
			if driverkit.IsProgress(e.Kind) {
				sawError = false
			}
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
