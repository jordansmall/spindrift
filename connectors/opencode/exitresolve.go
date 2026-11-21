package main

import (
	"bytes"
	"strings"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

// scanBufSize is the native logscan line buffer. Its SkipOversized policy drops
// a line whose bytes before the newline reach it, so ResolveExit must drop the
// same lines: the host's own oversize skip is looser and lets 4-8 MiB lines
// through.
const scanBufSize = 4 * 1024 * 1024

// ResolveExit returns exitCode unless it is 0 and log holds an unrecovered
// type:"error" event, in which case it returns 1: the opencode CLI exits 0 even
// after a mid-run error. An error followed by agent progress (see
// event.IsProgress) was recovered from, as in the host's classifier. It never
// judges outcome-line presence.
func ResolveExit(log []byte, exitCode int) (int, error) {
	if exitCode != 0 {
		return exitCode, nil
	}
	sawError := false
	for len(log) > 0 {
		line, rest, _ := bytes.Cut(log, []byte{'\n'})
		log = rest
		if len(line) >= scanBufSize {
			continue
		}
		root, ok := driver.Scan(strings.TrimSpace(string(line)))
		if !ok {
			continue
		}
		if isErrorEvent(root) {
			sawError = true
			continue
		}
		evs, _ := decodeRoot(root)
		for _, e := range evs {
			if event.IsProgress(e.Kind) {
				sawError = false
			}
		}
	}
	if sawError {
		return 1, nil
	}
	return 0, nil
}

// isErrorEvent reports whether root is a type:"error" event. It reads only
// type and part.text, so a malformed field elsewhere on an error line still
// counts as an error, where decodeRoot would drop the line to KindUnparsed.
// part.text stays only for parity with the native decode: an error line whose
// part.text is not a string is dropped, not counted.
func isErrorEvent(root driver.Value) bool {
	if !objectOrNull(root) {
		return false
	}
	var typ, text string
	for k, v := range root.Members() {
		switch {
		case driver.KeyMatches(k, "type"):
			if !v.DecodeString(&typ) {
				return false
			}
		case driver.KeyMatches(k, "part"):
			if !objectOrNull(v) {
				return false
			}
			for pk, pv := range v.Members() {
				if driver.KeyMatches(pk, "text") && !pv.DecodeString(&text) {
					return false
				}
			}
		}
	}
	return typ == "error"
}
