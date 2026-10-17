package driverkit

import "spindrift.dev/launcher/internal/logscan"

// scanEvents decodes each line of the log at path with a fresh decoder and
// calls fn with every event, in order. A missing log is an empty scan, and an
// oversized line is skipped whole, as for every reader that scans raw lines.
func scanEvents(path string, newDecoder func() Decoder, fn func(Event)) error {
	decode := newDecoder()
	return ScanLog(path, logscan.SkipOversized, func(line string) {
		for _, ev := range decode(line) {
			fn(ev)
		}
	})
}
