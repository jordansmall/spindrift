package main

import (
	"bytes"
	"time"

	"spindrift.dev/sdk/event"
)

// maxLine is the host's scan buffer size: a log line of maxLine bytes or more is
// skipped whole, as native's bufio.ReadLine skips it. A trailing \r that
// ReadLine would strip stays here; the decoder's TrimSpace makes that moot.
const maxLine = 4 * 1024 * 1024

// ResolveExit returns exitCode unless it is 0 and log holds an unrecovered
// error, in which case it returns 1: pi exits 0 after a failed turn (a 400, a
// 401, or retries exhausted) and reports it only as a stopReason "error"
// message. An error followed by agent progress (see event.IsProgress), as when
// an auto-retry succeeds, was recovered from, as in the host's classifier. It
// never judges outcome-line presence; an empty log passes exitCode through.
func ResolveExit(log []byte, exitCode int) (int, error) {
	if exitCode != 0 {
		return exitCode, nil
	}
	sawError := false
	decode := NewDecoder(time.Time{})
	for line := range bytes.SplitSeq(log, []byte("\n")) {
		if len(line) >= maxLine {
			continue
		}
		for _, e := range decode(string(line)) {
			switch {
			case e.Kind == event.KindError:
				sawError = true
			case event.IsProgress(e.Kind):
				sawError = false
			}
		}
	}
	if sawError {
		return 1, nil
	}
	return 0, nil
}
