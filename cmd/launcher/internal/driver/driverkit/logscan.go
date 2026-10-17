package driverkit

import (
	"errors"
	"os"

	"spindrift.dev/launcher/internal/logscan"
)

// ScanLog calls logscan.ForEachLine and degrades a missing log file to an
// empty scan, so a caller reading a not-yet-written log falls through to its
// own zero-value result instead of seeing an error.
func ScanLog(path string, policy logscan.Policy, fn func(line string)) error {
	err := logscan.ForEachLine(path, policy, fn)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
