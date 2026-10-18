package pi

import (
	"errors"
	"os"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
)

// ResolveExit returns exitCode unless it is 0 and logPath holds an unrecovered
// error, in which case it returns 1: pi exits 0 after a failed turn (a 400, a
// 401, or retries exhausted) and reports it only as a stopReason "error"
// message. An error followed by agent progress (see driverkit.IsProgress), as
// when an auto-retry succeeds, was recovered from, as in driverkit.Classify.
// It never judges outcome-line presence; a missing log passes exitCode through.
func ResolveExit(logPath string, exitCode int) (int, error) {
	if exitCode != 0 {
		return exitCode, nil
	}
	sawError := false
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		for _, e := range Decode(line) {
			switch {
			case e.Kind == driverkit.KindError:
				sawError = true
			case driverkit.IsProgress(e.Kind):
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
