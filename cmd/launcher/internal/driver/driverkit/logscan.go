package driverkit

import (
	"errors"
	"os"
	"strings"

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

// ResultText collects, one line per value, every string extract pulls out of
// the log's JSON lines: the Go twin of the in-box `jq -r '<selector>'` result
// extraction. A line extract rejects, or that is not JSON at all, is skipped.
// An empty string is dropped, as jq's `// empty` drops it. A missing log is
// "" with no error.
func ResultText(path string, extract func(line string) (string, bool)) (string, error) {
	var b strings.Builder
	err := ScanLog(path, logscan.SkipOversized, func(line string) {
		if s, ok := extract(line); ok && s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}
