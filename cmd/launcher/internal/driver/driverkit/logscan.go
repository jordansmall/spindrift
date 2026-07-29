package driverkit

import (
	"errors"
	"os"

	"spindrift.dev/launcher/internal/logscan"
)

// ScanLog calls logscan.ForEachLine(path, policy, fn). A missing log file
// degrades to an empty scan: if the underlying error satisfies
// errors.Is(err, os.ErrNotExist), ScanLog returns nil without having called
// fn, so the caller falls through to its own zero-value result instead of
// treating a not-yet-written log as an error. Any other error from
// ForEachLine is returned unchanged.
func ScanLog(path string, policy logscan.Policy, fn func(line string)) error {
	err := logscan.ForEachLine(path, policy, fn)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
